package runtyped_test

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/testutil/emittestutil"
	"github.com/microsoft/typescript-go/internal/testutil/parsetestutil"
	"github.com/microsoft/typescript-go/internal/transformers"
	"github.com/microsoft/typescript-go/internal/transformers/runtyped"
	"github.com/microsoft/typescript-go/internal/transformers/tstransforms"
)

// Tests the reflection transformer in isolation (without the type eraser).
// Type annotations remain in the output because the type eraser runs
// after this transformer in the real pipeline.
func TestReflectionTransformer(t *testing.T) {
	t.Parallel()
	data := []struct {
		title  string
		input  string
		output string
	}{
		{
			title:  "SimpleClass",
			input:  "class User { name: string; age: number; }",
			output: "class User {\n    name: string;\n    age: number;\n    static __type = \"\\\"\";\n}",
		},
		{
			title:  "EmptyClass",
			input:  "class Empty { }",
			output: "class Empty {\n    static __type = \"\\\"\";\n}",
		},
		{
			title:  "ClassExpression",
			input:  "(class User { name: string; })",
			output: "(class User {\n    name: string;\n    static __type = \"\\\"\";\n});",
		},
		{
			title:  "ClassWithMethod",
			input:  "class Service { greet(): string { return 'hi'; } }",
			output: "class Service {\n    greet(): string { return 'hi'; }\n    static __type = \"\\\"\";\n}",
		},
		{
			title:  "NonClassPreserved",
			input:  "const x = 42;",
			output: "const x = 42;",
		},
		{
			title:  "FunctionPreserved",
			input:  "function add(a: number, b: number): number { return a + b; }",
			output: "function add(a: number, b: number): number { return a + b; }",
		},
		{
			title:  "MultipleClasses",
			input:  "class A { a: string; }\nclass B { b: number; }",
			output: "class A {\n    a: string;\n    static __type = \"\\\"\";\n}\nclass B {\n    b: number;\n    static __type = \"\\\"\";\n}",
		},
		// ─── Type aliases ───
		{
			title:  "TypeAlias",
			input:  "type Foo = string;",
			output: "const __ΩFoo = \"\\\"\";\ntype Foo = string;",
		},
		{
			title:  "ExportedTypeAlias",
			input:  "export type Foo = string;",
			output: "const __ΩFoo = \"\\\"\";\nexport type Foo = string;\nexport { __ΩFoo as __ΩFoo };",
		},
		{
			title:  "MultipleTypeAliases",
			input:  "type Foo = string;\ntype Bar = number;",
			output: "const __ΩFoo = \"\\\"\";\nconst __ΩBar = \"\\\"\";\ntype Foo = string;\ntype Bar = number;",
		},
		// ─── Imports ───
		{
			title:  "NamedImport",
			input:  "import { User } from './models';",
			output: "import { User } from './models';\nimport { __ΩUser } from './models';",
		},
		{
			title:  "MultipleNamedImports",
			input:  "import { User, Post } from './models';",
			output: "import { User, Post } from './models';\nimport { __ΩUser, __ΩPost } from './models';",
		},
		// ─── Re-exports ───
		{
			title:  "ReExport",
			input:  "export { User } from './models';",
			output: "export { User } from './models';\nexport { __ΩUser as __ΩUser } from './models';",
		},
		// ─── Combined scenarios ───
		{
			title:  "ClassAndTypeAlias",
			input:  "type Status = string;\nclass User { status: Status; }",
			output: "const __ΩStatus = \"\\\"\";\ntype Status = string;\nclass User {\n    status: Status;\n    static __type = \"\\\"\";\n}",
		},
		{
			title:  "ImportAndClass",
			input:  "import { User } from './models';\nclass Service { user: User; }",
			output: "import { User } from './models';\nclass Service {\n    user: User;\n    static __type = \"\\\"\";\n}\nimport { __ΩUser } from './models';",
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

// Tests the full transformer chain: reflection → type eraser.
// This validates that __type and __Ω survive type erasure and
// that type annotations are properly stripped afterward.
func TestReflectionTransformerWithTypeEraser(t *testing.T) {
	t.Parallel()
	data := []struct {
		title  string
		input  string
		output string
	}{
		{
			title:  "SimpleClass",
			input:  "class User { name: string; age: number; }",
			output: "class User {\n    name;\n    age;\n    static __type = \"\\\"\";\n}",
		},
		{
			title:  "EmptyClass",
			input:  "class Empty { }",
			output: "class Empty {\n    static __type = \"\\\"\";\n}",
		},
		{
			title:  "FunctionPreserved",
			input:  "function add(a: number, b: number): number { return a + b; }",
			output: "function add(a, b) { return a + b; }",
		},
		// ─── Type aliases are elided by type eraser, __Ω survives ───
		{
			title:  "TypeAliasElided",
			input:  "type Foo = string;",
			output: "const __ΩFoo = \"\\\"\";",
		},
		{
			title:  "ExportedTypeAliasElided",
			input:  "export type Foo = string;",
			output: "const __ΩFoo = \"\\\"\";\nexport { __ΩFoo as __ΩFoo };",
		},
		// ─── Imports: original import kept (import elision is a separate transformer) ───
		{
			title:  "ImportWithClass",
			input:  "import { User } from './models';\nclass Service { user: User; }",
			output: "import { User } from './models';\nclass Service {\n    user;\n    static __type = \"\\\"\";\n}\nimport { __ΩUser } from './models';",
		},
		// ─── Re-exports: original re-export kept (import elision is a separate transformer) ───
		{
			title:  "ReExportSurvives",
			input:  "export { User } from './models';",
			output: "export { User } from './models';\nexport { __ΩUser as __ΩUser } from './models';",
		},
	}

	for _, rec := range data {
		t.Run(rec.title, func(t *testing.T) {
			t.Parallel()
			file := parsetestutil.ParseTypeScript(rec.input, false)
			parsetestutil.CheckDiagnostics(t, file)
			compilerOptions := &core.CompilerOptions{}
			emitContext := printer.NewEmitContext()
			opts := &transformers.TransformOptions{CompilerOptions: compilerOptions, Context: emitContext}
			// Run reflection transformer first, then type eraser — same order as the real pipeline
			transformed := runtyped.NewReflectionTransformer(opts).TransformSourceFile(file)
			transformed = tstransforms.NewTypeEraserTransformer(opts).TransformSourceFile(transformed)
			emittestutil.CheckEmit(t, emitContext, transformed, rec.output)
		})
	}
}
