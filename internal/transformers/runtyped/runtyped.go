package runtyped

import (
	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/transformers"
)

// ReflectionOp mirrors @runtyped/type-spec's ReflectionOp enum.
// Only a minimal subset is defined here for the proof of concept.
const (
	ReflectionOpNever   = 0
	ReflectionOpAny     = 1
	ReflectionOpString  = 5
	ReflectionOpNumber  = 6
	ReflectionOpNominal = 93 // last op in the enum
)

// encodeOps mirrors @runtyped/type-compiler's encodeOps function.
// Each opcode is encoded as a single character (opcode + 33, starting at '!').
func encodeOps(ops []int) string {
	buf := make([]byte, len(ops))
	for i, op := range ops {
		buf[i] = byte(op + 33)
	}
	return string(buf)
}

// NewReflectionTransformer creates a transformer that adds runtime type
// information (__type and __Ω) to classes, type aliases, and imports,
// before type annotations are erased.
func NewReflectionTransformer(opt *transformers.TransformOptions) *transformers.Transformer {
	tx := &reflectionTransformer{
		compilerOptions: opt.CompilerOptions,
		emitContext:     opt.Context,
	}
	return tx.NewTransformer(tx.visit, opt.Context)
}

type reflectionTransformer struct {
	transformers.Transformer
	compilerOptions *core.CompilerOptions
	emitContext     *printer.EmitContext
	// omegaStatements collects __Ω variable declarations to prepend at module scope
	omegaStatements []*ast.Node
	// additionalImports collects import { __ΩX } from '...' statements to append
	additionalImports []*ast.Node
	// additionalReExports collects export { __ΩX } from '...' statements to append
	additionalReExports []*ast.Node
}

func (tx *reflectionTransformer) visit(node *ast.Node) *ast.Node {
	switch node.Kind {
	case ast.KindSourceFile:
		return tx.visitSourceFile(node.AsSourceFile())

	case ast.KindClassDeclaration:
		return tx.visitClassDeclaration(node.AsClassDeclaration())

	case ast.KindClassExpression:
		return tx.visitClassExpression(node.AsClassExpression())

	case ast.KindTypeAliasDeclaration:
		return tx.visitTypeAliasDeclaration(node.AsTypeAliasDeclaration())

	case ast.KindImportDeclaration:
		return tx.visitImportDeclaration(node.AsImportDeclaration())

	case ast.KindExportDeclaration:
		return tx.visitExportDeclaration(node.AsExportDeclaration())

	default:
		return tx.Visitor().VisitEachChild(node)
	}
}

// ─── SourceFile ───

func (tx *reflectionTransformer) visitSourceFile(node *ast.SourceFile) *ast.Node {
	// Reset per-file state
	tx.omegaStatements = nil
	tx.additionalImports = nil
	tx.additionalReExports = nil

	// Visit all children (which collects omegaStatements, additionalImports, etc.)
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsSourceFile()

	// Collect all top-level statements
	statements := visited.Statements.Nodes

	// Append omega variables (type alias declarations) before additional imports
	if len(tx.omegaStatements) > 0 {
		statements = append(tx.omegaStatements, statements...)
	}

	// Append additional imports at the end
	if len(tx.additionalImports) > 0 {
		statements = append(statements, tx.additionalImports...)
	}

	// Append re-exports
	if len(tx.additionalReExports) > 0 {
		statements = append(statements, tx.additionalReExports...)
	}

	// Update the source file with all statements
	stmtList := tx.Factory().NewNodeList(statements)
	stmtList.Loc = visited.Statements.Loc
	return tx.Factory().UpdateSourceFile(visited, stmtList, visited.EndOfFileToken).AsSourceFile().AsNode()
}

// ─── Classes ───

func (tx *reflectionTransformer) visitClassDeclaration(node *ast.ClassDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsClassDeclaration()
	typeMember := tx.createTypeMember()
	members := tx.Factory().NewNodeList(append(visited.Members.Nodes, typeMember))
	return tx.Factory().UpdateClassDeclaration(visited, visited.Modifiers(), visited.Name(), visited.TypeParameters, visited.HeritageClauses, members)
}

func (tx *reflectionTransformer) visitClassExpression(node *ast.ClassExpression) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsClassExpression()
	typeMember := tx.createTypeMember()
	members := tx.Factory().NewNodeList(append(visited.Members.Nodes, typeMember))
	return tx.Factory().UpdateClassExpression(visited, visited.Modifiers(), visited.Name(), visited.TypeParameters, visited.HeritageClauses, members)
}

// ─── Type Aliases ───

func (tx *reflectionTransformer) visitTypeAliasDeclaration(node *ast.TypeAliasDeclaration) *ast.Node {
	// Visit children first
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsTypeAliasDeclaration()

	// Create the __Ω variable: const __ΩFoo = "!"; (hardcoded to ReflectionOp.any for PoC)
	name := visited.Name().AsIdentifier().Text
	omegaVar := tx.createOmegaVariable(name)
	tx.omegaStatements = append(tx.omegaStatements, omegaVar)

	// If the type alias is exported, we also need to re-export __ΩFoo
	if visited.Modifiers() != nil && hasModifier(visited.Modifiers(), ast.ModifierFlagsExport) {
		omegaName := tx.Factory().NewIdentifier("__Ω" + name)
		exportSpec := tx.Factory().NewExportSpecifier(false, omegaName, omegaName)
		namedExports := tx.Factory().NewNamedExports(tx.Factory().NewNodeList([]*ast.Node{exportSpec}))
		reExport := tx.Factory().NewExportDeclaration(nil, false, namedExports, nil, nil)
		tx.additionalReExports = append(tx.additionalReExports, reExport)
	}

	return visited.AsNode()
}

// ─── Imports ───

func (tx *reflectionTransformer) visitImportDeclaration(node *ast.ImportDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsImportDeclaration()

	// Check if this import has named imports
	if visited.ImportClause == nil {
		return visited.AsNode()
	}

	clause := visited.ImportClause.AsImportClause()
	if clause.NamedBindings == nil {
		return visited.AsNode()
	}

	if clause.NamedBindings.Kind != ast.KindNamedImports {
		return visited.AsNode()
	}

	namedImports := clause.NamedBindings.AsNamedImports()
	if namedImports.Elements == nil || len(namedImports.Elements.Nodes) == 0 {
		return visited.AsNode()
	}

	// For each imported name, add __Ω{name} to a new import from the same module
	var omegaSpecifiers []*ast.Node
	for _, spec := range namedImports.Elements.Nodes {
		specNode := spec.AsImportSpecifier()
		name := specNode.Name().AsIdentifier().Text
		omegaName := tx.Factory().NewIdentifier("__Ω" + name)
		omegaSpecifiers = append(omegaSpecifiers, tx.Factory().NewImportSpecifier(false, nil, omegaName))
	}

	if len(omegaSpecifiers) > 0 {
		// Create: import { __ΩX, __ΩY } from 'same-module'
		newNamedImports := tx.Factory().NewNamedImports(tx.Factory().NewNodeList(omegaSpecifiers))
		newImportClause := tx.Factory().NewImportClause(ast.KindUnknown, nil, newNamedImports)
		newImport := tx.Factory().NewImportDeclaration(nil, newImportClause, visited.ModuleSpecifier, nil)
		tx.additionalImports = append(tx.additionalImports, newImport)
	}

	return visited.AsNode()
}

// ─── Exports (re-exports) ───

func (tx *reflectionTransformer) visitExportDeclaration(node *ast.ExportDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsExportDeclaration()

	// Only handle re-exports: export { X } from './module'
	if visited.ModuleSpecifier == nil || visited.ExportClause == nil {
		return visited.AsNode()
	}

	if visited.ExportClause.Kind != ast.KindNamedExports {
		return visited.AsNode()
	}

	namedExports := visited.ExportClause.AsNamedExports()
	if namedExports.Elements == nil || len(namedExports.Elements.Nodes) == 0 {
		return visited.AsNode()
	}

	// For each exported name, add __Ω{name} re-export from the same module
	var omegaSpecifiers []*ast.Node
	for _, spec := range namedExports.Elements.Nodes {
		specNode := spec.AsExportSpecifier()
		name := specNode.Name().AsIdentifier().Text
		omegaName := tx.Factory().NewIdentifier("__Ω" + name)
		omegaSpecifiers = append(omegaSpecifiers, tx.Factory().NewExportSpecifier(false, omegaName, omegaName))
	}

	if len(omegaSpecifiers) > 0 {
		newNamedExports := tx.Factory().NewNamedExports(tx.Factory().NewNodeList(omegaSpecifiers))
		reExport := tx.Factory().NewExportDeclaration(nil, false, newNamedExports, visited.ModuleSpecifier, nil)
		tx.additionalReExports = append(tx.additionalReExports, reExport)
	}

	return visited.AsNode()
}

// ─── Helpers ───

// createTypeMember creates: static __type = "!"
// (encodeOps([ReflectionOp.any]) = String.fromCharCode(1 + 33) = String.fromCharCode(34) = '"')
func (tx *reflectionTransformer) createTypeMember() *ast.Node {
	encodedType := encodeOps([]int{ReflectionOpAny})
	typeValue := tx.Factory().NewStringLiteral(encodedType, ast.TokenFlagsNone)
	staticModifier := tx.Factory().NewModifierList([]*ast.Node{
		tx.Factory().NewToken(ast.KindStaticKeyword),
	})
	return tx.Factory().NewPropertyDeclaration(
		staticModifier,
		tx.Factory().NewIdentifier("__type"),
		nil, // exclamationToken
		nil, // typeNode
		typeValue,
	)
}

// createOmegaVariable creates: const __ΩName = "!";  (for type aliases with reflection)
func (tx *reflectionTransformer) createOmegaVariable(name string) *ast.Node {
	omegaName := tx.Factory().NewIdentifier("__Ω" + name)
	encodedType := encodeOps([]int{ReflectionOpAny})
	typeValue := tx.Factory().NewStringLiteral(encodedType, ast.TokenFlagsNone)
	varDecl := tx.Factory().NewVariableDeclaration(omegaName, nil, nil, typeValue)
	varDeclList := tx.Factory().NewVariableDeclarationList(tx.Factory().NewNodeList([]*ast.Node{varDecl}), ast.NodeFlagsConst)
	return tx.Factory().NewVariableStatement(nil, varDeclList)
}

// hasModifier checks if a ModifierList contains a specific modifier flag.
func hasModifier(modifiers *ast.ModifierList, flag ast.ModifierFlags) bool {
	if modifiers == nil {
		return false
	}
	return modifiers.ModifierFlags&flag != 0
}
