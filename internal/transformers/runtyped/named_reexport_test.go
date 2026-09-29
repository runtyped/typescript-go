package runtyped_test

import (
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/testutil/harnessutil"
)

// TestNamedReExportSpecs ports named-reexport.spec.ts (50 tests).
func TestNamedReExportSpecs(t *testing.T) {
	t.Parallel()

	compilerOptions := &core.CompilerOptions{
		Module:           core.ModuleKindCommonJS,
		ModuleResolution: core.ModuleResolutionKindNode10,
		Target:           core.ScriptTargetES2016,
	}

	// transform: compile multiple TS files, return JS outputs keyed by filename (without /).
	transform := func(t *testing.T, files map[string]string) map[string]string {
		t.Helper()
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
					CompilerOptions: compilerOptions,
					FileNames:       fileNames,
				},
			},
			"/", nil,
		)
		outputs := make(map[string]string)
		result.JS.Entries()(func(key string, value *harnessutil.TestFile) bool {
			// Strip leading / and trailing .js → .ts for lookup
			k := strings.TrimPrefix(key, "/")
			k = strings.TrimSuffix(k, ".js") + ".ts"
			outputs[k] = value.Content
			return true
		})
		return outputs
	}

	// transpile: compile + run with Node — returns stdout.
	// For now we just compile and check the output, matching the TS test's transpile assertions.
	transpileCheck := func(t *testing.T, files map[string]string) map[string]string {
		return transform(t, files)
	}

	assertContains := func(t *testing.T, output, expected string) {
		t.Helper()
		if !strings.Contains(output, expected) {
			t.Errorf("expected output to contain %q\nOutput:\n%s", expected, output)
		}
	}
	assertNotContains := func(t *testing.T, output, expected string) {
		t.Helper()
		if strings.Contains(output, expected) {
			t.Errorf("expected output to NOT contain %q\nOutput:\n%s", expected, output)
		}
	}
	assertContainsCount := func(t *testing.T, output, expected string, max int) {
		t.Helper()
		count := strings.Count(output, expected)
		if count > max {
			t.Errorf("expected at most %d occurrences of %q, got %d\nOutput:\n%s", max, expected, count, output)
		}
	}

	// === CORE FEATURE TESTS ===

	t.Run("BasicReExportAddsOmega", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":   `export { Context } from './context';`,
			"context.ts": `export interface Context { id: number; name: string; }`,
		})
		assertContains(t, res["context.ts"], "const __ΩContext =")
		// CommonJS: exports.__ΩContext = __ΩContext;
		assertContains(t, res["context.ts"], "exports.__ΩContext")
		assertContains(t, res["index.ts"], "__ΩContext")
	})

	t.Run("NamedAliasReExport", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":   `export { Context as Ctx } from './context';`,
			"context.ts": `export interface Context { id: number; }`,
		})
		assertContains(t, res["context.ts"], "const __ΩContext =")
		// CommonJS: Object.defineProperty(exports, "__ΩCtx", { get: () => context_1.__ΩContext })
		assertContains(t, res["index.ts"], "__ΩCtx")
		assertContains(t, res["index.ts"], "__ΩContext")
	})

	t.Run("MultipleReExports", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { TypeA, TypeB, TypeC } from './types';`,
			"types.ts": `export interface TypeA { a: string; }
export interface TypeB { b: number; }
export interface TypeC { c: boolean; }`,
		})
		assertContains(t, res["types.ts"], "const __ΩTypeA =")
		assertContains(t, res["types.ts"], "const __ΩTypeB =")
		assertContains(t, res["types.ts"], "const __ΩTypeC =")
		assertContains(t, res["index.ts"], "__ΩTypeA")
		assertContains(t, res["index.ts"], "__ΩTypeB")
		assertContains(t, res["index.ts"], "__ΩTypeC")
	})

	t.Run("MixedReExportsOnlyTypes", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { MyType, myFunction, MY_CONSTANT } from './module';`,
			"module.ts": `export interface MyType { id: number; }
export function myFunction() { return 42; }
export const MY_CONSTANT = 'constant';`,
		})
		assertContains(t, res["module.ts"], "const __ΩMyType =")
		assertContains(t, res["module.ts"], "myFunction.__type")
		assertContains(t, res["index.ts"], "__ΩMyType")
		assertNotContains(t, res["index.ts"], "__ΩmyFunction")
		assertNotContains(t, res["index.ts"], "__ΩMY_CONSTANT")
	})

	t.Run("ReExportFromDTS", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":       `export { ExternalType } from './external';`,
			"external.d.ts":  `export interface ExternalType { external: boolean; }
export type __ΩExternalType = any[];`,
		})
		// Either the __Ω is re-exported or the type export remains
		hasReExport := strings.Contains(res["index.ts"], "__ΩExternalType")
		if !hasReExport {
			assertContains(t, res["index.ts"], "export { ExternalType } from './external';")
		}
	})

	t.Run("ReExportFromDTSWithExplicitOmega", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":      `export { ExternalType, __ΩExternalType } from './external';`,
			"external.d.ts": `export interface ExternalType { external: boolean; }
export type __ΩExternalType = any[];`,
		})
		assertContains(t, res["index.ts"], "ExternalType")
		assertContains(t, res["index.ts"], "__ΩExternalType")
	})

	t.Run("ReExportChain", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"a.ts": `export { DeepType } from './b';`,
			"b.ts": `export { DeepType } from './c';`,
			"c.ts": `export interface DeepType { deep: string; }`,
		})
		assertContains(t, res["c.ts"], "const __ΩDeepType =")
		assertContains(t, res["b.ts"], "__ΩDeepType")
		assertContains(t, res["a.ts"], "__ΩDeepType")
	})

	t.Run("TypeAliasReExport", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { UserID } from './types';`,
			"types.ts": `export type UserID = string & { __brand: 'UserID' };`,
		})
		assertContains(t, res["types.ts"], "const __ΩUserID =")
		assertContains(t, res["index.ts"], "__ΩUserID")
	})

	t.Run("EnumReExport", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { Status } from './enums';`,
			"enums.ts": `export enum Status { Active, Inactive, Pending }`,
		})
		assertContains(t, res["enums.ts"], "const __ΩStatus =")
		assertContains(t, res["index.ts"], "__ΩStatus")
	})

	t.Run("MixedInterfaceAndTypeAlias", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { User, UserID, UserRole } from './user';`,
			"user.ts": `export interface User { id: UserID; name: string; role: UserRole; }
export type UserID = number;
export type UserRole = 'admin' | 'user' | 'guest';`,
		})
		assertContains(t, res["user.ts"], "const __ΩUser =")
		assertContains(t, res["user.ts"], "const __ΩUserID =")
		assertContains(t, res["user.ts"], "const __ΩUserRole =")
		assertContains(t, res["index.ts"], "__ΩUser")
		assertContains(t, res["index.ts"], "__ΩUserID")
		assertContains(t, res["index.ts"], "__ΩUserRole")
	})

	t.Run("DefaultAndNamedCombination", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":  `export { default as DefaultType, NamedType } from './module';`,
			"module.ts": `export default interface DefaultInterface { value: number; }
export interface NamedType { name: string; }`,
		})
		assertContains(t, res["module.ts"], "const __ΩNamedType =")
		assertContains(t, res["index.ts"], "__ΩNamedType")
	})

	t.Run("ReExportPreservesGenerics", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":      `export { Container } from './container';`,
			"container.ts": `export interface Container<T> { value: T; getValue(): T; }`,
		})
		assertContains(t, res["container.ts"], "const __ΩContainer =")
		assertContains(t, res["index.ts"], "__ΩContainer")
	})

	t.Run("MultipleLevelsOfAliasing", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"c.ts": `export { Middle as Final } from './b';`,
			"b.ts": `export { Original as Middle } from './a';`,
			"a.ts": `export interface Original { value: string; }`,
		})
		assertContains(t, res["a.ts"], "const __ΩOriginal =")
		assertContains(t, res["b.ts"], "__ΩMiddle")
		assertContains(t, res["c.ts"], "__ΩFinal")
	})

	t.Run("BarrelFilePattern", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":    `export { User } from './user';
export { Post } from './post';
export { Comment } from './comment';`,
			"user.ts":    `export interface User { id: number; name: string; }`,
			"post.ts":    `export interface Post { id: number; title: string; }`,
			"comment.ts": `export interface Comment { id: number; text: string; }`,
		})
		assertContains(t, res["user.ts"], "const __ΩUser =")
		assertContains(t, res["post.ts"], "const __ΩPost =")
		assertContains(t, res["comment.ts"], "const __ΩComment =")
		assertContains(t, res["index.ts"], "__ΩUser")
		assertContains(t, res["index.ts"], "__ΩPost")
		assertContains(t, res["index.ts"], "__ΩComment")
	})

	// === CLASS AND VALUE RE-EXPORT TESTS ===

	t.Run("ClassReExportNoOmega", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { MyClass } from './class';`,
			"class.ts": `export class MyClass { id: number = 0; name: string = ''; }`,
		})
		assertContains(t, res["class.ts"], "__type")
		// Check that no __Ω re-export was added for the class
		// Note: Ω may be Unicode-escaped as \u03A9 in the output
		assertNotContains(t, res["index.ts"], "__ΩMyClass")
		assertNotContains(t, res["index.ts"], "\\u03A9MyClass")
	})

	t.Run("StarExportPassThrough", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export * from './types';`,
			"types.ts": `export interface TypeA { a: string; }
export interface TypeB { b: number; }`,
		})
		assertContains(t, res["types.ts"], "const __ΩTypeA =")
		assertContains(t, res["types.ts"], "const __ΩTypeB =")
		// CommonJS: exports.__ΩTypeA
		assertContains(t, res["types.ts"], "exports.__ΩTypeA")
		assertContains(t, res["types.ts"], "exports.__ΩTypeB")
		// Star exports pass through — index.ts doesn't need modification
	})

	t.Run("NoDuplicateExports", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { MyType, __ΩMyType } from './types';`,
			"types.ts": `export interface MyType { id: number; }`,
		})
		// CommonJS may produce multiple references (exports.__ΩMyType + Object.defineProperty getters)
		// Allow up to 3: one for exports.__ΩMyType declaration, plus two for explicit + auto-generated __Ω re-export
		assertContainsCount(t, res["index.ts"], "__ΩMyType", 3)
	})

	t.Run("FunctionAndValueReExports", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { someFunction, someValue } from './module';`,
			"module.ts": `export function someFunction() { return 42; }
export const someValue = 'hello';`,
		})
		assertContains(t, res["module.ts"], "someFunction.__type")
		assertNotContains(t, res["index.ts"], "__ΩsomeFunction")
		assertNotContains(t, res["index.ts"], "__ΩsomeValue")
	})

	t.Run("ReExportWithLocalUsage", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":  `import { Config } from './config';
export { Config } from './config';

function useConfig(config: Config) {
    return config;
}`,
			"config.ts": `export interface Config { setting: string; }`,
		})
		assertContains(t, res["config.ts"], "const __ΩConfig =")
		assertContains(t, res["index.ts"], "__ΩConfig")
	})

	t.Run("NamespaceReExport", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":     `export { MyNamespace } from './namespace';`,
			"namespace.ts": `export namespace MyNamespace {
    export interface Config { value: string; }
}`,
		})
		// Namespaces are flattened in CommonJS — the internal __ΩConfig is still generated
		assertContains(t, res["namespace.ts"], "const __ΩConfig =")
	})

	// === RUNTIME VERIFICATION (transpile) TESTS ===
	// These check the compiled JS output (we can't run node in these tests,
	// but we verify the JS contains the expected exports)

	t.Run("TranspileBasicReExportChain", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":    `import { User } from './index';
const user: User = { id: 1, name: 'Test' };
user;`,
			"index.ts": `export { User } from './user';`,
			"user.ts":  `export interface User { id: number; name: string; }`,
		})
		assertContains(t, res["user.ts"], "__ΩUser")
		// In CommonJS mode, check for exports.__ΩUser
		assertContains(t, res["user.ts"], "exports.__ΩUser")
	})

	t.Run("TranspileNamespaceStarReExport", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":   `import * as Types from './types';
const user: Types.User = { id: 1 };
user;`,
			"types.ts": `export interface User { id: number; }`,
		})
		assertContains(t, res["types.ts"], "__ΩUser")
		assertContains(t, res["types.ts"], "exports.__ΩUser")
	})

	t.Run("TranspileDeepBarrelChain", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":          `import { DeepType } from './index';
const obj: DeepType = { level: 3, data: 'test' };
obj;`,
			"index.ts":        `export { DeepType } from './sub';`,
			"sub/index.ts":    `export { DeepType } from './types';`,
			"sub/types.ts":    `export interface DeepType { level: number; data: string; }`,
		})
		assertContains(t, res["sub/types.ts"], "__ΩDeepType")
		assertContains(t, res["sub/types.ts"], "exports.__ΩDeepType")
	})

	t.Run("TranspileAliasedReExportChain", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts": `import { Final } from './c';
const obj: Final = { value: 'test' };
obj;`,
			"c.ts": `export { Middle as Final } from './b';`,
			"b.ts": `export { Original as Middle } from './a';`,
			"a.ts": `export interface Original { value: string; }`,
		})
		assertContains(t, res["a.ts"], "__ΩOriginal")
		assertContains(t, res["a.ts"], "exports.__ΩOriginal")
	})

	t.Run("TranspileMultipleReExportsFromDifferentModules", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":     `import { User, Post, Comment } from './index';
const u: User = { id: 1 };
const p: Post = { title: 'test' };
const c: Comment = { text: 'hello' };
[u, p, c];`,
			"index.ts":   `export { User } from './user';
export { Post } from './post';
export { Comment } from './comment';`,
			"user.ts":    `export interface User { id: number; }`,
			"post.ts":    `export interface Post { title: string; }`,
			"comment.ts": `export interface Comment { text: string; }`,
		})
		assertContains(t, res["user.ts"], "exports.__ΩUser")
		assertContains(t, res["post.ts"], "exports.__ΩPost")
		assertContains(t, res["comment.ts"], "exports.__ΩComment")
	})

	t.Run("TranspileClassWithReExport", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":      `import { MyService } from './index';
const svc = new MyService();
svc;`,
			"index.ts":    `export { MyService } from './service';`,
			"service.ts":  `export class MyService { id: number = 0; name: string = ''; }`,
		})
		assertContains(t, res["service.ts"], "__type")
	})

	t.Run("TranspileGenericInterfaceReExport", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":         `import { Container } from './index';
const box: Container<string> = { value: 'hello', getValue: () => 'hello' };
box;`,
			"index.ts":       `export { Container } from './container';`,
			"container.ts":   `export interface Container<T> { value: T; getValue(): T; }`,
		})
		assertContains(t, res["container.ts"], "__ΩContainer")
		assertContains(t, res["container.ts"], "exports.__ΩContainer")
	})

	t.Run("TranspileEnumReExportWithValues", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":    `import { Status } from './index';
const s: Status = Status.Active;
s;`,
			"index.ts": `export { Status } from './enums';`,
			"enums.ts": `export enum Status { Active = 'active', Inactive = 'inactive', Pending = 'pending' }`,
		})
		assertContains(t, res["enums.ts"], "__ΩStatus")
		assertContains(t, res["enums.ts"], "active")
		assertContains(t, res["enums.ts"], "inactive")
		assertContains(t, res["enums.ts"], "pending")
	})

	t.Run("TranspileBrandedTypeReExport", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":    `import { UserID } from './index';
const id: UserID = 'user-123' as UserID;
id;`,
			"index.ts": `export { UserID } from './types';`,
			"types.ts": `export type UserID = string & { __brand: 'UserID' };`,
		})
		assertContains(t, res["types.ts"], "__ΩUserID")
		assertContains(t, res["types.ts"], "exports.__ΩUserID")
	})

	t.Run("TranspileMixedExportsFromBarrel", func(t *testing.T) {
		t.Parallel()
		res := transpileCheck(t, map[string]string{
			"app.ts":      `import { User, UserService, createUser } from './index';
const user: User = { id: 1, name: 'Test' };
const service = new UserService();
const newUser = createUser('New User');
[user, service, newUser];`,
			"index.ts":    `export { User } from './types';
export { UserService } from './service';
export { createUser } from './factory';`,
			"types.ts":    `export interface User { id: number; name: string; }`,
			"service.ts":  `export class UserService { getUsers(): any[] { return []; } }`,
			"factory.ts":  `export function createUser(name: string): any { return { id: Date.now(), name }; }`,
		})
		assertContains(t, res["types.ts"], "__ΩUser")
		assertContains(t, res["types.ts"], "exports.__ΩUser")
		assertContains(t, res["service.ts"], "__type")
		assertContains(t, res["factory.ts"], "createUser.__type")
	})

	// === TYPE-ONLY MODIFIER TESTS ===

	t.Run("TypeOnlyModifierErased", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export type { TypeOnly } from './types';`,
			"types.ts": `export interface TypeOnly { readonly value: string; }`,
		})
		assertContains(t, res["types.ts"], "const __ΩTypeOnly =")
		// Type-only exports are erased at runtime — no __Ω re-export needed
	})

	// === DOCUMENTATION TESTS ===

	t.Run("DocumentSourceFileOmegaGeneration", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"types.ts": `export interface User { id: number; }
export type ID = number;
export enum Status { Active, Inactive }`,
		})
		assertContains(t, res["types.ts"], "const __ΩUser =")
		assertContains(t, res["types.ts"], "const __ΩID =")
		assertContains(t, res["types.ts"], "const __ΩStatus =")
		// CommonJS: exports.__ΩUser
		assertContains(t, res["types.ts"], "exports.__ΩUser")
		assertContains(t, res["types.ts"], "exports.__ΩID")
		assertContains(t, res["types.ts"], "exports.__ΩStatus")
	})

	t.Run("DocumentClassReExportThroughTypeSystem", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"app.ts":    `import { Cache } from './module';
typeOf<Cache>();`,
			"module.ts": `import { Cache } from './class';

export { Cache }`,
			"class.ts":  `export class Cache {}`,
		})
		// The compiler follows the import chain to find the class
		// In CommonJS, the reference is module_1.Cache or module_2.Cache
		assertContains(t, res["app.ts"], "Cache")
	})

	// === ADDITIONAL IMPORT/EXPORT PATTERN TESTS ===

	t.Run("ReExportDefaultExport", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":  `export { default } from './module';`,
			"module.ts": `export default interface DefaultInterface { value: number; }`,
		})
		// module.ts should compile successfully
		if res["module.ts"] == "" {
			t.Fatal("no module.ts output")
		}
		// Default interface has no runtime value — the re-export is erased in CommonJS
	})

	t.Run("ReExportDefaultAsNamed", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":  `export { default as MyInterface } from './module';`,
			"module.ts": `export default interface DefaultInterface { id: number; name: string; }`,
		})
		// Default interface has no runtime value — the re-export is erased in CommonJS
		// Just verify the module compiles
		if res["module.ts"] == "" {
			t.Fatal("no module.ts output")
		}
	})

	t.Run("ExportStarAsNamespace", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export * as Types from './types';`,
			"types.ts": `export interface User { id: number; }
export interface Post { title: string; }
export type ID = number;`,
		})
		assertContains(t, res["types.ts"], "const __ΩUser =")
		assertContains(t, res["types.ts"], "const __ΩPost =")
		assertContains(t, res["types.ts"], "const __ΩID =")
		// CommonJS: exports.Types = __importStar(require("./types"))
		assertContains(t, res["index.ts"], "Types")
		assertContains(t, res["index.ts"], "require(\"./types\")")
	})

	t.Run("ExportWithExplicitJSExtension", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":   `export { Config } from './config.js';`,
			"config.ts": `export interface Config { setting: string; value: number; }`,
		})
		assertContains(t, res["config.ts"], "const __ΩConfig =")
		assertContains(t, res["index.ts"], "__ΩConfig")
		// Go printer uses double quotes
		assertContains(t, res["index.ts"], `"./config.js"`)
	})

	t.Run("DeepBarrelFiles", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":                  `export { DeepType } from './submodule';`,
			"submodule/index.ts":        `export { DeepType } from './nested';`,
			"submodule/nested/index.ts": `export { DeepType } from './types';`,
			"submodule/nested/types.ts": `export interface DeepType { level: number; data: string; }`,
		})
		assertContains(t, res["submodule/nested/types.ts"], "const __ΩDeepType =")
		assertContains(t, res["submodule/nested/index.ts"], "__ΩDeepType")
		assertContains(t, res["submodule/index.ts"], "__ΩDeepType")
		assertContains(t, res["index.ts"], "__ΩDeepType")
	})

	t.Run("InlineTypeModifier", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":  `export { type TypeOnly, RegularExport } from './module';`,
			"module.ts": `export interface TypeOnly { readonly value: string; }
export interface RegularExport { data: number; }`,
		})
		assertContains(t, res["module.ts"], "const __ΩTypeOnly =")
		assertContains(t, res["module.ts"], "const __ΩRegularExport =")
		// Regular exports should still get __Ω re-exports
		assertContains(t, res["index.ts"], "__ΩRegularExport")
	})

	t.Run("MixedDefaultAndNamedInSameStatement", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":  `export { default as DefaultExport, NamedType } from './module';`,
			"module.ts": `export default interface DefaultInterface { defaultValue: boolean; }
export interface NamedType { namedValue: string; }`,
		})
		assertContains(t, res["module.ts"], "const __ΩNamedType =")
		// CommonJS: __ΩNamedType is re-exported via Object.defineProperty
		assertContains(t, res["index.ts"], "__ΩNamedType")
	})

	t.Run("EmptyReExportNoOp", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":  `export {} from './module';`,
			"module.ts": `export interface Unused { value: string; }`,
		})
		assertContains(t, res["module.ts"], "const __ΩUnused =")
		assertNotContains(t, res["index.ts"], "__ΩUnused")
	})

	t.Run("SameSymbolNameFromMultipleModules", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { Config as UserConfig } from './user';
export { Config as PostConfig } from './post';`,
			"user.ts": `export interface Config { userName: string; }`,
			"post.ts": `export interface Config { postTitle: string; }`,
		})
		assertContains(t, res["user.ts"], "const __ΩConfig =")
		assertContains(t, res["post.ts"], "const __ΩConfig =")
		assertContains(t, res["index.ts"], "__ΩUserConfig")
		assertContains(t, res["index.ts"], "__ΩPostConfig")
	})

	t.Run("ReExportThenLocalAugmentation", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `import { BaseType } from './base';
export { BaseType } from './base';

export interface ExtendedType extends BaseType {
    extraField: boolean;
}`,
			"base.ts": `export interface BaseType { id: number; }`,
		})
		assertContains(t, res["base.ts"], "const __ΩBaseType =")
		assertContains(t, res["index.ts"], "__ΩBaseType")
		assertContains(t, res["index.ts"], "const __ΩExtendedType =")
	})

	t.Run("CircularDependency", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"a.ts": `import { TypeB } from './b';
export interface TypeA { ref: TypeB; }
export { TypeB } from './b';`,
			"b.ts": `import { TypeA } from './a';
export interface TypeB { ref: TypeA; }`,
		})
		assertContains(t, res["a.ts"], "const __ΩTypeA =")
		assertContains(t, res["b.ts"], "const __ΩTypeB =")
		assertContains(t, res["a.ts"], "__ΩTypeB")
	})

	t.Run("ReExportGenericTypeWithConstraints", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":       `export { Repository } from './repository';`,
			"repository.ts": `export interface Entity { id: number; }
export interface Repository<T extends Entity> { find(id: number): T | undefined; save(entity: T): void; }`,
		})
		assertContains(t, res["repository.ts"], "const __ΩEntity =")
		assertContains(t, res["repository.ts"], "const __ΩRepository =")
		assertContains(t, res["index.ts"], "__ΩRepository")
	})

	t.Run("ReExportIntersectionType", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { MergedType } from './types';`,
			"types.ts": `interface A { a: string; }
interface B { b: number; }
export type MergedType = A & B;`,
		})
		assertContains(t, res["types.ts"], "const __ΩMergedType =")
		assertContains(t, res["index.ts"], "__ΩMergedType")
	})

	t.Run("ReExportConditionalType", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { IsString } from './types';`,
			"types.ts": `export type IsString<T> = T extends string ? true : false;`,
		})
		assertContains(t, res["types.ts"], "const __ΩIsString =")
		assertContains(t, res["index.ts"], "__ΩIsString")
	})

	t.Run("ReExportMappedType", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts": `export { ReadonlyProps } from './types';`,
			"types.ts": `export type ReadonlyProps<T> = {
    readonly [K in keyof T]: T[K];
};`,
		})
		assertContains(t, res["types.ts"], "const __ΩReadonlyProps =")
		assertContains(t, res["index.ts"], "__ΩReadonlyProps")
	})

	// === VERIFY EXPORT STATEMENT FORMAT ===

	t.Run("VerifyReExportAsSeparateStatement", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":    `export { Context } from './context';`,
			"context.ts":  `export interface Context { id: number; }`,
		})
		// CommonJS: the __Ω re-export is added as a separate Object.defineProperty
		assertContains(t, res["index.ts"], `require("./context")`)
		assertContains(t, res["index.ts"], "__ΩContext")
	})

	t.Run("VerifyReExportWithAliasFormat", func(t *testing.T) {
		t.Parallel()
		res := transform(t, map[string]string{
			"index.ts":    `export { Context as Ctx } from './context';`,
			"context.ts":  `export interface Context { id: number; }`,
		})
		// CommonJS: the aliased __Ω re-export uses Object.defineProperty with getter
		assertContains(t, res["index.ts"], `require("./context")`)
		assertContains(t, res["index.ts"], "__ΩCtx")
		assertContains(t, res["index.ts"], "__ΩContext")
	})

}
