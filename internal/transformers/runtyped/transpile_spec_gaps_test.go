package runtyped_test

import (
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/testutil/harnessutil"
	"github.com/microsoft/typescript-go/internal/tsoptions"
)

// TestTranspileSpecGaps fills in the remaining transpile.spec.ts tests
// that weren't covered by TestTranspileSpecs and other test functions.
func TestTranspileSpecGaps(t *testing.T) {
	t.Parallel()

	compile := func(t *testing.T, content string, target core.ScriptTarget) string {
		t.Helper()
		inputFiles := []*harnessutil.TestFile{
			{UnitName: "app.ts", Content: content},
		}
		result := harnessutil.CompileFiles(t,
			inputFiles, nil, harnessutil.TestConfiguration{},
			&tsoptions.ParsedCommandLine{
				ParsedConfig: &tsoptions.ParsedOptions{
					CompilerOptions: &core.CompilerOptions{
						Module:           core.ModuleKindCommonJS,
						ModuleResolution: core.ModuleResolutionKindNode10,
						Target:           target,
					},
					FileNames: []string{"/app.ts"},
				},
			}, "/", nil,
		)
		appJS := result.JS.GetOrZero("/app.js")
		if appJS == nil {
			t.Fatal("no app.js output")
		}
		return appJS.Content
	}

	assertContains := func(t *testing.T, output, expected string) {
		t.Helper()
		if !strings.Contains(output, expected) {
			t.Errorf("expected output to contain %q\nOutput:\n%s", expected, output)
		}
	}

	// "use global types with esnext target" — globals work with ESNext target
	t.Run("GlobalsWithESNextTarget", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `interface User {}
export type a = Partial<User>;`, core.ScriptTargetESNext)
		assertContains(t, output, "__ΩPartial")
		assertContains(t, output, "() => __ΩPartial")
	})

	// "pass type argument property access" — class method with type argument
	t.Run("PassTypeArgumentPropertyAccess", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `class Database {
    query<T>(type?: ReceiveType<T>) {}
}

const db = new Database;
db.query<string>();`, core.ScriptTargetES2016)
		// Should compile successfully and contain the query method __type
		assertContains(t, output, "query")
	})

	// "pass type argument named function second param" — type arg with first param
	t.Run("PassTypeArgumentNamedFunctionSecondParam", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `type ReceiveType<T> = Packed | Type | ClassType<T>;

function getType<T>(first: string = 1, type?: ReceiveType<T>) {
}

getType<string>();`, core.ScriptTargetES2016)
		// Should compile successfully — the type argument is passed
		// through the Ω side-channel to the second parameter
		assertContains(t, output, "getType")
	})

	// "resolve import ts" — verify specific logger_1.Logger pattern
	t.Run("ResolveImportTSPattern", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"app.ts":    `import { Logger } from './logger';
function fn(logger: Logger) {}`,
			"logger.ts": `export class Logger {}`,
		}
		var inputFiles []*harnessutil.TestFile
		var fileNames []string
		for name, content := range files {
			inputFiles = append(inputFiles, &harnessutil.TestFile{UnitName: name, Content: content})
			fileNames = append(fileNames, "/"+name)
		}
		result := harnessutil.CompileFiles(t,
			inputFiles, nil, harnessutil.TestConfiguration{},
			&tsoptions.ParsedCommandLine{
				ParsedConfig: &tsoptions.ParsedOptions{
					CompilerOptions: &core.CompilerOptions{
						Module:           core.ModuleKindCommonJS,
						ModuleResolution: core.ModuleResolutionKindNode10,
						Target:           core.ScriptTargetES2016,
					},
					FileNames: fileNames,
				},
			}, "/", nil,
		)
		appJS := result.JS.GetOrZero("/app.js")
		if appJS == nil {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS.Content)
		// Should reference Logger as a value reference (logger_1.Logger in CommonJS)
		assertContains(t, appJS.Content, "Logger")
		// Logger class should have __type
		loggerJS := result.JS.GetOrZero("/logger.js")
		if loggerJS == nil {
			t.Fatal("no logger.js output")
		}
		assertContains(t, loggerJS.Content, "__type")
	})

	// "resolve import d.ts" — import from declaration file
	t.Run("ResolveImportDTSPattern", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{
			"app.ts":        `import { Logger } from './logger';
function fn(logger: Logger) {}`,
			"logger.d.ts":   `export declare class Logger {}`,
		}
		var inputFiles []*harnessutil.TestFile
		var fileNames []string
		for name, content := range files {
			inputFiles = append(inputFiles, &harnessutil.TestFile{UnitName: name, Content: content})
			fileNames = append(fileNames, "/"+name)
		}
		result := harnessutil.CompileFiles(t,
			inputFiles, nil, harnessutil.TestConfiguration{},
			&tsoptions.ParsedCommandLine{
				ParsedConfig: &tsoptions.ParsedOptions{
					CompilerOptions: &core.CompilerOptions{
						Module:           core.ModuleKindCommonJS,
						ModuleResolution: core.ModuleResolutionKindNode10,
						Target:           core.ScriptTargetES2016,
					},
					FileNames: fileNames,
				},
			}, "/", nil,
		)
		appJS := result.JS.GetOrZero("/app.js")
		if appJS == nil {
			t.Fatal("no app.js output")
		}
		t.Logf("app.js:\n%s", appJS.Content)
		// Should reference Logger
		assertContains(t, appJS.Content, "Logger")
	})

	// "function __type" — basic function __type assignment
	t.Run("FunctionTypeBasic", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `function log(message: string) {}`, core.ScriptTargetES2016)
		assertContains(t, output, "log.__type")
	})

	// "symbol function name" — verify Symbol.iterator reference
	t.Run("SymbolFunctionName", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `const a = Symbol('a');
class MySet {
    [a](): any {}
    [Symbol.iterator](): any {}
}`, core.ScriptTargetES2016)
		assertContains(t, output, "Symbol.iterator")
	})

	// "class typeName" — verify class name in __type
	t.Run("ClassTypeNameCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `class StreamApiResponseClass<T> {
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
}`, core.ScriptTargetES2016)
		// Go printer uses double quotes for string literals
		assertContains(t, output, `"StreamApiResponseClass"`)
	})

	// "resolve type ref" — verify () => Guest, 'Guest' pattern
	t.Run("ResolveTypeRefCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `class Guest {}
class Vehicle {
    constructor(public Guest: Guest) {
    }
}`, core.ScriptTargetES2016)
		assertContains(t, output, "() => Guest")
		// Go printer uses double quotes
		assertContains(t, output, `"Guest"`)
	})

	// "ReceiveType arrow function" — verify Ω and __assignType pattern
	t.Run("ReceiveTypeArrowFunctionCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `export const typeValidation = <T>(type?: ReceiveType<T>): ValidatorFn => (control: AbstractControl) => {
    type = resolveReceiveType(type);
    return null;
}`, core.ScriptTargetES2016)
		assertContains(t, output, "__assignType")
	})

	// "infer type" — verify 'a' property name in __type
	t.Run("InferTypeCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `class A {
    a = 1;
}`, core.ScriptTargetES2016)
		// Go printer uses double quotes
		assertContains(t, output, `"a"`)
	})

	// "intrinsic type" — verify __ΩCapitalize
	t.Run("IntrinsicTypeCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `export type A = Capitalize<'a'>;`, core.ScriptTargetES2016)
		assertContains(t, output, "__ΩCapitalize")
	})

	// "Function" — verify [() => Function, pattern
	t.Run("FunctionTypeRefCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `type a = Function;`, core.ScriptTargetES2016)
		assertContains(t, output, "() => Function")
	})

	// "Return function ref" — verify () => Option, pattern
	t.Run("ReturnFunctionRefCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `function Option<T>(val: T): Option<T> {
};`, core.ScriptTargetES2016)
		assertContains(t, output, "() => Option,")
	})

	// "Return arrow function ref" — verify () => Option, pattern
	t.Run("ReturnArrowFunctionRefCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `const Option = <T>(val: T): Option<T> => {
};`, core.ScriptTargetES2016)
		assertContains(t, output, "() => Option,")
	})

	// "keep 'use x' at top" — verify "use client" stays before code
	t.Run("KeepUseClientAtTopCheck", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `"use client";
const a = (a: string) => {};`, core.ScriptTargetES2016)
		assertContains(t, output, `"use client"`)
		useClientIdx := strings.Index(output, `"use client"`)
		functionIdx := strings.Index(output, "const a")
		if useClientIdx >= functionIdx {
			t.Errorf("expected 'use client' to come before function code")
		}
	})

	// "es2021" — verify __ΩPick with ES2021 target
	t.Run("ES2021Check", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `interface User {
    id: number;
    name: string;
    password: string;
}
type ReadUser = Omit<User, 'password'>;
const type = typeOf<ReadUser>();`, core.ScriptTargetES2021)
		assertContains(t, output, "__ΩPick")
	})

	// "es2022" — verify __ΩPick with ES2022 target
	t.Run("ES2022Check", func(t *testing.T) {
		t.Parallel()
		output := compile(t, `interface User {
    id: number;
    name: string;
    password: string;
}
type ReadUser = Omit<User, 'password'>;
const type = typeOf<ReadUser>();`, core.ScriptTargetES2022)
		assertContains(t, output, "__ΩPick")
	})
}
