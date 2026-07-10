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
//
// The __type arrays contain real encoded type programs (opcodes + stack entries).
// Opcodes are encoded as charCode(op + 33), e.g.:
//   '!' = 0 (never), '"' = 1 (any), '&' = 5 (string), "'" = 6 (number),
//   '0' = 17 (parameter), '3' = 20 (class), '5' = 22 (classReference),
//   'P' = 48 (method), 'w' = 86 (typeName), 'y' = 88 (nominal)
//
// NOTE: Stack indices > 0 produce characters that need JS escaping.
//   Index 0 → '!' (33), Index 1 → '"' (34, escaped as \" in JS strings),
//   Index 2 → '#' (35), etc.
func TestReflectionTransformer(t *testing.T) {
	t.Parallel()
	data := []struct {
		title  string
		input  string
		output string
	}{
		{
			title: "SimpleClass",
			input: "class User { name: string; age: number; }",
			output: `class User {
    name: string;
    age: number;
    static __type = ["name", "age", "User", "&3!'3\"5w#"];
}`,
		},
		{
			title:  "EmptyClass",
			input:  "class Empty { }",
			output: `class Empty {
    static __type = ["Empty", "5w!"];
}`,
		},
		{
			title: "ClassExpression",
			input: "(class User { name: string; })",
			output: `(class User {
    name: string;
    static __type = ["name", "User", "&3!5w\""];
});`,
		},
		{
			title: "ClassWithMethod",
			input: "class Service { greet(): string { return 'hi'; } }",
			output: `class Service {
    greet(): string { return 'hi'; }
    static __type = ["greet", "Service", "P&0!5w\""];
}`,
		},
		{
			title:  "NonClassPreserved",
			input:  "const x = 42;",
			output: "const x = 42;",
		},
		{
			title: "FunctionDeclaration",
			input: "function add(a: number, b: number): number { return a + b; }",
			output: `function add(a: number, b: number): number { return a + b; }
add.__type = ["a", "b", "add", "P'2!'2\"'/#"];`,
		},
		{
			title: "MultipleClasses",
			input: "class A { a: string; }\nclass B { b: number; }",
			output: `class A {
    a: string;
    static __type = ["a", "A", "&3!5w\""];
}
class B {
    b: number;
    static __type = ["b", "B", "'3!5w\""];
}`,
		},
		// ─── Type aliases ───
		{
			title:  "TypeAlias",
			input:  "type Foo = string;",
			output: `const __ΩFoo = ["Foo", "&w!y"];
type Foo = string;`,
		},
		{
			title:  "ExportedTypeAlias",
			input:  "export type Foo = string;",
			output: `const __ΩFoo = ["Foo", "&w!y"];
export { __ΩFoo as __ΩFoo };
export type Foo = string;`,
		},
		{
			title:  "MultipleTypeAliases",
			input:  "type Foo = string;\ntype Bar = number;",
			output: `const __ΩFoo = ["Foo", "&w!y"];
const __ΩBar = ["Bar", "'w!y"];
type Foo = string;
type Bar = number;`,
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
			title: "ClassAndTypeAlias",
			input: "type Status = string;\nclass User { status: Status; }",
			output: `const __ΩStatus = ["Status", "&w!y"];
type Status = string;
class User {
    status: Status;
    static __type = ["status", "User", "!3!5w\""];
}`,
		},
		{
			title:  "ImportAndClass",
			input:  "import { User } from './models';\nclass Service { user: User; }",
			output: `import { User } from './models';
class Service {
    user: User;
    static __type = ["user", "Service", "!3!5w\""];
}
import { __ΩUser } from './models';`,
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
			title: "SimpleClass",
			input: "class User { name: string; age: number; }",
			output: `class User {
    name;
    age;
    static __type = ["name", "age", "User", "&3!'3\"5w#"];
}`,
		},
		{
			title:  "EmptyClass",
			input:  "class Empty { }",
			output: `class Empty {
    static __type = ["Empty", "5w!"];
}`,
		},
		{
			title: "FunctionDeclaration",
			input: "function add(a: number, b: number): number { return a + b; }",
			output: `function add(a, b) { return a + b; }
add.__type = ["a", "b", "add", "P'2!'2\"'/#"];`,
		},
		// ─── Type aliases are elided by type eraser, __Ω survives ───
		{
			title:  "TypeAliasElided",
			input:  "type Foo = string;",
			output: `const __ΩFoo = ["Foo", "&w!y"];`,
		},
		{
			title:  "ExportedTypeAliasElided",
			input:  "export type Foo = string;",
			output: `const __ΩFoo = ["Foo", "&w!y"];
export { __ΩFoo as __ΩFoo };`,
		},
		// ─── Imports: original import kept (import elision is a separate transformer) ───
		{
			title:  "ImportWithClass",
			input:  "import { User } from './models';\nclass Service { user: User; }",
			output: `import { User } from './models';
class Service {
    user;
    static __type = ["user", "Service", "!3!5w\""];
}
import { __ΩUser } from './models';`,
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
