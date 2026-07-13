package loader

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/tspath"
	"github.com/microsoft/typescript-go/internal/transformers/runtyped"
	"github.com/microsoft/typescript-go/internal/vfs"
	"github.com/microsoft/typescript-go/internal/vfs/cachedvfs"
	"github.com/microsoft/typescript-go/internal/vfs/osvfs"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
)

// ReflectionMode controls how reflection is applied.
type ReflectionMode string

const (
	// ReflectionDefault enables reflection for all types.
	ReflectionDefault ReflectionMode = "default"
	// ReflectionNever disables all reflection.
	ReflectionNever ReflectionMode = "never"
	// ReflectionExplicit only reflects types marked with @reflection JSDoc.
	ReflectionExplicit ReflectionMode = "explicit"
)

// LoaderOptions configures the Loader.
type LoaderOptions struct {
	// Reflection overrides tsconfig reflection setting.
	// If empty, uses tsconfig's reflection setting (or "never" if no tsconfig).
	Reflection ReflectionMode

	// CompilerOptions merged with defaults.
	CompilerOptions *core.CompilerOptions
}

// globalMu serializes all loader transforms across all loader instances.
// This is necessary because the reflection mode override is a process-global
// value used during compilation, and concurrent transforms with different
// modes would race.
var globalMu sync.Mutex

// Loader is a stateful transformer that manages file state across transforms.
// It is the Go equivalent of DeepkitLoader, designed for use by bundlers
// and build tools that need to transform TypeScript files one at a time
// while maintaining cross-file type resolution.
type Loader struct {
	mu      sync.Mutex
	options LoaderOptions

	// knownFiles stores source code by path for cross-file resolution
	knownFiles map[string]string

	// fileTimes tracks when each file was last modified (for VFS)
	fileTimes map[string]time.Time
}

// NewLoader creates a new Loader with the given options.
func NewLoader(opts LoaderOptions) *Loader {
	if opts.CompilerOptions == nil {
		opts.CompilerOptions = &core.CompilerOptions{}
	}
	return &Loader{
		options:    opts,
		knownFiles: make(map[string]string),
		fileTimes:  make(map[string]time.Time),
	}
}

// Transform transforms a TypeScript source file with type reflection.
// The path must be an absolute path — it's used for cross-file type resolution.
// Returns the transformed JavaScript.
func (l *Loader) Transform(source, path string) (string, error) {
	// Update known files (compileAndEmit will hold the lock for the rest)
	l.mu.Lock()
	l.knownFiles[path] = source
	l.fileTimes[path] = time.Now()
	l.mu.Unlock()

	return l.compileAndEmit(source, path)
}

// compileAndEmit creates a Program with the known files and emits JS for the target file.
func (l *Loader) compileAndEmit(source, path string) (string, error) {
	// Hold the global lock for the entire compile to prevent race on reflectionModeOverride
	globalMu.Lock()
	defer globalMu.Unlock()

	files := make(map[string]string, len(l.knownFiles))
	for k, v := range l.knownFiles {
		files[k] = v
	}

	// Build VFS: overlay known files on real filesystem
	fsMap := make(map[string]any)
	for filePath, content := range files {
		fsMap[filePath] = &fstest.MapFile{
			Data: []byte(content),
		}
	}

	// Create VFS from map, wrap with bundled lib files
	baseFS := vfstest.FromMap(fsMap, true)
	baseFS = bundled.WrapFS(baseFS)

	// Also wrap with real OS filesystem so node_modules and other files are accessible
	osFS := osvfs.FS()
	overlayFS := &overlayFS{base: baseFS, overlay: osFS}

	fs := cachedvfs.From(overlayFS)

	// Determine compiler options
	compilerOptions := l.options.CompilerOptions.Clone()
	if compilerOptions.Target == core.ScriptTargetNone {
		compilerOptions.Target = core.ScriptTargetESNext
	}
	if compilerOptions.Module == core.ModuleKindNone {
		compilerOptions.Module = core.ModuleKindESNext
	}
	if compilerOptions.ModuleResolution == core.ModuleResolutionKindUnknown {
		compilerOptions.ModuleResolution = core.ModuleResolutionKindNodeNext
	}
	compilerOptions.AllowJs = core.TSTrue
	// Don't force "use strict" — let source directives pass through
	compilerOptions.AlwaysStrict = core.TSFalse

	// Set reflection mode
	reflectionMode := string(l.options.Reflection)
	if reflectionMode == "" {
		reflectionMode = "default"
	}
	runtyped.SetReflectionModeOverride(reflectionMode)
	defer runtyped.SetReflectionModeOverride("") // reset after compile

	currentDir := filepath.Dir(path)

	host := compiler.NewCompilerHost(currentDir, fs, bundled.LibPath(), nil, nil)

	config := &tsoptions.ParsedCommandLine{
		ParsedConfig: &core.ParsedOptions{
			CompilerOptions: compilerOptions,
			FileNames:       []string{path},
		},
	}

	program := compiler.NewProgram(compiler.ProgramOptions{
		Config: config,
		Host:   host,
	})

	// Find the target source file
	var targetFile *ast.SourceFile
	for _, sf := range program.GetSourceFiles() {
		if sf.FileName() == path {
			targetFile = sf
			break
		}
	}
	if targetFile == nil {
		return "", fmt.Errorf("source file not found in program: %s", path)
	}

	// Emit and capture output
	var output string
	emitResult := program.Emit(context.Background(), compiler.EmitOptions{
		TargetSourceFile: targetFile,
		WriteFile: func(fileName string, text string, _ *compiler.WriteFileData) error {
			output = text
			return nil
		},
	})
	if emitResult == nil {
		return "", fmt.Errorf("emit returned nil result")
	}

	// Collect diagnostics
	var diags []string
	for _, d := range program.GetSyntacticDiagnostics(context.Background(), targetFile) {
		diags = append(diags, string(d.MessageKey()))
	}
	for _, d := range program.GetSemanticDiagnostics(context.Background(), targetFile) {
		diags = append(diags, string(d.MessageKey()))
	}

	_ = diags // diagnostics are non-fatal for transform purposes

	return output, nil
}

// overlayFS combines a base VFS with an overlay VFS.
// Files in the base VFS take precedence; if not found, fall through to overlay.
type overlayFS struct {
	base    vfs.FS
	overlay vfs.FS
}

func (o *overlayFS) UseCaseSensitiveFileNames() bool {
	return true
}

func (o *overlayFS) FileExists(path string) bool {
	if o.base.FileExists(path) {
		return true
	}
	return o.overlay.FileExists(path)
}

func (o *overlayFS) ReadFile(path string) (string, bool) {
	if contents, ok := o.base.ReadFile(path); ok {
		return contents, true
	}
	return o.overlay.ReadFile(path)
}

func (o *overlayFS) WriteFile(path string, data string) error {
	return o.overlay.WriteFile(path, data)
}

func (o *overlayFS) AppendFile(path string, data string) error {
	return o.overlay.AppendFile(path, data)
}

func (o *overlayFS) Remove(path string) error {
	return o.overlay.Remove(path)
}

func (o *overlayFS) Chtimes(path string, aTime time.Time, mTime time.Time) error {
	return o.overlay.Chtimes(path, aTime, mTime)
}

func (o *overlayFS) DirectoryExists(path string) bool {
	if o.base.DirectoryExists(path) {
		return true
	}
	return o.overlay.DirectoryExists(path)
}

func (o *overlayFS) GetAccessibleEntries(path string) vfs.Entries {
	if o.base.DirectoryExists(path) {
		return o.base.GetAccessibleEntries(path)
	}
	return o.overlay.GetAccessibleEntries(path)
}

func (o *overlayFS) Stat(path string) vfs.FileInfo {
	if fi := o.base.Stat(path); fi != nil {
		return fi
	}
	return o.overlay.Stat(path)
}

func (o *overlayFS) WalkDir(root string, walkFn vfs.WalkDirFunc) error {
	return o.overlay.WalkDir(root, walkFn)
}

func (o *overlayFS) Realpath(path string) string {
	if o.base.FileExists(path) {
		return path
	}
	return o.overlay.Realpath(path)
}

// ensure tspath is used (for path normalization utilities)
var _ = tspath.GetNormalizedAbsolutePath

// suppress unused import warning for strings
var _ = strings.Contains
