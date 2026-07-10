package runtyped_test

import (
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
			ParsedConfig: &core.ParsedOptions{
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
				ParsedConfig: &core.ParsedOptions{
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
				ParsedConfig: &core.ParsedOptions{
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
			ParsedConfig: &core.ParsedOptions{
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
			ParsedConfig: &core.ParsedOptions{
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
			ParsedConfig: &core.ParsedOptions{
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
			ParsedConfig: &core.ParsedOptions{
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
			ParsedConfig: &core.ParsedOptions{
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
					ParsedConfig: &core.ParsedOptions{
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
			ParsedConfig: &core.ParsedOptions{
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
			ParsedConfig: &core.ParsedOptions{
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
					ParsedConfig: &core.ParsedOptions{
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
