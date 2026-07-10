package runtyped

import (
	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/transformers"
)

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

	// Per-file state
	sourceFile *ast.SourceFile
	tc         *typeCompiler

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

	case ast.KindInterfaceDeclaration:
		return tx.visitInterfaceDeclaration(node.AsInterfaceDeclaration())

	case ast.KindEnumDeclaration:
		return tx.visitEnumDeclaration(node.AsEnumDeclaration())

	case ast.KindImportDeclaration:
		return tx.visitImportDeclaration(node.AsImportDeclaration())

	case ast.KindExportDeclaration:
		return tx.visitExportDeclaration(node.AsExportDeclaration())

	case ast.KindFunctionDeclaration:
		return tx.visitFunctionDeclaration(node)

	case ast.KindFunctionExpression:
		return tx.visitFunctionExpression(node)

	case ast.KindArrowFunction:
		return tx.visitArrowFunction(node)

	default:
		return tx.Visitor().VisitEachChild(node)
	}
}

// ─── SourceFile ───

func (tx *reflectionTransformer) visitSourceFile(node *ast.SourceFile) *ast.Node {
	// Skip non-TS/TSX files (JS, JSON, etc.)
	if node.ScriptKind != core.ScriptKindTS && node.ScriptKind != core.ScriptKindTSX {
		return node.AsNode()
	}

	// Reset per-file state
	tx.omegaStatements = nil
	tx.additionalImports = nil
	tx.additionalReExports = nil
	tx.sourceFile = node
	tx.tc = newTypeCompiler(tx.Factory())
	tx.tc.sourceFile = node

	// Visit all children (which collects omegaStatements, additionalImports, etc.)
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsSourceFile()

	// Collect all top-level statements
	statements := visited.Statements.Nodes

	// Process compileDeclarations and embedDeclarations
	// This is the iterative loop from compiler.ts that compiles all pending declarations
	tx.processDeclarations()

	// Prepend omega variables (type alias/interface/enum declarations)
	if len(tx.omegaStatements) > 0 {
		statements = append(tx.omegaStatements, statements...)
	}

	// Append hoisted function __type assignments (fn.__type = [...])
	// These go after omega statements but before additional imports
	if len(tx.tc.functionTypeAssignments) > 0 {
		statements = append(statements, tx.tc.functionTypeAssignments...)
	}

	// Append additional imports
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

// processDeclarations handles the compileDeclarations and embedDeclarations maps.
func (tx *reflectionTransformer) processDeclarations() {
	for {
		allCompiled := true
		for _, declNode := range tx.tc.compileDeclarationOrder {
			d := tx.tc.compileDeclarations[declNode]
			if d == nil || d.compiled != nil {
				continue
			}
			allCompiled = false
			break
		}

		if len(tx.tc.embedDeclarations) == 0 && allCompiled {
			break
		}

		// Compile pending declarations (in insertion order)
		for _, declNode := range tx.tc.compileDeclarationOrder {
			d := tx.tc.compileDeclarations[declNode]
			if d == nil || d.compiled != nil {
				continue
			}
			d.compiled = tx.tc.createProgramVarFromNode(declNode, d.name)
			tx.omegaStatements = append(tx.omegaStatements, d.compiled...)
		}

		// Embed declarations
		if len(tx.tc.embedDeclarations) > 0 {
			for node := range tx.tc.embedDeclarations {
				tx.tc.compiledDeclarations[node] = true
			}
			entries := tx.tc.embedDeclarations
			tx.tc.embedDeclarations = make(map[*ast.Node]*embedDeclEntry)
			for node, d := range entries {
				stmts := tx.tc.createProgramVarFromNode(node, d.name)
				tx.omegaStatements = append(tx.omegaStatements, stmts...)
			}
		}
	}

	// Process additional imports
	if len(tx.tc.addImports) > 0 {
		handled := make(map[string]bool)
		importMap := make(map[*ast.Node][]string)

		for _, imp := range tx.tc.addImports {
			if handled[imp.identifier] {
				continue
			}
			handled[imp.identifier] = true
			importMap[imp.importDecl] = append(importMap[imp.importDecl], imp.identifier)
		}

		for importDecl, identifiers := range importMap {
			// Create: import { __ΩX, __ΩY } from 'same-module'
			var omegaSpecifiers []*ast.Node
			for _, id := range identifiers {
				omegaSpecifiers = append(omegaSpecifiers, tx.Factory().NewImportSpecifier(false, nil, tx.Factory().NewIdentifier(id)))
			}
			newNamedImports := tx.Factory().NewNamedImports(tx.Factory().NewNodeList(omegaSpecifiers))
			newImportClause := tx.Factory().NewImportClause(ast.KindUnknown, nil, newNamedImports)
			moduleSpec := importDecl.AsImportDeclaration().ModuleSpecifier
			newImport := tx.Factory().NewImportDeclaration(nil, newImportClause, moduleSpec, nil)
			tx.additionalImports = append(tx.additionalImports, newImport)
		}
	}
}

// ─── Classes ───

func (tx *reflectionTransformer) visitClassDeclaration(node *ast.ClassDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsClassDeclaration()

	// Check reflection
	// For now, default to true (reflection enabled)
	// Full config resolution is deferred

	// Compile the class type
	typeExpr := tx.tc.getTypeOfType(node.AsNode())
	if typeExpr == nil {
		typeExpr = tx.tc.valueToExpression([]stackEntry{
			{kind: stackEntryString, str: encodeOps([]int{OpAny})},
		})
	}

	typeMember := tx.Factory().NewPropertyDeclaration(
		tx.Factory().NewModifierList([]*ast.Node{
			tx.Factory().NewToken(ast.KindStaticKeyword),
		}),
		tx.Factory().NewIdentifier("__type"),
		nil,
		nil,
		typeExpr,
	)

	members := tx.Factory().NewNodeList(append(visited.Members.Nodes, typeMember))
	return tx.Factory().UpdateClassDeclaration(visited, visited.Modifiers(), visited.Name(), visited.TypeParameters, visited.HeritageClauses, members)
}

func (tx *reflectionTransformer) visitClassExpression(node *ast.ClassExpression) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsClassExpression()

	typeExpr := tx.tc.getTypeOfType(node.AsNode())
	if typeExpr == nil {
		typeExpr = tx.tc.valueToExpression([]stackEntry{
			{kind: stackEntryString, str: encodeOps([]int{OpAny})},
		})
	}

	typeMember := tx.Factory().NewPropertyDeclaration(
		tx.Factory().NewModifierList([]*ast.Node{
			tx.Factory().NewToken(ast.KindStaticKeyword),
		}),
		tx.Factory().NewIdentifier("__type"),
		nil,
		nil,
		typeExpr,
	)

	members := tx.Factory().NewNodeList(append(visited.Members.Nodes, typeMember))
	return tx.Factory().UpdateClassExpression(visited, visited.Modifiers(), visited.Name(), visited.TypeParameters, visited.HeritageClauses, members)
}

// ─── Type Aliases, Interfaces, Enums ───

func (tx *reflectionTransformer) visitTypeAliasDeclaration(node *ast.TypeAliasDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsTypeAliasDeclaration()

	// Register for compilation
	if !hasModifierKind(node.AsNode(), ast.KindDeclareKeyword) {
		declNode := node.AsNode()
		if _, exists := tx.tc.compileDeclarations[declNode]; !exists {
			tx.tc.compileDeclarationOrder = append(tx.tc.compileDeclarationOrder, declNode)
		}
		tx.tc.compileDeclarations[declNode] = &compileDeclEntry{
			name:      getIdentifierName(visited.Name()),
			sourceFile: tx.sourceFile,
		}
	}

	return visited.AsNode()
}

func (tx *reflectionTransformer) visitInterfaceDeclaration(node *ast.InterfaceDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsInterfaceDeclaration()

	if !hasModifierKind(node.AsNode(), ast.KindDeclareKeyword) {
		declNode := node.AsNode()
		if _, exists := tx.tc.compileDeclarations[declNode]; !exists {
			tx.tc.compileDeclarationOrder = append(tx.tc.compileDeclarationOrder, declNode)
		}
		tx.tc.compileDeclarations[declNode] = &compileDeclEntry{
			name:      getIdentifierName(visited.Name()),
			sourceFile: tx.sourceFile,
		}
	}

	return visited.AsNode()
}

func (tx *reflectionTransformer) visitEnumDeclaration(node *ast.EnumDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsEnumDeclaration()

	if !hasModifierKind(node.AsNode(), ast.KindDeclareKeyword) {
		declNode := node.AsNode()
		if _, exists := tx.tc.compileDeclarations[declNode]; !exists {
			tx.tc.compileDeclarationOrder = append(tx.tc.compileDeclarationOrder, declNode)
		}
		tx.tc.compileDeclarations[declNode] = &compileDeclEntry{
			name:      getIdentifierName(visited.Name()),
			sourceFile: tx.sourceFile,
		}
	}

	return visited.AsNode()
}

// ─── Functions ───

func (tx *reflectionTransformer) visitFunctionDeclaration(node *ast.Node) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node).AsFunctionDeclaration()

	encodedType := tx.tc.getTypeOfFunction(node)
	if encodedType == nil {
		return visited.AsNode()
	}

	fnName := visited.Name()
	if fnName == nil {
		// Default export function — wrap with __assignType
		// Strip export/default/decorator modifiers, preserve async
		fnExpr := tx.createFunctionExpressionFromDeclaration(visited)
		tx.tc.embedAssignType = true
		return tx.Factory().NewExportAssignment(nil, false, nil,
			tx.wrapWithAssignType(fnExpr, encodedType))
	}

	// Skip functions named 'default' — `default.__type =` is invalid syntax
	if fnName.Kind == ast.KindIdentifier && fnName.AsIdentifier().Text == "default" {
		return visited.AsNode()
	}

	// fn.__type = encodedType
	typeAssignment := tx.Factory().NewExpressionStatement(
		tx.Factory().NewBinaryExpression(nil,
			tx.Factory().NewPropertyAccessExpression(
				tx.serializeEntityNameAsExpression(fnName), nil, tx.Factory().NewIdentifier("__type"), 0),
			nil,
			tx.Factory().NewToken(ast.KindEqualsToken),
			encodedType))

	// For module-level functions, hoist the __type assignment
	// (parent is nil in synthetic test nodes — treat as module-level)
	if node.Parent == nil || node.Parent.Kind == ast.KindSourceFile {
		tx.tc.functionTypeAssignments = append(tx.tc.functionTypeAssignments, typeAssignment)
		return visited.AsNode()
	}

	// Block-scoped: return both
	// Note: in Go AST, we can't return multiple nodes from a visitor.
	// The TypeScript version returns [declaration, typeAssignment] as an array.
	// We need to handle this differently — for now, append as a sibling.
	// This is a known limitation that the test suite will surface.
	return visited.AsNode()
}

func (tx *reflectionTransformer) visitFunctionExpression(node *ast.Node) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node).AsFunctionExpression()

	encodedType := tx.tc.getTypeOfFunction(node)
	if encodedType == nil {
		return visited.AsNode()
	}

	tx.tc.embedAssignType = true
	return tx.wrapWithAssignType(node, encodedType)
}

func (tx *reflectionTransformer) visitArrowFunction(node *ast.Node) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node).AsArrowFunction()

	encodedType := tx.tc.getTypeOfFunction(node)
	if encodedType == nil {
		return visited.AsNode()
	}

	tx.tc.embedAssignType = true
	return tx.wrapWithAssignType(node, encodedType)
}

// createFunctionExpressionFromDeclaration converts a FunctionDeclaration to a FunctionExpression,
// stripping export/default/decorator modifiers but preserving async.
func (tx *reflectionTransformer) createFunctionExpressionFromDeclaration(decl *ast.FunctionDeclaration) *ast.Node {
	// Filter modifiers: keep async, drop export/default/decorator
	var keptModifiers []*ast.Node
	if mods := decl.Modifiers(); mods != nil {
		for _, mod := range mods.Nodes {
			if mod.Kind != ast.KindExportKeyword && mod.Kind != ast.KindDefaultKeyword && mod.Kind != ast.KindDecorator {
				keptModifiers = append(keptModifiers, mod)
			}
		}
	}
	modifierList := tx.Factory().NewModifierList(keptModifiers)

	return tx.Factory().NewFunctionExpression(
		modifierList,
		decl.AsteriskToken,
		nil, // name is nil for default exports
		decl.TypeParameters,
		decl.Parameters,
		nil, // less than fullSignature
		decl.Type,
		decl.Body,
	)
}

func (tx *reflectionTransformer) wrapWithAssignType(fn *ast.Node, typeExpr *ast.Node) *ast.Node {
	return tx.Factory().NewCallExpression(
		tx.Factory().NewIdentifier("__assignType"),
		nil,
		nil,
		tx.Factory().NewNodeList([]*ast.Node{fn, typeExpr}),
		ast.NodeFlagsNone,
	)
}

// serializeEntityNameAsExpression converts an Identifier to an expression.
func (tx *reflectionTransformer) serializeEntityNameAsExpression(name *ast.Node) *ast.Node {
	if name.Kind == ast.KindIdentifier {
		return tx.Factory().NewIdentifier(name.AsIdentifier().Text)
	}
	if name.Kind == ast.KindQualifiedName {
		qn := name.AsQualifiedName()
		left := tx.serializeEntityNameAsExpression(qn.Left)
		return tx.Factory().NewPropertyAccessExpression(left, nil, qn.Right, 0)
	}
	return tx.Factory().NewIdentifier("undefined")
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
