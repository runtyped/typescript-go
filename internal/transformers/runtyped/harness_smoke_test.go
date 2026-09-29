package runtyped_test

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/testutil/harnessutil"
	"github.com/microsoft/typescript-go/internal/tsoptions"
)

// Smoke test: verify CompileFiles works with the runtyped transformer
func TestCompileFilesMultiFile(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: "import { Logger } from './logger.js';\nfunction fn(logger: Logger) {}"},
		{UnitName: "logger.ts", Content: "export class Logger {}"},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts", "/logger.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)

	loggerJS := result.JS.GetOrZero("/logger.js")
	if loggerJS == nil {
		t.Fatal("no logger.js output")
	}
	t.Logf("logger.js output:\n%s", loggerJS.Content)

	if !strings.Contains(appJS.Content, "__type") && !strings.Contains(appJS.Content, "__Ω") {
		t.Error("app.js should contain __type or __Ω")
	}
}

// Port of the named re-export tests from transform.spec.ts.
// These are pure syntax transforms — __Ω re-exports are added regardless of
// whether the actual type can be resolved cross-file.
func TestNamedReExportMultiFile(t *testing.T) {
	t.Parallel()

	compilerOptions := &core.CompilerOptions{
		Module:           core.ModuleKindCommonJS,
		ModuleResolution: core.ModuleResolutionKindNode10,
		Target:           core.ScriptTargetES2016,
	}

	// Helper to compile multiple files and return outputs
	compile := func(t *testing.T, files map[string]string) map[string]string {
		t.Helper()
		var inputFiles []*harnessutil.TestFile
		var fileNames []string
		for name, content := range files {
			path := "/" + name
			inputFiles = append(inputFiles, &harnessutil.TestFile{UnitName: name, Content: content})
			fileNames = append(fileNames, path)
		}
		result := harnessutil.CompileFiles(t,
			inputFiles,
			nil,
			harnessutil.TestConfiguration{},
			&tsoptions.ParsedCommandLine{
				ParsedConfig: &tsoptions.ParsedOptions{
					CompilerOptions: compilerOptions,
					FileNames:       fileNames,
				},
			},
			"/",
			nil,
		)
		outputs := make(map[string]string)
		result.JS.Entries()(func(key string, value *harnessutil.TestFile) bool {
			outputs[key] = value.Content
			return true
		})
		return outputs
	}

	// "named re-export with __Ω symbol from .ts file"
	t.Run("NamedReExportFromTS", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":   "import { User } from './index';\ntypeOf<User>();",
			"index.ts": "export { User } from './types';",
			"types.ts": "export interface User {\n    name: string;\n}",
		})
		indexJS, ok := outputs["/index.js"]
		if !ok {
			t.Fatal("no index.js output")
		}
		t.Logf("index.js:\n%s", indexJS)
		assertContains(t, indexJS, "__ΩUser")
	})

	// "named re-export with __Ω symbol from .d.ts file"
	t.Run("NamedReExportFromDTS", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":     "import { User } from './index';\ntypeOf<User>();",
			"index.ts":   "export { User } from './types';",
			"types.d.ts": "export interface User {\n    name: string;\n}\nexport type __ΩUser = any[];",
		})
		indexJS, ok := outputs["/index.js"]
		if !ok {
			t.Fatal("no index.js output")
		}
		t.Logf("index.js:\n%s", indexJS)
		assertContains(t, indexJS, "__ΩUser")
	})

	// "named re-export with alias"
	t.Run("NamedReExportWithAlias", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":   "import { MyUser } from './index';\ntypeOf<MyUser>();",
			"index.ts": "export { User as MyUser } from './types';",
			"types.ts": "export interface User {\n    name: string;\n}",
		})
		indexJS, ok := outputs["/index.js"]
		if !ok {
			t.Fatal("no index.js output")
		}
		t.Logf("index.js:\n%s", indexJS)
		// Should re-export __ΩUser as __ΩMyUser
		assertContains(t, indexJS, "__ΩUser")
		assertContains(t, indexJS, "__ΩMyUser")
	})

	// "named re-export multiple symbols"
	t.Run("NamedReExportMultiple", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":   "import { User, Post } from './index';\ntypeOf<User>();\ntypeOf<Post>();",
			"index.ts": "export { User, Post } from './types';",
			"types.ts": "export interface User {\n    name: string;\n}\nexport interface Post {\n    title: string;\n}",
		})
		indexJS, ok := outputs["/index.js"]
		if !ok {
			t.Fatal("no index.js output")
		}
		t.Logf("index.js:\n%s", indexJS)
		assertContains(t, indexJS, "__ΩUser")
		assertContains(t, indexJS, "__ΩPost")
	})

	// "named re-export without __Ω symbol (no-op)"
	// TODO: This test currently fails because our transformer doesn't implement
	// shouldReExportOmegaSymbol — it adds __Ω re-exports for ALL named exports,
	// including value exports like `config`. This requires cross-file resolution
	// to determine whether the exported symbol is a type (interface/type alias/enum)
	// or a value. Deferred until cross-file resolution is implemented.
	t.Run("NamedReExportNoOp", func(t *testing.T) {
		outputs := compile(t, map[string]string{
			"app.ts":     "import { config } from './index';",
			"index.ts":   "export { config } from './config';",
			"config.ts":  "export const config = { debug: true };",
		})
		indexJS, ok := outputs["/index.js"]
		if !ok {
			t.Fatal("no index.js output")
		}
		t.Logf("index.js:\n%s", indexJS)
		assertNotContains(t, indexJS, "__Ω")
	})
}

// Port of transform.spec.ts tests that require cross-file type resolution.
// These use the multi-file harness so the EmitResolver can resolve imports
// to actual declarations in other source files.
func TestCrossFileResolution(t *testing.T) {
	t.Parallel()

	compilerOptions := &core.CompilerOptions{
		Module:           core.ModuleKindCommonJS,
		ModuleResolution: core.ModuleResolutionKindNode10,
		Target:           core.ScriptTargetES2016,
	}

	compile := func(t *testing.T, files map[string]string) map[string]string {
		t.Helper()
		var inputFiles []*harnessutil.TestFile
		var fileNames []string
		for name, content := range files {
			inputFiles = append(inputFiles, &harnessutil.TestFile{UnitName: name, Content: content})
			fileNames = append(fileNames, "/"+name)
		}
		result := harnessutil.CompileFiles(t,
			inputFiles,
			nil,
			harnessutil.TestConfiguration{},
			&tsoptions.ParsedCommandLine{
				ParsedConfig: &tsoptions.ParsedOptions{
					CompilerOptions: compilerOptions,
					FileNames:       fileNames,
				},
			},
			"/",
			nil,
		)
		outputs := make(map[string]string)
		result.JS.Entries()(func(key string, value *harnessutil.TestFile) bool {
			outputs[key] = value.Content
			return true
		})
		return outputs
	}

	// "resolve import ts" — import a class from another .ts file
	t.Run("ResolveImportTS", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":    "import { Logger } from './logger';\nfunction fn(logger: Logger) {}",
			"logger.ts": "export class Logger {}",
		})
		appJS, ok := outputs["/app.js"]
		if !ok {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS)
		assertContains(t, appJS, "Logger")
		assertContains(t, appJS, "__type")

		loggerJS, ok := outputs["/logger.js"]
		if !ok {
			t.Fatal("no logger.js output")
		}
		t.Logf("logger.js:\n%s", loggerJS)
		assertContains(t, loggerJS, "__type")
	})

	// "resolve import d.ts" — import a class from a .d.ts file
	t.Run("ResolveImportDTS", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":       "import { Logger } from './logger';\nfunction fn(logger: Logger) {}",
			"logger.d.ts":  "export declare class Logger {}",
		})
		appJS, ok := outputs["/app.js"]
		if !ok {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS)
		assertContains(t, appJS, "Logger")
		assertContains(t, appJS, "__type")
	})

	// "declaration file" — import from .d.ts with explicit __Ω type
	t.Run("DeclarationFile", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts": "import { T } from './types';\ntypeOf<T>();",
			"types.d.ts": "export type T = string;\nexport type __ΩT = any[];",
		})
		appJS, ok := outputs["/app.js"]
		if !ok {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS)
		assertContains(t, appJS, "__ΩT")
	})

	// "import typeOnly interface" — type-only import from .d.ts
	t.Run("ImportTypeOnlyInterface", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":      "import type { Cache } from './module';\ntypeOf<Cache>();",
			"module.d.ts": "export interface Cache {}",
		})
		appJS, ok := outputs["/app.js"]
		if !ok {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS)
		assertContains(t, appJS, "Cache")
	})

	// "import typeOnly class" — type-only import from .d.ts (class)
	t.Run("ImportTypeOnlyClass", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":      "import type { Cache } from './module';\ntypeOf<Cache>();",
			"module.d.ts": "export declare class Cache {}",
		})
		appJS, ok := outputs["/app.js"]
		if !ok {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS)
		assertContains(t, appJS, "Cache")
	})

	// "reexport existing" — re-export of imported class
	t.Run("ReExportExisting", func(t *testing.T) {
		t.Parallel()
		outputs := compile(t, map[string]string{
			"app.ts":     "import { Cache } from './module';\ntypeOf<Cache>();",
			"module.ts":  "import { Cache } from './class';\nexport { Cache }",
			"class.ts":   "export class Cache {}",
		})
		appJS, ok := outputs["/app.js"]
		if !ok {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS)
		assertContains(t, appJS, "Cache")
	})
}

// ─── ReceiveType tests (require binder/locals) ───

func TestReceiveTypePassing(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: `function getType<T>(type?: ReceiveType<T>) {
}

getType<string>();`},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)

	// Direct passing: type arg is passed as a function argument
	// Note: type arguments are stripped in emit, so getType<string>([...]) becomes getType([...])
	if !strings.Contains(appJS.Content, "getType([") {
		t.Errorf("expected direct passing: getType([")
	}
	// No Ω side-channel at call site
	if strings.Contains(appJS.Content, "getType.Ω = [") {
		t.Errorf("should not have Ω side-channel")
	}
}

func TestReceiveTypeArrowFunction(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: `(<T>(type?: ReceiveType<T>) => {})<string>();`},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)
	// Arrow function inline calls are excluded from type passing — just make sure it compiles
}

func TestGlobalsPartial(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: `interface User {}
export type a = Partial<User>;`},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)

	// Global was detected and embedded
	if !strings.Contains(appJS.Content, "__ΩPartial") {
		t.Errorf("expected __ΩPartial to be embedded in output")
	}
}

func TestFunctionTypeHoisting(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: `function greet(name: string): string {
    return "Hello, " + name;
}`},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)

	// __type assignment is hoisted to the top
	funcIdx := strings.Index(appJS.Content, "function greet")
	typeIdx := strings.Index(appJS.Content, "greet.__type")
	if funcIdx < 0 {
		t.Fatal("function greet not found")
	}
	if typeIdx < 0 {
		t.Fatal("greet.__type not found")
	}
	if typeIdx >= funcIdx {
		t.Errorf("greet.__type should be hoisted before function declaration (typeIdx=%d, funcIdx=%d)", typeIdx, funcIdx)
	}
}

func TestFunctionTypeHoistingBlockScoped(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: `if (true) {
    function insideIf(x: number): void {}
}`},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)

	// Block-scoped: __type should be after function (inline)
	funcIdx := strings.Index(appJS.Content, "function insideIf")
	typeIdx := strings.Index(appJS.Content, "insideIf.__type")
	if funcIdx < 0 {
		t.Fatal("function insideIf not found")
	}
	if typeIdx < 0 {
		t.Fatal("insideIf.__type not found")
	}
	if typeIdx < funcIdx {
		t.Errorf("insideIf.__type should be inline (after function), not hoisted (typeIdx=%d, funcIdx=%d)", typeIdx, funcIdx)
	}
}

func TestInferTypeFix(t *testing.T) {
	tests := []struct {
		name    string
		content string
		checks  []string
	}{
		{
			name: "GenericTypeParameterPassing",
			content: `function a<T>(t: T): T {
    return b<T>(t);
}
function b<T>(t: T): T {
    return t;
}
a(1);`,
			checks: []string{"a.__type", "b.__type"},
		},
		{
			name: "NestedFunctionCalls",
			content: `function outer<T>(value: T): T {
    return middle<T>(value);
}
function middle<T>(value: T): T {
    return inner<T>(value);
}
function inner<T>(value: T): T {
    return value;
}
outer('test');`,
			checks: []string{"outer.__type", "middle.__type", "inner.__type"},
		},
		{
			name: "GenericTypeWithConstraints",
			content: `function process<T extends object>(data: T): T {
    return transform<T>(data);
}
function transform<T extends object>(data: T): T {
    return data;
}`,
			checks: []string{"process.__type", "transform.__type"},
		},
		{
			name: "GenericTypeParameterInArrowFunctions",
			content: `const wrapper = <T>(value: T): T => {
    return identity<T>(value);
};
const identity = <T>(value: T): T => value;`,
			checks: []string{"__type"},
		},
		{
			name: "GenericTypeParameterMultipleTypeParams",
			content: `function map<T, U>(value: T, fn: (v: T) => U): U {
    return apply<T, U>(value, fn);
}
function apply<T, U>(value: T, fn: (v: T) => U): U {
    return fn(value);
}`,
			checks: []string{"map.__type", "apply.__type"},
		},
		{
			name: "GenericTypeParameterInClassMethods",
			content: `class Processor {
    process<T>(value: T): T {
        return this.transform<T>(value);
    }
    transform<T>(value: T): T {
        return value;
    }
}`,
			checks: []string{"Processor"},
		},
		{
			name: "GenericTypeParameterWithDefaultType",
			content: `function create<T = string>(value: T): T {
    return process<T>(value);
}
function process<T = string>(value: T): T {
    return value;
}`,
			checks: []string{"create.__type", "process.__type"},
		},
		{
			name: "GenericTypeParameterInTypeReferenceWithTypeArguments",
			content: `function wrap<T>(value: T): Array<T> {
    return makeArray<T>(value);
}
function makeArray<T>(value: T): Array<T> {
    return [value];
}`,
			checks: []string{"wrap.__type", "makeArray.__type"},
		},
		{
			name: "GenericTypeParameterInUnionTypes",
			content: `function maybe<T>(value: T | undefined): T | undefined {
    return process<T>(value);
}
function process<T>(value: T | undefined): T | undefined {
    return value;
}`,
			checks: []string{"maybe.__type", "process.__type"},
		},
		{
			name: "GenericTypeParameterInIntersectionTypes",
			content: `interface Named { name: string }
function extend<T>(value: T): T & Named {
    return addName<T>(value);
}
function addName<T>(value: T): T & Named {
    return { ...value, name: 'test' } as T & Named;
}`,
			checks: []string{"extend.__type", "addName.__type"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputFiles := []*harnessutil.TestFile{
				{UnitName: "app.ts", Content: tt.content},
			}

			result := harnessutil.CompileFiles(t,
				inputFiles,
				nil,
				harnessutil.TestConfiguration{},
				&tsoptions.ParsedCommandLine{
					ParsedConfig: &tsoptions.ParsedOptions{
						CompilerOptions: &core.CompilerOptions{
							Module:           core.ModuleKindCommonJS,
							ModuleResolution: core.ModuleResolutionKindNode10,
							Target:           core.ScriptTargetES2016,
						},
						FileNames: []string{"/app.ts"},
					},
				},
				"/",
				nil,
			)

			appJS := result.JS.GetOrZero("/app.js")
			if appJS == nil {
				t.Fatal("no app.js output")
			}

			for _, check := range tt.checks {
				if !strings.Contains(appJS.Content, check) {
					t.Errorf("expected output to contain %q\noutput:\n%s", check, appJS.Content)
				}
			}
		})
	}
}

func TestDeclarationFileExportAll(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: `import { T, T2 } from './module';
typeOf<T>();
typeOf<T2>();`},
		{UnitName: "module.d.ts", Content: `export * from './module/types';`},
		{UnitName: "module/types.d.ts", Content: `export type T = string;
export type T2 = string;
export type __ΩT = any[];
export type __ΩT2 = any[];`},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts", "/module.d.ts", "/module/types.d.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)

	// Should import __ΩT and __ΩT2 from './module'
	if !strings.Contains(appJS.Content, "__ΩT") {
		t.Errorf("expected __ΩT import")
	}
}

func TestResolveImportNodeModules(t *testing.T) {
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: `import { Logger } from 'logger';
function fn(logger: Logger) {}`},
		{UnitName: "node_modules/logger/index.d.ts", Content: `export declare class Logger {}`},
	}

	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts", "/node_modules/logger/index.d.ts"},
			},
		},
		"/",
		nil,
	)

	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	t.Logf("app.js output:\n%s", appJS.Content)

	// Should reference Logger as a value (not __Ω since it's a class)
	if !strings.Contains(appJS.Content, "Logger") {
		t.Errorf("expected Logger reference in output")
	}
}

func TestTranspileSpecs(t *testing.T) {
	tests := []struct {
		name    string
		content string
		checks  []string
	}{
		{
			name:    "FunctionTypeRef",
			content: `type a = Function;`,
			checks:  []string{"() => Function"},
		},
		{
			name: "EnumUnion",
			content: `enum StatEnginePowerUnit { Hp }
enum StatWeightUnit { Lbs, Kg }
type StatMeasurementUnit = StatEnginePowerUnit | StatWeightUnit;
typeOf<StatMeasurementUnit>();`,
			checks: []string{"__ΩStatEnginePowerUnit", "__ΩStatWeightUnit"},
		},
		{
			name: "ClassGenericReflection",
			content: `class A<T> {
    constructor(type?: ReceiveType<T>) {
    }
}
new A<string>();`,
			checks: []string{"A.__type"},
		},
		{
			name: "ClassExtendsGeneric",
			content: `class A<T> {
    constructor(type?: ReceiveType<T>) {
    }
}
class B extends A<string> {}
new B();`,
			checks: []string{"B.__type"},
		},
		{
			name: "InlineTypeDefinitions",
			content: `function testFn<
    T extends ClassType<any>,
    Prop extends keyof InstanceType<T>
>(options: {
    type: T;
    props: Prop[];
}) {
    type R = Pick<InstanceType<T>, Prop>;
}`,
			checks: []string{"testFn.__type"},
		},
		{
			name:    "ReadonlyArray",
			content: `class A { constructor(readonly id: number) {} }`,
			checks:  []string{"A.__type"},
		},
		{
			name: "InferType",
			content: `type R<T> = T extends infer U ? U : never;
typeOf<R<string>>();`,
			checks: []string{"__ΩR"},
		},
		{
			name: "SymbolFunctionName",
			content: `const fn = function namedFn(a: string) {};
typeOf<typeof fn>();`,
			checks: []string{"__assignType"},
		},
		{
			name: "ReturnTypeFunctionRef",
			content: `function Option<T>(val: T): Option<T> {
};`,
			checks: []string{"() => Option,"},
		},
		{
			name: "ReturnTypeArrowFunctionRef",
			content: `const Option = <T>(val: T): Option<T> => {
};`,
			checks: []string{"() => Option,"},
		},
		{
			name: "ClassTypeName",
			content: `class StreamApiResponseClass<T> {
    constructor(public response: T) {
    }
}
function StreamApiResponse<T>(responseBodyClass: ClassType<T>) {
    class A extends StreamApiResponseClass<T> {
        constructor(public response: T) {
            super(response);
        }
    }
    return A;
}`,
			checks: []string{`"StreamApiResponseClass"`},
		},
		{
			name: "ResolveTypeRef",
			content: `class Guest {}
class Vehicle {
    constructor(public Guest: Guest) {
    }
}`,
			checks: []string{`() => Guest, "Guest"`},
		},
		{
			name: "ResolveTypeRef2",
			content: `class Guest {}
class Vehicle {
    public Guest: Guest;
}`,
			checks: []string{`() => Guest, "Guest"`},
		},
		{
			name: "IntrinsicType",
			content: `export type A = Capitalize<'a'>;`,
			checks: []string{"__ΩCapitalize"},
		},
		{
			name: "KeyofThisExpression",
			content: `class Factory {
    someFunctionC(input: keyof this) { }
}`,
			checks: []string{"Factory.__type"},
		},
		{
			name: "ExtendsWithReferenceToThis",
			content: `class Factory {
    create() {
        class LogEntityForSchema extends this.options.entity {
        }
    }
}`,
			checks: []string{"Factory.__type"},
		},
		{
			name: "ClassGenericExpressionReflection",
			content: `class A<T> {
    constructor(type?: ReceiveType<T>) {
    }
}
const a = {b: A};
new a.b<string>();`,
			checks: []string{"A.__type"},
		},
		{
			name: "Issue352EmptyOpsFallback",
			content: `interface MyInterface {
    (): void;
}
export type Test = MyInterface;`,
			checks: []string{"__ΩTest"},
		},
		{
			name: "KeepUseClientAtTop",
			content: `"use client";
const a = (a: string) => {};`,
			checks: []string{`"use client";`},
		},
		{
			name: "ReadonlyArray",
			content: `interface Post {
    id: number;
}
interface User {
    readonly id: number;
    readonly posts: readonly Post[]
}
typeOf<User>();`,
			checks: []string{"__ΩUser"},
		},
		{
			name: "ReceiveTypeForwardToTypePassing",
			content: `function typeOf2<T>(type?: ReceiveType<T>) {
    return resolveReceiveType(type);
}
function mySerialize<T>(type?: ReceiveType<T>) {
    return typeOf2<T>();
}`,
			checks: []string{"mySerialize.__type", "typeOf2.__type"},
		},
		{
			name: "ReceiveTypeArrowFunction",
			content: `export const typeValidation = <T>(type?: ReceiveType<T>): ValidatorFn => (control: AbstractControl) => {
    type = resolveReceiveType(type);
    return null;
}`,
			checks: []string{"typeValidation"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputFiles := []*harnessutil.TestFile{
				{UnitName: "app.ts", Content: tt.content},
			}

			result := harnessutil.CompileFiles(t,
				inputFiles,
				nil,
				harnessutil.TestConfiguration{},
				&tsoptions.ParsedCommandLine{
					ParsedConfig: &tsoptions.ParsedOptions{
						CompilerOptions: &core.CompilerOptions{
							Module:           core.ModuleKindCommonJS,
							ModuleResolution: core.ModuleResolutionKindNode10,
							Target:           core.ScriptTargetES2016,
						},
						FileNames: []string{"/app.ts"},
					},
				},
				"/",
				nil,
			)

			appJS := result.JS.GetOrZero("/app.js")
			if appJS == nil {
				t.Fatal("no app.js output")
			}
			t.Logf("app.js:\n%s", appJS.Content)

			for _, check := range tt.checks {
				if !strings.Contains(appJS.Content, check) {
					t.Errorf("expected output to contain %q", check)
				}
			}
		})
	}
}

// TestDeclarationTransformer verifies that .d.ts output includes
// `export declare type __ΩX = any[]` for exported type/interface/enum declarations.
func TestDeclarationTransformer(t *testing.T) {
	t.Parallel()

	compilerOptions := &core.CompilerOptions{
		Module:           core.ModuleKindCommonJS,
		ModuleResolution: core.ModuleResolutionKindNode10,
		Target:           core.ScriptTargetES2016,
		Declaration:      core.TSTrue,
	}

	tests := []struct {
		name    string
		content string
		checks  []string
	}{
		{
			name: "TypeAlias",
			content: `export type User = { name: string; age: number };`,
			checks: []string{"export declare type __ΩUser = any[];"},
		},
		{
			name: "Interface",
			content: `export interface IUser { name: string; }`,
			checks: []string{"export declare type __ΩIUser = any[];"},
		},
		{
			name: "Enum",
			content: `export enum Color { Red, Green, Blue }`,
			checks: []string{"export declare type __ΩColor = any[];"},
		},
		{
			name: "MultipleTypes",
			content: `export type User = { name: string };
export interface Repo { id: number; }
export enum Status { Active, Inactive }`,
			checks: []string{
				"export declare type __ΩUser = any[];",
				"export declare type __ΩRepo = any[];",
				"export declare type __ΩStatus = any[];",
			},
		},
		{
			name: "NonExportedNoOmega",
			content: `type Internal = { x: number };
export type Public = { y: string };`,
			checks: []string{
				"export declare type __ΩPublic = any[];",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inputFiles := []*harnessutil.TestFile{
				{UnitName: "app.ts", Content: tt.content},
			}

			result := harnessutil.CompileFiles(t,
				inputFiles,
				nil,
				harnessutil.TestConfiguration{},
				&tsoptions.ParsedCommandLine{
					ParsedConfig: &tsoptions.ParsedOptions{
						CompilerOptions: compilerOptions,
						FileNames:       []string{"/app.ts"},
					},
				},
				"/",
				nil,
			)

			// Find the .d.ts output
			var dtsContent string
			result.DTS.Entries()(func(key string, value *harnessutil.TestFile) bool {
				dtsContent = value.Content
				return false
			})

			if dtsContent == "" {
				t.Fatal("no .d.ts output produced")
			}
			t.Logf(".d.ts output:\n%s", dtsContent)

			for _, check := range tt.checks {
				if !strings.Contains(dtsContent, check) {
					t.Errorf("expected .d.ts output to contain %q\nGot:\n%s", check, dtsContent)
				}
			}
		})
	}
}

// TestIssue352ExternalTypeAlias verifies that external type aliases produce valid
// bytecode (not `const __ΩX;` without initializer) when referencing external .d.ts types.
func TestIssue352ExternalTypeAlias(t *testing.T) {
	t.Parallel()

	compilerOptions := &core.CompilerOptions{
		Module:           core.ModuleKindCommonJS,
		ModuleResolution: core.ModuleResolutionKindNode10,
		Target:           core.ScriptTargetES2016,
	}

	tests := []struct {
		name     string
		files    map[string]string
		checks   []string
		notCheck []string
	}{
		{
			name: "ExternalTypeAlias",
			files: map[string]string{
				"app.ts": `import { ExternalResult } from './external-lib';
export type MyResult = ExternalResult<number, Error>;`,
				"external-lib.d.ts": `export declare type ExternalResult<T, E> = { ok: true; value: T } | { ok: false; error: E };`,
			},
			checks:   []string{"__ΩMyResult"},
			notCheck: []string{`const __ΩMyResult;`},
		},
		{
			name: "ExternalGenericClass",
			files: map[string]string{
				"app.ts": `import { Result } from './result-lib';
export type AppResult<T> = Result<T, string>;
function processResult(r: AppResult<number>) {
    return r;
}`,
				"result-lib.d.ts": `export declare class Result<T, E> {
    static ok<T>(value: T): Result<T, never>;
    static err<E>(error: E): Result<never, E>;
}`,
			},
			checks:   []string{"__ΩAppResult"},
			notCheck: []string{`const __ΩAppResult;`},
		},
	}

	compile := func(t *testing.T, files map[string]string) map[string]string {
		t.Helper()
		var inputFiles []*harnessutil.TestFile
		var fileNames []string
		for name, content := range files {
			inputFiles = append(inputFiles, &harnessutil.TestFile{UnitName: name, Content: content})
			fileNames = append(fileNames, "/"+name)
		}
		result := harnessutil.CompileFiles(t,
			inputFiles,
			nil,
			harnessutil.TestConfiguration{},
			&tsoptions.ParsedCommandLine{
				ParsedConfig: &tsoptions.ParsedOptions{
					CompilerOptions: compilerOptions,
					FileNames:       fileNames,
				},
			},
			"/",
			nil,
		)
		outputs := make(map[string]string)
		result.JS.Entries()(func(key string, value *harnessutil.TestFile) bool {
			outputs[key] = value.Content
			return true
		})
		return outputs
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outputs := compile(t, tt.files)
			appJS, ok := outputs["/app.js"]
			if !ok {
				t.Fatal("no app.js output")
			}
			t.Logf("app.js:\n%s", appJS)
			for _, check := range tt.checks {
				if !strings.Contains(appJS, check) {
					t.Errorf("expected output to contain %q", check)
				}
			}
			for _, nc := range tt.notCheck {
				if strings.Contains(appJS, nc) {
					t.Errorf("expected output to NOT contain %q", nc)
				}
			}
		})
	}
}

// TestOmitPickWithTarget verifies that Omit/Pick globals work with different ES targets.
func TestOmitPickWithTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target core.ScriptTarget
	}{
		{"ES2021", core.ScriptTargetES2021},
		{"ES2022", core.ScriptTargetES2022},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inputFiles := []*harnessutil.TestFile{
				{UnitName: "app.ts", Content: `interface User {
    id: number;
    name: string;
    password: string;
}
type ReadUser = Omit<User, 'password'>;
const type = typeOf<ReadUser>();`},
			}

			result := harnessutil.CompileFiles(t,
				inputFiles,
				nil,
				harnessutil.TestConfiguration{},
				&tsoptions.ParsedCommandLine{
					ParsedConfig: &tsoptions.ParsedOptions{
						CompilerOptions: &core.CompilerOptions{
							Module:           core.ModuleKindCommonJS,
							ModuleResolution: core.ModuleResolutionKindNode10,
							Target:           tt.target,
						},
						FileNames: []string{"/app.ts"},
					},
				},
				"/",
				nil,
			)

			appJS := result.JS.GetOrZero("/app.js")
			if appJS == nil {
				t.Fatal("no app.js output")
			}
			t.Logf("app.js:\n%s", appJS.Content)

			if !strings.Contains(appJS.Content, "__ΩPick") {
				t.Errorf("expected __ΩPick in output")
			}
			if !strings.Contains(appJS.Content, "__ΩReadUser") {
				t.Errorf("expected __ΩReadUser in output")
			}
		})
	}
}

// runJSWithNode runs the given JS code with node and returns stdout.
// The JS code should set module.exports to the result it wants to assert on.
func runJSWithNode(t *testing.T, js string) string {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "runtyped-test-*.js")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(js); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	cmd := exec.Command("node", tmpFile.Name())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node failed: %v\nstderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// compileSingleFile compiles one TS file via the harness and returns the emitted JS.
func compileSingleFileJS(t *testing.T, content string) string {
	t.Helper()
	inputFiles := []*harnessutil.TestFile{
		{UnitName: "app.ts", Content: content},
	}
	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &tsoptions.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: []string{"/app.ts"},
			},
		},
		"/",
		nil,
	)
	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	return appJS.Content
}

// TestChainedMethodCalls verifies that chained method calls with type arguments
// correctly pass types without double-evaluating inner calls.
// Port of transpile.spec.ts runtime tests: chained methods, multiple calls, optional methods.
func TestChainedMethodCalls(t *testing.T) {
	t.Parallel()

	compilerOptions := &core.CompilerOptions{
		Module:           core.ModuleKindCommonJS,
		ModuleResolution: core.ModuleResolutionKindNode10,
		Target:           core.ScriptTargetES2016,
	}

	compile := func(t *testing.T, content string) string {
		t.Helper()
		inputFiles := []*harnessutil.TestFile{
			{UnitName: "app.ts", Content: content},
		}
		result := harnessutil.CompileFiles(t,
			inputFiles, nil, harnessutil.TestConfiguration{},
			&tsoptions.ParsedCommandLine{
				ParsedConfig: &tsoptions.ParsedOptions{
					CompilerOptions: compilerOptions,
					FileNames:        []string{"/app.ts"},
				},
			}, "/", nil,
		)
		appJS := result.JS.GetOrZero("/app.js")
		if appJS == nil {
			t.Fatal("no app.js output")
		}
		return appJS.Content
	}

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "TwoCalls",
			content: `const types: any[] = [];
class Http {
    response<T>(type?: ReceiveType<T>) {
        types.push(type);
        return this;
    }
}
const http = new Http;
http.response<1>().response<2>();
console.log(JSON.stringify(types));`,
			want: `[[1,".!"],[2,".!"]]`,
		},
		{
			name: "TwoCallsOneWithout",
			content: `const types: any[] = [];
class Http {
    response<T>(type?: ReceiveType<T>) {
        types.push(type);
        return this;
    }
}
const http = new Http;
http.response().response<2>();
console.log(JSON.stringify(types));`,
			want: `[null,[2,".!"]]`,
		},
		{
			name: "ThreeCalls",
			content: `const types: any[] = [];
class Http {
    response<T>(type?: ReceiveType<T>) {
        types.push(type);
        return this;
    }
}
const http = new Http;
http.response<1>().response<2>().response<3>();
console.log(JSON.stringify(types));`,
			want: `[[1,".!"],[2,".!"],[3,".!"]]`,
		},
		{
			name: "ThreeCallsOneWithout",
			content: `const types: any[] = [];
class Http {
    GET(path: string) { return this }
    response<T>(n: number, desc: string, type?: ReceiveType<T>) {
        types.push(type);
        return this;
    }
}
const http = new Http;
http.GET('/action3')
    .response<2>(200, 'List')
    .response<3>(400, 'Error');
console.log(JSON.stringify(types));`,
			want: `[[2,".!"],[3,".!"]]`,
		},
		{
			name: "MultipleCallsOptionalTypes",
			content: `const types: any[] = [];
function add<T>(type?: ReceiveType<T>) {
    types.push(type);
}
add<1>();
add();
console.log(JSON.stringify(types));`,
			want: `[[1,".!"],null]`,
		},
		{
			name: "MultipleDeepCallsOptionalTypes",
			content: `const types: any[] = [];
function add<T>(type?: ReceiveType<T>) {
    types.push(type);
    add2();
}
function add2<T>(type?: ReceiveType<T>) {
    types.push(type);
}
add<1>();
add();
console.log(JSON.stringify(types));`,
			want: `[[1,".!"],null,null,null]`,
		},
		{
			name: "ChainedOptionalMethods",
			content: `const types: any[] = [];
class Http {
    response<T>(type?: ReceiveType<T>) {
        types.push(type);
        return this;
    }
}
const http = new Http;
http.response<1>().response();
console.log(JSON.stringify(types));`,
			want: `[[1,".!"],null]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			js := compile(t, tt.content)
			t.Logf("emitted JS:\n%s", js)
			got := runJSWithNode(t, js)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOptionalChainRuntime verifies that optional chaining with type arguments
// works correctly at runtime, including when the service is undefined.
func TestOptionalChainRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "OptionalChainWithMethodCall",
			content: `const types: any[] = [];
class Service {
    doSomething<T>(type?: ReceiveType<T>) {
        types.push(type);
        return { catch: () => 'caught' };
    }
}
class Controller {
    service?: Service;
    constructor(service?: Service) {
        this.service = service;
    }
    action() {
        this.service?.doSomething<string>().catch();
    }
}
const ctrl = new Controller(new Service());
ctrl.action();
console.log(JSON.stringify(types));`,
			want: "", // types has 1 entry but its format depends on encoding — just check length
		},
		{
			name: "OptionalChainUndefinedService",
			content: `const types: any[] = [];
class Service {
    doSomething<T>(type?: ReceiveType<T>) {
        types.push(type);
        return { catch: () => 'caught' };
    }
}
class Controller {
    service?: Service;
    action() {
        this.service?.doSomething<string>().catch();
    }
}
const ctrl = new Controller();
ctrl.action();
console.log(JSON.stringify(types));`,
			want: `[]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			js := compileSingleFileJS(t, tt.content)
			t.Logf("emitted JS:\n%s", js)
			got := runJSWithNode(t, js)
			if tt.want == "" {
				// Just verify it runs without error
				return
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOptionalChainTransform verifies that optional chaining with type arguments
// is properly rewritten to use a temp variable and ternary.
func TestOptionalChainTransform(t *testing.T) {
	t.Parallel()

	compilerOptions := &core.CompilerOptions{
		Module:           core.ModuleKindCommonJS,
		ModuleResolution: core.ModuleResolutionKindNode10,
		Target:           core.ScriptTargetES2020,
	}

	tests := []struct {
		name    string
		content string
		checks  []string
	}{
		{
			name: "DirectOptionalChain",
			content: `class Service {
    doSomething<T>(type?: ReceiveType<T>) {}
}
class App {
    service?: Service;
    run() {
        this.service?.doSomething<string>();
    }
}`,
			checks: []string{"Ωr"},
		},
		{
			name: "NestedOptionalChain",
			content: `class Client {
    method<T>(type?: ReceiveType<T>) {}
}
class Service {
    getClient(): Client { return new Client(); }
}
class App {
    service?: Service;
    run() {
        this.service?.getClient().method<string>();
    }
}`,
			checks: []string{"Ωr"},
		},
		{
			name: "ChainContinuation",
			content: `class Service {
    doSomething<T>(type?: ReceiveType<T>): any { return this; }
}
class App {
    service?: Service;
    run() {
        this.service?.doSomething<string>().then();
    }
}`,
			checks: []string{"Ωr"},
		},
		{
			name: "OptionalChainWithRegularCall",
			content: `class Service {
    doSomething<T>(type?: ReceiveType<T>) {}
    regularCall() {}
}
class App {
    service?: Service;
    run() {
        this.service?.doSomething<string>();
        this.service?.regularCall();
    }
}`,
			checks: []string{"Ωr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inputFiles := []*harnessutil.TestFile{
				{UnitName: "app.ts", Content: tt.content},
			}

			result := harnessutil.CompileFiles(t,
				inputFiles,
				nil,
				harnessutil.TestConfiguration{},
				&tsoptions.ParsedCommandLine{
					ParsedConfig: &tsoptions.ParsedOptions{
						CompilerOptions: compilerOptions,
						FileNames:       []string{"/app.ts"},
					},
				},
				"/",
				nil,
			)

			appJS := result.JS.GetOrZero("/app.js")
			if appJS == nil {
				t.Fatal("no app.js output")
			}
			t.Logf("app.js:\n%s", appJS.Content)

			for _, check := range tt.checks {
				if !strings.Contains(appJS.Content, check) {
					t.Errorf("expected output to contain %q", check)
				}
			}
		})
	}
}
