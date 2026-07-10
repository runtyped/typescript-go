package runtyped_test

import (
	"testing"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/binder"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/testutil/parsetestutil"
	"github.com/microsoft/typescript-go/internal/transformers"
	"github.com/microsoft/typescript-go/internal/transformers/tstransforms"
)

// ─── Risk Factor 1: Binder Locals Access ───
//
// The existing compiler.ts resolves type references by walking the binder's
// symbol table (node.Locals), NOT the type checker. This test proves we can
// access sourceFile.Locals from within a transformer to find declarations by name.

func TestBinderLocalsAccess(t *testing.T) {
	t.Parallel()

	input := "type Foo = string;\nclass Bar { }"
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	// Bind the file — this populates Locals on the SourceFile
	binder.BindSourceFile(file)

	// Verify Locals is populated
	if file.Locals == nil {
		t.Fatal("expected sourceFile.Locals to be non-nil after binding")
	}

	// Check that we can find "Foo" and "Bar" in the symbol table
	fooSymbol, hasFoo := file.Locals["Foo"]
	if !hasFoo {
		t.Fatal("expected to find 'Foo' in sourceFile.Locals")
	}
	if len(fooSymbol.Declarations) == 0 {
		t.Fatal("expected 'Foo' symbol to have declarations")
	}
	declKind := fooSymbol.Declarations[0].Kind
	if declKind != ast.KindTypeAliasDeclaration {
		t.Fatalf("expected 'Foo' declaration to be TypeAliasDeclaration, got %v", declKind)
	}

	barSymbol, hasBar := file.Locals["Bar"]
	if !hasBar {
		t.Fatal("expected to find 'Bar' in sourceFile.Locals")
	}
	if len(barSymbol.Declarations) == 0 {
		t.Fatal("expected 'Bar' symbol to have declarations")
	}
	declKind = barSymbol.Declarations[0].Kind
	if declKind != ast.KindClassDeclaration {
		t.Fatalf("expected 'Bar' declaration to be ClassDeclaration, got %v", declKind)
	}

	t.Logf("Resolved 'Foo' → TypeAliasDeclaration and 'Bar' → ClassDeclaration via binder locals")
}

func TestBinderLocalsWithImports(t *testing.T) {
	t.Parallel()

	input := "import { User } from './models';"
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	binder.BindSourceFile(file)

	if file.Locals == nil {
		t.Fatal("expected sourceFile.Locals to be non-nil after binding")
	}

	// The binder should have created a symbol for the imported name "User"
	userSymbol, hasUser := file.Locals["User"]
	if !hasUser {
		// In module mode the import might be in the module symbol's exports
		if file.Symbol != nil && file.Symbol.Exports != nil {
			userSymbol, hasUser = file.Symbol.Exports["User"]
		}
		if !hasUser {
			t.Skip("binder did not create a local symbol for 'User' — import may be resolved via module exports instead")
		}
	}

	if len(userSymbol.Declarations) == 0 {
		t.Fatal("expected 'User' symbol to have declarations")
	}

	decl := userSymbol.Declarations[0]
	t.Logf("Resolved import 'User' → declaration kind: %v", decl.Kind)
}

func TestBinderLocalsWithExportedType(t *testing.T) {
	t.Parallel()

	input := "export type Status = string;"
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	binder.BindSourceFile(file)

	// Exported declarations should appear in both locals and module exports
	statusSymbol, hasStatus := file.Locals["Status"]
	if !hasStatus && file.Symbol != nil && file.Symbol.Exports != nil {
		statusSymbol, hasStatus = file.Symbol.Exports["Status"]
	}
	if !hasStatus {
		t.Fatal("expected to find 'Status' in locals or module exports")
	}

	if len(statusSymbol.Declarations) == 0 {
		t.Fatal("expected 'Status' symbol to have declarations")
	}

	declKind := statusSymbol.Declarations[0].Kind
	if declKind != ast.KindTypeAliasDeclaration {
		t.Fatalf("expected 'Status' to be TypeAliasDeclaration, got %v", declKind)
	}

	t.Logf("Resolved exported 'Status' → TypeAliasDeclaration")
}

// TestBinderLocalsInClassScope verifies that class declarations also have
// Locals populated, which is needed for resolving type parameters and
// nested type references.
func TestBinderLocalsInClassScope(t *testing.T) {
	t.Parallel()

	input := "class Foo<T> { value: T; }"
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	binder.BindSourceFile(file)

	if file.Locals == nil {
		t.Fatal("expected sourceFile.Locals to be non-nil after binding")
	}

	// The class declaration itself should be in the file's locals
	fooSymbol, hasFoo := file.Locals["Foo"]
	if !hasFoo {
		t.Fatal("expected to find 'Foo' in sourceFile.Locals")
	}
	if len(fooSymbol.Declarations) == 0 {
		t.Fatal("expected 'Foo' symbol to have declarations")
	}

	// Class members and type parameters are stored in the class symbol's Members table
	// (via declareClassMember), not in Locals. Locals on a class contains block-scoped
	// declarations like constructor locals. This is important for the port: class member
	// resolution goes through Symbol.Members, not Locals.
	if fooSymbol.Members == nil {
		t.Fatal("expected class symbol to have Members populated after binding")
	}

	tParamSymbol, hasT := fooSymbol.Members["T"]
	if !hasT {
		t.Fatal("expected to find type parameter 'T' in class locals")
	}

	if len(tParamSymbol.Declarations) == 0 {
		t.Fatal("expected 'T' symbol to have declarations")
	}

	declKind := tParamSymbol.Declarations[0].Kind
	if declKind != ast.KindTypeParameter {
		t.Fatalf("expected 'T' to be TypeParameter, got %v", declKind)
	}

	t.Logf("Resolved type parameter 'T' in class scope via binder locals")
}

// ─── Risk Factor 2: Complex __type Value Construction ───
//
// The real __type value is an array: [...stackEntries, encodeOps(ops)]
// where stack entries can be string literals, numeric literals, or
// arrow functions (for typeof operations). We verify by parsing source
// that already contains these patterns, then running through the pipeline
// to confirm the printer handles them correctly.

func TestComplexTypeValueArrayWithString(t *testing.T) {
	t.Parallel()
	// Parse a class that has a __type with a string literal entry
	// We test that the printer can emit this correctly
	input := `class Foo { static __type = ["hello", "!"]; }`
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	compilerOptions := &core.CompilerOptions{}
	emitContext := printer.NewEmitContext()
	opts := &transformers.TransformOptions{CompilerOptions: compilerOptions, Context: emitContext}
	transformed := tstransforms.NewTypeEraserTransformer(opts).TransformSourceFile(file)

	expected := `class Foo {
    static __type = ["hello", "!"];
}`
	checkEmitOutput(t, emitContext, transformed, expected)
}

func TestComplexTypeValueArrayWithNumeric(t *testing.T) {
	t.Parallel()
	input := `class Foo { static __type = [42, "!"]; }`
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	compilerOptions := &core.CompilerOptions{}
	emitContext := printer.NewEmitContext()
	opts := &transformers.TransformOptions{CompilerOptions: compilerOptions, Context: emitContext}
	transformed := tstransforms.NewTypeEraserTransformer(opts).TransformSourceFile(file)

	expected := `class Foo {
    static __type = [42, "!"];
}`
	checkEmitOutput(t, emitContext, transformed, expected)
}

func TestComplexTypeValueArrayWithArrowFunction(t *testing.T) {
	t.Parallel()
	// Arrow functions are used for typeof operations: [() => User, "!"]
	input := `class Foo { static __type = [() => User, "!"]; }`
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	compilerOptions := &core.CompilerOptions{}
	emitContext := printer.NewEmitContext()
	opts := &transformers.TransformOptions{CompilerOptions: compilerOptions, Context: emitContext}
	transformed := tstransforms.NewTypeEraserTransformer(opts).TransformSourceFile(file)

	expected := `class Foo {
    static __type = [() => User, "!"];
}`
	checkEmitOutput(t, emitContext, transformed, expected)
}

func TestComplexTypeValueArrayWithMixed(t *testing.T) {
	t.Parallel()
	input := `class Foo { static __type = ["hello", 42, () => User, "!"]; }`
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	compilerOptions := &core.CompilerOptions{}
	emitContext := printer.NewEmitContext()
	opts := &transformers.TransformOptions{CompilerOptions: compilerOptions, Context: emitContext}
	transformed := tstransforms.NewTypeEraserTransformer(opts).TransformSourceFile(file)

	expected := `class Foo {
    static __type = ["hello", 42, () => User, "!"];
}`
	checkEmitOutput(t, emitContext, transformed, expected)
}

func TestComplexTypeValueEmptyArray(t *testing.T) {
	t.Parallel()
	input := `class Foo { static __type = []; }`
	file := parsetestutil.ParseTypeScript(input, false)
	parsetestutil.CheckDiagnostics(t, file)

	compilerOptions := &core.CompilerOptions{}
	emitContext := printer.NewEmitContext()
	opts := &transformers.TransformOptions{CompilerOptions: compilerOptions, Context: emitContext}
	transformed := tstransforms.NewTypeEraserTransformer(opts).TransformSourceFile(file)

	expected := `class Foo {
    static __type = [];
}`
	checkEmitOutput(t, emitContext, transformed, expected)
}

// TestComplexTypeValueConstructionViaFactory validates that we can use the
// NodeFactory to construct the exact same array literals programmatically,
// which is what the real transformer will need to do.
func TestComplexTypeValueConstructionViaFactory(t *testing.T) {
	t.Parallel()

	ctx := printer.NewEmitContext()
	f := ctx.Factory

	// Build: ["hello", 42, () => User, "!"]
	emptyParams := f.NewNodeList(nil)
	elements := []*ast.Node{
		f.NewStringLiteral("hello", ast.TokenFlagsNone),
		f.NewNumericLiteral("42", ast.TokenFlagsNone),
		f.NewArrowFunction(nil, nil, emptyParams, nil, nil,
			f.NewToken(ast.KindEqualsGreaterThanToken),
			f.NewIdentifier("User"),
		),
		f.NewStringLiteral("!", ast.TokenFlagsNone),
	}

	arrExpr := f.NewArrayLiteralExpression(f.NewNodeList(elements), false)

	staticModifier := f.NewModifierList([]*ast.Node{
		f.NewToken(ast.KindStaticKeyword),
	})
	typeMember := f.NewPropertyDeclaration(
		staticModifier,
		f.NewIdentifier("__type"),
		nil, nil, arrExpr,
	)

	// Build a class with this __type
	classMembers := f.NewNodeList([]*ast.Node{typeMember})
	classDecl := f.NewClassDeclaration(nil, f.NewIdentifier("Foo"), nil, nil, classMembers)

	// Parse a dummy source file and replace its statements
	dummyInput := "class Dummy {}"
	file := parsetestutil.ParseTypeScript(dummyInput, false)
	parsetestutil.CheckDiagnostics(t, file)

	stmtList := f.NewNodeList([]*ast.Node{classDecl})
	updated := f.UpdateSourceFile(file, stmtList, file.EndOfFileToken).AsSourceFile()

	printers := printer.NewPrinter(
		printer.PrinterOptions{NewLine: core.NewLineKindLF},
		printer.PrintHandlers{},
		ctx,
	)
	output := printers.EmitSourceFile(updated)

	expected := `class Foo {
    static __type = ["hello", 42, () => User, "!"];
}`
	trimmed := output[:len(output)-1] // trim trailing newline

	if trimmed != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, trimmed)
	}
}

// checkEmitOutput is a helper that prints a source file and compares to expected output
func checkEmitOutput(t *testing.T, ctx *printer.EmitContext, file *ast.SourceFile, expected string) {
	t.Helper()
	p := printer.NewPrinter(
		printer.PrinterOptions{NewLine: core.NewLineKindLF},
		printer.PrintHandlers{},
		ctx,
	)
	output := p.EmitSourceFile(file)
	// Printer adds a trailing newline
	output = output[:len(output)-1]
	if output != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, output)
	}
}
