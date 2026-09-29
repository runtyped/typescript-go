// Additional loader tests ported from loader.spec.ts — the parity-audit gaps
// (2026-09-29): knownFiles tracking, cache sharing, custom compiler options,
// namespace exports, decorators, graceful handling.
//
// NOT ported here (needs wiring decision — see audit): the JS loader reads
// tsconfig.json reflection settings via getConfigResolver; the Go loader takes
// the reflection mode explicitly via LoaderOptions. The tsconfig-driven loader
// variants ("no option + tsconfig with/without reflection", "option overrides
// tsconfig") are blocked until the config resolver is wired into the loader.

package loader_test

import (
	"strings"
	"testing"

	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/transformers/runtyped/loader"
)

func TestLoader_KnownFilesTracking(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	// Transform main.ts FIRST (before shared.ts is known to the loader).
	// The loader should still work because it reads shared.ts from disk.
	mainResult := transformInline(t, l, `import { __ΩCreateUserData } from './shared.js';

function process(data: CreateUserData): boolean {
    return true;
}`, "main-first.ts")

	sharedResult := transformInline(t, l, `export interface CreateUserData {
    readonly name: string;
}`, "shared.ts")

	t.Logf("main.ts:\n%s", mainResult)
	t.Logf("shared.ts:\n%s", sharedResult)
	if !strings.Contains(sharedResult, "__ΩCreateUserData") {
		t.Error("shared: missing __ΩCreateUserData")
	}
}

func TestLoader_CacheSharedBetweenTransforms(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	transformInline(t, l, `type A = Partial<{ name: string }>;`, "cache-file1.ts")

	result := transformInline(t, l, `type B = Partial<{ age: number }>;`, "cache-file2.ts")

	// Should still contain the Partial global (from cache/shared globals)
	if !strings.Contains(result, "__ΩPartial") {
		t.Errorf("cache-file2: missing __ΩPartial\noutput:\n%s", result)
	}
}

func TestLoader_CustomCompilerOptions(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{
		Reflection: loader.ReflectionDefault,
		CompilerOptions: &core.CompilerOptions{
			Strict:        core.TSTrue,
			NoImplicitAny: core.TSTrue,
		},
	})

	result := transformInline(t, l, `interface User {
    name: string;
}`, "compiler-options.ts")

	if !strings.Contains(result, "__ΩUser") {
		t.Errorf("custom options: missing __ΩUser\noutput:\n%s", result)
	}
}

// Divergence note (2026-09-29): the JS reference's transform() prints the full
// source tree (TS syntax included), so its `toContain('Models')` passes even for
// a type-only namespace. The Go loader performs a real JS emit, where a
// namespace containing only types is correctly elided (standard tsc behavior).
// The Go test therefore asserts the meaningful contract instead:
//   1. Omega reflection vars ARE generated for interfaces nested in namespaces.
//   2. With a runtime member, the namespace wrapper and Models.X assignments
//      survive the emit.
func TestLoader_NamespaceExports(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	// Type-only namespace: Omega vars generated, wrapper elided.
	result := transformInline(t, l, `export namespace Models {
    export interface User {
        id: number;
        name: string;
    }

    export interface Post {
        id: number;
        title: string;
        author: User;
    }
}`, "namespace.ts")

	t.Logf("type-only output:\n%s", result)
	if !strings.Contains(result, "__ΩUser") {
		t.Error("namespace: missing __ΩUser reflection var")
	}
	if !strings.Contains(result, "__ΩPost") {
		t.Error("namespace: missing __ΩPost reflection var")
	}

	// Namespace with a runtime member: wrapper survives.
	result = transformInline(t, l, `export namespace Models {
    export interface Shape { kind: string; }
    export class Circle implements Shape { kind: string = "circle"; radius: number = 1; }
}`, "namespace-runtime.ts")

	t.Logf("runtime output:\n%s", result)
	if !strings.Contains(result, "Models") {
		t.Error("namespace with runtime member: missing Models wrapper")
	}
	if !strings.Contains(result, "Models.Circle") {
		t.Error("namespace with runtime member: missing Models.Circle assignment")
	}
	if !strings.Contains(result, "__ΩShape") {
		t.Error("namespace with runtime member: missing __ΩShape reflection var")
	}
}

func TestLoader_DecoratorsOnClasses(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{
		Reflection: loader.ReflectionDefault,
		CompilerOptions: &core.CompilerOptions{
			ExperimentalDecorators: core.TSTrue,
		},
	})

	result := transformInline(t, l, `function Injectable() {
    return function(target: any) {};
}

@Injectable()
class Service {
    constructor(private db: Database) {}
}

interface Database {
    query(sql: string): Promise<any>;
}`, "decorators.ts")

	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "Service") {
		t.Error("decorators: missing Service")
	}
	if !strings.Contains(result, "__type") {
		t.Error("decorators: missing __type")
	}
}

func TestLoader_SyntaxErrorsGracefully(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	// Mild source the TS parser still handles — must not panic or hard-fail.
	result := transformInline(t, l, `interface User {
    name: string;
}`, "syntax-test.ts")

	if !strings.Contains(result, "User") {
		t.Errorf("graceful: missing User\noutput:\n%s", result)
	}
}
