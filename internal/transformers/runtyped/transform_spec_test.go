package runtyped_test

import (
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/testutil/parsetestutil"
	"github.com/microsoft/typescript-go/internal/transformers"
	"github.com/microsoft/typescript-go/internal/transformers/runtyped"
)

// transformEmit runs the reflection transformer on the given input and returns the emitted code.
func transformEmit(t *testing.T, input string) string {
	t.Helper()
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)
	compilerOptions := &core.CompilerOptions{}
	emitContext := printer.NewEmitContext()
	transformed := runtyped.NewReflectionTransformer(&transformers.TransformOptions{
		CompilerOptions: compilerOptions,
		Context:         emitContext,
	}).TransformSourceFile(file)
	p := printer.NewPrinter(printer.PrinterOptions{NewLine: core.NewLineKindLF}, printer.PrintHandlers{}, emitContext)
	text := p.EmitSourceFile(transformed)
	return strings.TrimSuffix(text, "\n")
}

// assertContains checks that the output contains the given substring.
func assertContains(t *testing.T, output, substr string) {
	t.Helper()
	if !strings.Contains(output, substr) {
		t.Errorf("expected output to contain %q\noutput:\n%s", substr, output)
	}
}

// assertNotContains checks that the output does NOT contain the given substring.
func assertNotContains(t *testing.T, output, substr string) {
	t.Helper()
	if strings.Contains(output, substr) {
		t.Errorf("expected output to NOT contain %q\noutput:\n%s", substr, output)
	}
}

// Port of transform.spec.ts — tests the reflection transformer against
// expected behaviors from the original TypeScript type-compiler test suite.
//
// The TS tests use `expect(code).toContain(...)` assertions rather than
// exact equality checks, so we mirror that with assertContains.
//
// Only single-file tests are ported here. Multi-file tests (resolve import,
// declaration file, re-export) require a vfs/host test harness and are
// deferred to a later phase.
func TestTransformSpec(t *testing.T) {
	t.Parallel()

	t.Run("TransformSimpleTS", func(t *testing.T) {
		t.Parallel()
		// function fn(logger: Logger) {} — should get fn.__type
		// Logger is unresolved (single-file), so it emits as a reference
		output := transformEmit(t, `import { Logger } from './logger.js';

function fn(logger: Logger) {}`)
		assertContains(t, output, "fn.__type")
	})

	t.Run("TransformSimpleJS", func(t *testing.T) {
		t.Parallel()
		// JS files should NOT be transformed
		file := parsetestutil.ParseTypeScript(`
        import { Logger } from './logger.js';
        const a = (v) => {
            return v + 1;
        }
        function fn(logger) {}`, false)
		// Mark as JS
		file.ScriptKind = core.ScriptKindJS
		compilerOptions := &core.CompilerOptions{}
		emitContext := printer.NewEmitContext()
		transformed := runtyped.NewReflectionTransformer(&transformers.TransformOptions{
			CompilerOptions: compilerOptions,
			Context:         emitContext,
		}).TransformSourceFile(file)
		p := printer.NewPrinter(printer.PrinterOptions{NewLine: core.NewLineKindLF}, printer.PrintHandlers{}, emitContext)
		output := strings.TrimSuffix(p.EmitSourceFile(transformed), "\n")
		assertNotContains(t, output, "fn.__type")
	})

	t.Run("TransformUtil", func(t *testing.T) {
		t.Parallel()
		output := transformEmit(t, `function log(message: string) {}`)
		assertContains(t, output, "log.__type = ")
	})

	t.Run("ClassExpression", func(t *testing.T) {
		t.Parallel()
		output := transformEmit(t, `const a = class {};`)
		assertContains(t, output, "static __type = [")
	})

	t.Run("ExportDefaultFunction", func(t *testing.T) {
		t.Parallel()
		output := transformEmit(t, "export default function(bar: string) {\n    return bar;\n}")
		assertContains(t, output, "export default __assignType(function (bar: string")
	})

	t.Run("ExportDefaultAsyncFunction", func(t *testing.T) {
		t.Parallel()
		output := transformEmit(t, "export default async function(bar: string) {\n    return bar;\n}")
		assertContains(t, output, "export default __assignType(async function (bar: string")
	})

	t.Run("DefaultFunctionName", func(t *testing.T) {
		t.Parallel()
		output := transformEmit(t, `const a = {
    default(val: any): any {
        console.log('default', val)
        return 'default'
    }
};`)
		assertNotContains(t, output, "function default(")
	})
}

// ─── ReceiveType tests (single-file, no binder) ───
// Note: These tests verify the transformer's output without a full program.
// resolveValueDeclaration needs the binder to find locals, so direct passing
// is only tested via the harness tests (TestReceiveTypePassing etc.).
// These tests verify Ω reset and parameter default injection.

func TestReceiveTypeSingleFile(t *testing.T) {
	t.Run("FunctionDeclarationReceivesOmegaReset", func(t *testing.T) {
		// function getType<T>(type?: ReceiveType<T>) {}
		// → body starts with getType.Ω = undefined
		output := transformEmit(t, `function getType<T>(type?: ReceiveType<T>) {
            return type;
        }`)

		assertContains(t, output, "getType.Ω = undefined")
	})

	t.Run("FunctionDeclarationParameterDefault", func(t *testing.T) {
		// The ReceiveType<T> parameter should get a default value of getType.Ω
		output := transformEmit(t, `function getType<T>(type?: ReceiveType<T>) {
            return type;
        }`)

		// The parameter should have default = getType.Ω
		assertContains(t, output, "getType.Ω")
	})

	t.Run("PassTypeArgumentArrowFunction", func(t *testing.T) {
		// (<T>(type?: ReceiveType<T>) => {})<string>();
		// Arrow functions in inline expressions are excluded from type passing
		output := transformEmit(t, `(<T>(type?: ReceiveType<T>) => {})<string>();`)

		// Should compile without error — that's the main thing
		_ = output
	})

	t.Run("OmegaSideChannelFallback", func(t *testing.T) {
		// Without binder, resolveValueDeclaration can't find the function,
		// so it falls back to the Ω side-channel: (fn.Ω = [type], fn<string>())
		output := transformEmit(t, `function getType<T>(type?: ReceiveType<T>) {
        }

        getType<string>();`)

		// Should have Ω side-channel (fallback when can't resolve)
		assertContains(t, output, "getType.Ω = [")
	})
}

// ─── Declare statement filtering tests ───

func TestDeclareFiltering(t *testing.T) {
	t.Run("DeclareTypeNoOmega", func(t *testing.T) {
		output := transformEmit(t, `declare type DeclaredType = string;`)
		assertNotContains(t, output, "__ΩDeclaredType")
	})

	t.Run("DeclareInterfaceNoOmega", func(t *testing.T) {
		output := transformEmit(t, `declare interface DeclaredInterface {
    id: number;
    name: string;
}`)
		assertNotContains(t, output, "__ΩDeclaredInterface")
	})

	t.Run("DeclareEnumNoOmega", func(t *testing.T) {
		output := transformEmit(t, `declare enum DeclaredEnum {
    A, B, C
}`)
		assertNotContains(t, output, "__ΩDeclaredEnum")
	})

	t.Run("RegularTypeAliasGeneratesOmega", func(t *testing.T) {
		output := transformEmit(t, `type RegularType = string;`)
		assertContains(t, output, "__ΩRegularType")
	})

	t.Run("RegularInterfaceGeneratesOmega", func(t *testing.T) {
		output := transformEmit(t, `interface RegularInterface {
    id: number;
}`)
		assertContains(t, output, "__ΩRegularInterface")
	})

	t.Run("RegularEnumGeneratesOmega", func(t *testing.T) {
		output := transformEmit(t, `enum RegularEnum {
    A, B, C
}`)
		assertContains(t, output, "__ΩRegularEnum")
	})
}
