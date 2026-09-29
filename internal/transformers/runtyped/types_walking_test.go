package runtyped_test

import (
	"testing"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/printer"
	"github.com/microsoft/TypeScript/tsc/internal/testutil/emittestutil"
	"github.com/microsoft/TypeScript/tsc/internal/testutil/parsetestutil"
	"github.com/microsoft/TypeScript/tsc/internal/transformers"
	"github.com/microsoft/TypeScript/tsc/internal/transformers/runtyped"
)

// Tests real type walking for various type kinds.
// Verifies that the compiler produces correct opcodes for each TS type construct.
func TestReflectionTypeWalking(t *testing.T) {
	t.Parallel()
	data := []struct {
		title  string
		input  string
		output string
	}{
		{
			title:  "Union",
			input:  "type Foo = string | number;",
			output: "const __ΩFoo = [\"Foo\", \"P&'Jw!y\"];\ntype Foo = string | number;",
		},
		{
			title:  "Array",
			input:  "type Foo = string[];",
			output: "const __ΩFoo = [\"Foo\", \"&Fw!y\"];\ntype Foo = string[];",
		},
		{
			title:  "Intersection",
			input:  "type Foo = string & number;",
			output: "const __ΩFoo = [\"Foo\", \"P&'Kw!y\"];\ntype Foo = string & number;",
		},
		{
			title:  "Void",
			input:  "type Foo = void;",
			output: "const __ΩFoo = [\"Foo\", \"$w!y\"];\ntype Foo = void;",
		},
		{
			title:  "Boolean",
			input:  "type Foo = boolean;",
			output: "const __ΩFoo = [\"Foo\", \")w!y\"];\ntype Foo = boolean;",
		},
		{
			title:  "LiteralString",
			input:  "type Foo = \"hello\";",
			output: `const __ΩFoo = ["hello", "Foo", ".!w\"y"];
type Foo = "hello";`,
		},
		{
			title:  "Tuple",
			input:  "type Foo = [string, number];",
			output: "const __ΩFoo = [\"Foo\", \"P&'Gw!y\"];\ntype Foo = [\n    string,\n    number\n];",
		},
	}

	for _, rec := range data {
		t.Run(rec.title, func(t *testing.T) {
			t.Parallel()
			file := parsetestutil.ParseTypeScript(rec.input, false)
			parsetestutil.CheckDiagnostics(t, file)
			compilerOptions := &core.CompilerOptions{}
			emittestutil.CheckEmit(t, nil, runtyped.NewReflectionTransformer(&transformers.TransformOptions{CompilerOptions: compilerOptions, Context: printer.NewEmitContext()}).TransformSourceFile(file), rec.output)
		})
	}
}
