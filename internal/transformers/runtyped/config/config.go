// Package config implements tsconfig.json-based reflection configuration resolution.
// It is a port of @runtyped/type-compiler's config.ts and resolver.ts,
// implementing patternMatch, reflectionModeMatcher, and getConfigResolver.
package config

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs"
)

// Mode represents the reflection mode for a file.
type Mode string

const (
	ModeDefault  Mode = "default"
	ModeExplicit Mode = "explicit"
	ModeNever    Mode = "never"
)

// DefaultExcluded are the default exclude patterns for lib files.
var DefaultExcluded = []string{
	"lib.dom*.d.ts",
	"*typedarrays.d.ts",
	"lib.webworker*.d.ts",
	"lib.decorator*.d.ts",
	"lib.es2015.proxy.d.ts",
	"lib.es2020.sharedmemory.d.ts",
	"lib.es2015.core.d.ts",
}

// RawMode is the raw reflection value from tsconfig (bool, string, or string[]).
type RawMode = any

// TsConfigJson represents the runtyped-specific fields in a tsconfig.json.
type TsConfigJson struct {
	Extends                any `json:"extends"`
	CompilerOptions        any `json:"compilerOptions"`
	Reflection             RawMode
	DeepkitCompilerOptions *struct {
		Reflection    RawMode  `json:"reflection"`
		MergeStrategy string   `json:"mergeStrategy"`
		Exclude       []string `json:"exclude"`
	} `json:"deepkitCompilerOptions"`
}

// ResolvedConfig is the fully resolved configuration after extends processing.
type ResolvedConfig struct {
	Path            string
	CompilerOptions map[string]any
	MergeStrategy   string
	Reflection      any // string[] or Mode
	Exclude         []string
}

// MatchResult is the result of matching a file path against the config.
type MatchResult struct {
	TSConfigPath string
	Mode         Mode
}

// ConfigResolver holds the resolved config and a match function.
type ConfigResolver struct {
	Config ResolvedConfig
	Match  func(filePath string) MatchResult
}

// PatternMatch implements glob matching with ** support.
// Patterns prefixed with ! are treated as exclusions.
func PatternMatch(p string, patterns []string) bool {
	var include []string
	var exclude []string

	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") {
			exclude = append(exclude, pattern[1:])
		} else {
			include = append(include, pattern)
		}
	}

	if !globMatchAny(p, include) {
		return false
	}

	if len(exclude) > 0 && globMatchAny(p, exclude) {
		return false
	}

	return true
}

// globMatchAny checks if the path matches any of the patterns.
func globMatchAny(p string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if globMatch(p, pattern) {
			return true
		}
	}
	return false
}

// globMatch implements micromatch-like glob matching with ** support.
func globMatch(s, pattern string) bool {
	s = strings.ReplaceAll(s, "\\", "/")
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	return globMatchSegments(s, pattern)
}

// globMatchSegments matches a path against a pattern segment by segment.
// ** matches zero or more path segments.
// * matches within a single segment (not /).
func globMatchSegments(s, pattern string) bool {
	sParts := strings.Split(s, "/")
	pParts := strings.Split(pattern, "/")
	return matchSegments(sParts, pParts)
}

func matchSegments(sParts, pParts []string) bool {
	si, pi := 0, 0
	starPI, starSI := -1, -1

	for si < len(sParts) {
		if pi < len(pParts) {
			if pParts[pi] == "**" {
				starPI = pi
				starSI = si
				pi++
				continue
			}
			if segmentMatch(sParts[si], pParts[pi]) {
				si++
				pi++
				continue
			}
		}
		if starPI != -1 {
			pi = starPI + 1
			starSI++
			si = starSI
			continue
		}
		return false
	}

	for pi < len(pParts) && pParts[pi] == "**" {
		pi++
	}

	return pi == len(pParts)
}

// segmentMatch matches a single path segment against a pattern segment.
// Supports * (matches anything except /) and ? (matches single char).
func segmentMatch(s, pattern string) bool {
	if pattern == "*" {
		return true
	}

	si, pi := 0, 0
	starI, starJ := -1, -1

	for si < len(s) {
		if pi < len(pattern) {
			if pattern[pi] == '*' {
				starI = pi
				starJ = si
				pi++
				continue
			}
			if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]) {
				si++
				pi++
				continue
			}
		}
		if starI != -1 {
			pi = starI + 1
			starJ++
			si = starJ
			continue
		}
		return false
	}

	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}

	return pi == len(pattern)
}

// ReflectionConfig holds the reflection-related config values.
type ReflectionConfig struct {
	Exclude    []string
	Reflection any // string[] or Mode
}

// ReflectionModeMatcher determines the reflection mode for a file based on config.
func ReflectionModeMatcher(config ReflectionConfig, filePath string) Mode {
	if len(config.Exclude) > 0 {
		if PatternMatch(filePath, config.Exclude) {
			return ModeNever
		}
	}
	if arr, ok := config.Reflection.([]string); ok {
		if PatternMatch(filePath, arr) {
			return ModeDefault
		}
		return ModeNever
	}
	if mode, ok := config.Reflection.(Mode); ok {
		if mode == ModeDefault || mode == ModeExplicit {
			return mode
		}
	}
	return ModeNever
}

// ParseRawMode converts a raw mode value (bool, string, string[]) to normalized form.
func ParseRawMode(mode RawMode) any {
	if b, ok := mode.(bool); ok {
		if b {
			return ModeDefault
		}
		return ModeNever
	}
	if s, ok := mode.(string); ok {
		if s == "default" || s == "explicit" {
			return Mode(s)
		}
		if s == "never" {
			return ModeNever
		}
		if s == "" {
			return []string{}
		}
		return []string{s}
	}
	if arr, ok := mode.([]string); ok {
		return arr
	}
	if arr, ok := mode.([]any); ok {
		result := make([]string, 0, len(arr))
		for _, v := range arr {
			result = append(result, toString(v))
		}
		return result
	}
	return []string{}
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}

func ensureStringArray(value any) []string {
	if arr, ok := value.([]any); ok {
		result := make([]string, 0, len(arr))
		for _, v := range arr {
			result = append(result, toString(v))
		}
		return result
	}
	if arr, ok := value.([]string); ok {
		return arr
	}
	if s, ok := value.(string); ok {
		return []string{s}
	}
	return []string{}
}

// normalizeExtends converts extends to an array for consistent handling.
func normalizeExtends(extends_ any) []string {
	if extends_ == nil {
		return []string{}
	}
	if s, ok := extends_.(string); ok {
		return []string{s}
	}
	if arr, ok := extends_.([]any); ok {
		result := make([]string, 0, len(arr))
		for _, v := range arr {
			result = append(result, toString(v))
		}
		return result
	}
	return []string{}
}

// resolvePaths makes relative paths absolute based on baseDir.
func resolvePaths(baseDir string, paths any) {
	arr, ok := paths.([]string)
	if !ok {
		return
	}

	for i := 0; i < len(arr); i++ {
		p := arr[i]
		if filepath.IsAbs(p) {
			continue
		}
		exclude := false
		if strings.HasPrefix(p, "!") {
			exclude = true
			p = p[1:]
		}
		if strings.HasPrefix(p, "./") || strings.Contains(p, "/") {
			p = filepath.Join(baseDir, p)
		}
		p = strings.ReplaceAll(p, "\\", "/")
		if exclude {
			p = "!" + p
		}
		arr[i] = p
	}
}

// appendPaths merges parent and existing arrays based on merge strategy.
func appendPaths(strategy string, parent []string, existing []string) []string {
	if strategy == "replace" {
		if existing != nil {
			return append([]string{}, existing...)
		}
		return append([]string{}, parent...)
	}
	if existing == nil {
		return append([]string{}, parent...)
	}
	return append(append([]string{}, parent...), existing...)
}

// currentConfig holds the working state during config resolution.
type currentConfig struct {
	compilerOptions map[string]any
	mergeStrategy   string
	reflection       any // Mode or []string
	exclude          []string
	extends          any
}

// applyConfigValues merges parent config values into the current config.
func applyConfigValues(existing *currentConfig, parent *TsConfigJson, baseDir string) {
	var parentReflection RawMode
	if parent.DeepkitCompilerOptions != nil {
		parentReflection = parent.DeepkitCompilerOptions.Reflection
	} else {
		parentReflection = parent.Reflection
	}

	if parent.DeepkitCompilerOptions != nil && existing.mergeStrategy == "" {
		existing.mergeStrategy = parent.DeepkitCompilerOptions.MergeStrategy
	}

	if parentReflection != nil {
		next := ParseRawMode(parentReflection)
		if existing.reflection == nil {
			existing.reflection = next
		} else if _, isMode := existing.reflection.(Mode); isMode {
			// if existing is already a Mode, nothing to inherit
		} else if nextArr, isArr := next.([]string); isArr {
			if existingArr, existingIsArr := existing.reflection.([]string); existingIsArr {
				existing.reflection = appendPaths(existing.mergeStrategy, nextArr, existingArr)
			}
		}
	}

	if parent.DeepkitCompilerOptions != nil && parent.DeepkitCompilerOptions.Exclude != nil {
		next := parent.DeepkitCompilerOptions.Exclude
		existing.exclude = appendPaths(existing.mergeStrategy, next, existing.exclude)
	}

	resolvePaths(baseDir, getReflectionPaths(existing.reflection))
	resolvePaths(baseDir, existing.exclude)

	if parent.CompilerOptions != nil {
		if len(existing.compilerOptions) == 0 {
			if opts, ok := parent.CompilerOptions.(map[string]any); ok {
				existing.compilerOptions = opts
			}
		}
	}
	existing.extends = parent.Extends
}

func getReflectionPaths(reflection any) []string {
	if arr, ok := reflection.([]string); ok {
		return arr
	}
	return nil
}

// readTsConfig reads and parses a tsconfig.json file from the VFS.
func readTsConfig(host tsoptions.ParseConfigHost, configPath string) *TsConfigJson {
	fs := host.FS()
	content, ok := fs.ReadFile(configPath)
	if !ok {
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil
	}

	config := &TsConfigJson{
		Extends:         raw["extends"],
		CompilerOptions: raw["compilerOptions"],
		Reflection:      raw["reflection"],
	}

	if dko, ok := raw["deepkitCompilerOptions"].(map[string]any); ok {
		config.DeepkitCompilerOptions = &struct {
			Reflection    RawMode  `json:"reflection"`
			MergeStrategy string   `json:"mergeStrategy"`
			Exclude       []string `json:"exclude"`
		}{
			Reflection:    dko["reflection"],
			MergeStrategy: toString(dko["mergeStrategy"]),
			Exclude:       ensureStringArray(dko["exclude"]),
		}
	}

	return config
}

// GetConfigResolver resolves the full tsconfig chain and returns a ConfigResolver.
func GetConfigResolver(
	cache map[string]*ConfigResolver,
	host tsoptions.ParseConfigHost,
	compilerOptions map[string]any,
	sourceFile struct{ FileName string },
	tsConfigPath string,
) *ConfigResolver {
	config := &currentConfig{
		compilerOptions: make(map[string]any),
	}

	if tsConfigPath == "" {
		if cf, ok := compilerOptions["configFilePath"]; ok {
			tsConfigPath = toString(cf)
		}
	}

	if tsConfigPath != "" {
		if cached, ok := cache[tsConfigPath]; ok {
			return cached
		}
		configFile := readTsConfig(host, tsConfigPath)
		if configFile != nil {
			applyConfigValues(config, configFile, path.Dir(tsConfigPath))
		}
	} else {
		if sourceFile.FileName != "" {
			baseDir := path.Dir(sourceFile.FileName)
			configPath := findConfigFile(host, baseDir)
			if configPath != "" {
				tsConfigPath = configPath
				if cached, ok := cache[tsConfigPath]; ok {
					return cached
				}
				configFile := readTsConfig(host, tsConfigPath)
				if configFile != nil {
					applyConfigValues(config, configFile, path.Dir(tsConfigPath))
				}
			}
		}
	}

	// Follow extends chain
	if tsConfigPath != "" {
		basePath := path.Dir(tsConfigPath)
		seen := map[string]bool{tsConfigPath: true}

		extendsQueue := normalizeExtends(config.extends)

		for len(extendsQueue) > 0 {
			extendPath := extendsQueue[0]
			extendsQueue = extendsQueue[1:]

			var resolvedPath string
			if filepath.IsAbs(extendPath) {
				resolvedPath = extendPath
			} else {
				resolvedPath = filepath.Join(basePath, extendPath)
			}

			if seen[resolvedPath] {
				continue
			}
			seen[resolvedPath] = true

			nextConfig := readTsConfig(host, resolvedPath)
			if nextConfig == nil {
				continue
			}

			nextBasePath := path.Dir(resolvedPath)
			applyConfigValues(config, nextConfig, nextBasePath)

			additionalExtends := normalizeExtends(nextConfig.Extends)
			for i := len(additionalExtends) - 1; i >= 0; i-- {
				e := additionalExtends[i]
				var additionalPath string
				if filepath.IsAbs(e) {
					additionalPath = e
				} else {
					additionalPath = filepath.Join(nextBasePath, e)
				}
				extendsQueue = append([]string{additionalPath}, extendsQueue...)
			}
		}
	}

	// Prepend default excludes
	if config.exclude != nil {
		config.exclude = append(append([]string{}, DefaultExcluded...), config.exclude...)
	} else {
		config.exclude = append([]string{}, DefaultExcluded...)
	}

	config.compilerOptions["configFilePath"] = tsConfigPath

	// Merge in provided compilerOptions
	for k, v := range compilerOptions {
		if k == "configFilePath" {
			continue
		}
		if _, exists := config.compilerOptions[k]; !exists {
			config.compilerOptions[k] = v
		}
	}

	resolvedConfig := ResolvedConfig{
		Path:            tsConfigPath,
		CompilerOptions: config.compilerOptions,
		MergeStrategy:   config.mergeStrategy,
		Reflection:      config.reflection,
		Exclude:         config.exclude,
	}

	if resolvedConfig.MergeStrategy == "" {
		resolvedConfig.MergeStrategy = "merge"
	}

	reflConfig := ReflectionConfig{
		Exclude:    resolvedConfig.Exclude,
		Reflection: resolvedConfig.Reflection,
	}

	resolver := &ConfigResolver{
		Config: resolvedConfig,
		Match: func(filePath string) MatchResult {
			mode := ReflectionModeMatcher(reflConfig, filePath)
			return MatchResult{TSConfigPath: tsConfigPath, Mode: mode}
		},
	}

	cache[tsConfigPath] = resolver
	return resolver
}

// findConfigFile searches for tsconfig.json starting from baseDir upward.
func findConfigFile(host tsoptions.ParseConfigHost, baseDir string) string {
	for {
		candidate := filepath.Join(baseDir, "tsconfig.json")
		if host.FS().FileExists(candidate) {
			return candidate
		}
		parent := path.Dir(baseDir)
		if parent == baseDir {
			break
		}
		baseDir = parent
	}
	return ""
}

// Suppress unused import warning
var _ vfs.FS
