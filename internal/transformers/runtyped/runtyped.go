package runtyped

import (
	"strconv"

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
		emitResolver:    opt.EmitResolver,
		sourceFiles:     opt.SourceFiles,
	}
	return tx.NewTransformer(tx.visit, opt.Context)
}

type reflectionTransformer struct {
	transformers.Transformer
	compilerOptions *core.CompilerOptions
	emitContext     *printer.EmitContext
	emitResolver    printer.EmitResolver
	sourceFiles     func() []*ast.SourceFile

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

	case ast.KindParameter:
		return tx.visitParameterDeclaration(node.AsParameterDeclaration())

	case ast.KindCallExpression:
		return tx.visitCallExpression(node.AsCallExpression())

	case ast.KindNewExpression:
		return tx.visitNewExpression(node.AsNewExpression())

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
	tx.tc = newTypeCompiler(tx.Factory(), tx.emitResolver, tx.sourceFiles)
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

	// Inject Ω reset if function has ReceiveType params
	visited = tx.injectResetOmega(visited.AsNode()).AsFunctionDeclaration()

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

	// Inject Ω reset if function has ReceiveType params
	visited = tx.injectResetOmega(visited.AsNode()).AsFunctionExpression()

	encodedType := tx.tc.getTypeOfFunction(node)
	if encodedType == nil {
		return visited.AsNode()
	}

	tx.tc.embedAssignType = true
	return tx.wrapWithAssignType(node, encodedType)
}

func (tx *reflectionTransformer) visitArrowFunction(node *ast.Node) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node).AsArrowFunction()

	// Inject Ω reset if function has ReceiveType params
	visited = tx.injectResetOmega(visited.AsNode()).AsArrowFunction()

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

// injectResetOmega prepends `fn.Ω = undefined` (or appropriate container) to the
// function body if the function has ReceiveType<T> parameters.
func (tx *reflectionTransformer) injectResetOmega(node *ast.Node) *ast.Node {
	var parameters []*ast.Node
	var body *ast.Node
	var name *ast.Node

	switch node.Kind {
	case ast.KindFunctionDeclaration:
		fd := node.AsFunctionDeclaration()
		parameters = fd.Parameters.Nodes
		body = fd.Body
		name = fd.Name()
	case ast.KindFunctionExpression:
		fe := node.AsFunctionExpression()
		parameters = fe.Parameters.Nodes
		body = fe.Body
		name = fe.Name()
	case ast.KindArrowFunction:
		af := node.AsArrowFunction()
		parameters = af.Parameters.Nodes
		body = af.Body
		name = nil // Arrow functions get their name from variable declaration parent
	case ast.KindMethodDeclaration:
		md := node.AsMethodDeclaration()
		parameters = md.Parameters.Nodes
		body = md.Body
		name = md.Name()
	case ast.KindConstructor:
		cd := node.AsConstructorDeclaration()
		parameters = cd.Parameters.Nodes
		body = cd.Body
		name = nil
	default:
		return node
	}

	if !hasReceiveTypeParameter(parameters) {
		return node
	}
	if body == nil || body.Kind != ast.KindBlock {
		return node
	}

	// Build the container expression for Ω reset
	var container *ast.Node
	factory := tx.Factory()

	switch node.Kind {
	case ast.KindArrowFunction:
		// For arrow functions, the container comes from the parent variable declaration
		// e.g. const fn = <T>(type: ReceiveType<T>) => {} → fn.Ω = undefined
		// If parent is not a VariableDeclaration, we can't set Ω
		parent := node.Parent
		if parent != nil && parent.Kind == ast.KindVariableDeclaration {
			varDecl := parent.AsVariableDeclaration()
			if varDecl.Name() != nil && varDecl.Name().Kind == ast.KindIdentifier {
				container = factory.NewIdentifier(varDecl.Name().AsIdentifier().Text)
			}
		}
		if container == nil {
			return node
		}
	case ast.KindFunctionDeclaration, ast.KindFunctionExpression:
		if name != nil && name.Kind == ast.KindIdentifier {
			container = factory.NewIdentifier(name.AsIdentifier().Text)
		}
		if container == nil {
			container = factory.NewIdentifier("globalThis")
		}
	case ast.KindMethodDeclaration:
		if name != nil && name.Kind == ast.KindIdentifier {
			container = factory.NewPropertyAccessExpression(
				factory.NewIdentifier("this"), nil, factory.NewIdentifier(name.AsIdentifier().Text), 0)
		}
	case ast.KindConstructor:
		container = factory.NewPropertyAccessExpression(
			factory.NewIdentifier("this"), nil, factory.NewIdentifier("constructor"), 0)
	}

	if container == nil {
		container = factory.NewIdentifier("globalThis")
	}

	// Build: container.Ω = undefined
	resetStmt := factory.NewExpressionStatement(
		factory.NewBinaryExpression(nil,
			factory.NewPropertyAccessExpression(container, nil, factory.NewIdentifier("Ω"), 0),
			nil,
			factory.NewToken(ast.KindEqualsToken),
			factory.NewIdentifier("undefined"),
		))

	// Prepend reset statement to body
	block := body.AsBlock()
	newStatements := append([]*ast.Node{resetStmt}, block.Statements.Nodes...)
	newBody := factory.NewBlock(factory.NewNodeList(newStatements), false)

	// Rebuild the node with new body
	switch node.Kind {
	case ast.KindFunctionDeclaration:
		fd := node.AsFunctionDeclaration()
		return factory.NewFunctionDeclaration(
			fd.Modifiers(), fd.AsteriskToken, fd.Name(), fd.TypeParameters, fd.Parameters, fd.Type, fd.FullSignature, newBody)
	case ast.KindFunctionExpression:
		fe := node.AsFunctionExpression()
		return factory.NewFunctionExpression(
			fe.Modifiers(), fe.AsteriskToken, fe.Name(), fe.TypeParameters, fe.Parameters, fe.Type, fe.FullSignature, newBody)
	case ast.KindArrowFunction:
		af := node.AsArrowFunction()
		return factory.NewArrowFunction(
			af.Modifiers(), af.TypeParameters, af.Parameters, af.Type, af.FullSignature, af.EqualsGreaterThanToken, newBody)
	case ast.KindMethodDeclaration:
		md := node.AsMethodDeclaration()
		return factory.NewMethodDeclaration(
			md.Modifiers(), md.AsteriskToken, md.Name(), md.PostfixToken, md.TypeParameters, md.Parameters, md.Type, md.FullSignature, newBody)
	case ast.KindConstructor:
		cd := node.AsConstructorDeclaration()
		return factory.NewConstructorDeclaration(cd.Modifiers(), cd.TypeParameters, cd.Parameters, cd.Type, cd.FullSignature, newBody)
	}

	return node
}

// getTypeParametersOfFunctionLike extracts type parameters from any function-like node.
func getTypeParametersOfFunctionLike(node *ast.Node) *ast.NodeList {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case ast.KindFunctionDeclaration:
		return node.AsFunctionDeclaration().TypeParameters
	case ast.KindFunctionExpression:
		return node.AsFunctionExpression().TypeParameters
	case ast.KindArrowFunction:
		return node.AsArrowFunction().TypeParameters
	case ast.KindMethodDeclaration:
		return node.AsMethodDeclaration().TypeParameters
	case ast.KindConstructor:
		return nil // Constructor type params come from the class
	}
	return nil
}

// getFunctionLikeName extracts the name from a function-like node.
func getFunctionLikeName(node *ast.Node) *ast.Node {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case ast.KindFunctionDeclaration:
		return node.AsFunctionDeclaration().Name()
	case ast.KindFunctionExpression:
		return node.AsFunctionExpression().Name()
	case ast.KindMethodDeclaration:
		return node.AsMethodDeclaration().Name()
	}
	return nil
}

// visitParameterDeclaration handles ReceiveType<T> parameters by adding a default
// value that reads from the function's Ω property.
func (tx *reflectionTransformer) visitParameterDeclaration(node *ast.ParameterDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsParameterDeclaration()

	if visited.Type == nil {
		return visited.AsNode()
	}

	receiveType := getReceiveTypeParameter(visited.Type)
	if receiveType == nil {
		return visited.AsNode()
	}

	// Get the type parameter name from ReceiveType<T>
	typeRef := receiveType.AsTypeReferenceNode()
	if typeRef.TypeArguments == nil || len(typeRef.TypeArguments.Nodes) == 0 {
		return visited.AsNode()
	}
	first := typeRef.TypeArguments.Nodes[0]
	if first.Kind != ast.KindTypeReference {
		return visited.AsNode()
	}
	firstRef := first.AsTypeReferenceNode()
	if firstRef.TypeName.Kind != ast.KindIdentifier {
		return visited.AsNode()
	}
	typeParamName := firstRef.TypeName.AsIdentifier().Text

	// Find the parent function to get type parameters
	parent := visited.Parent
	if parent == nil {
		return visited.AsNode()
	}

	var typeParameters *ast.NodeList
	switch parent.Kind {
	case ast.KindConstructor:
		// Constructor's type params are on the class
		classNode := parent.Parent
		if classNode != nil && (classNode.Kind == ast.KindClassDeclaration || classNode.Kind == ast.KindClassExpression) {
			typeParameters = classNode.ClassLikeData().TypeParameters
		}
	default:
		// Function/Method/Arrow — type params are on the parent
		typeParameters = getTypeParametersOfFunctionLike(parent)
	}

	if typeParameters == nil || len(typeParameters.Nodes) == 0 {
		return visited.AsNode()
	}

	// Find the type parameter index
	typeParamIdx := -1
	for i, tp := range typeParameters.Nodes {
		tpDecl := tp.AsTypeParameterDeclaration()
		if tpDecl.Name() != nil && tpDecl.Name().Kind == ast.KindIdentifier &&
			tpDecl.Name().AsIdentifier().Text == typeParamName {
			typeParamIdx = i
			break
		}
	}
	if typeParamIdx == -1 {
		return visited.AsNode()
	}

	// Build the container expression
	var container *ast.Node
	factory := tx.Factory()

	switch parent.Kind {
	case ast.KindArrowFunction:
		// Arrow function: container is the variable name from parent declaration
		varDecl := parent.Parent
		if varDecl != nil && varDecl.Kind == ast.KindVariableDeclaration {
			if varDecl.AsVariableDeclaration().Name() != nil && varDecl.AsVariableDeclaration().Name().Kind == ast.KindIdentifier {
				container = factory.NewIdentifier(varDecl.AsVariableDeclaration().Name().AsIdentifier().Text)
			}
		}
		if container == nil {
			return visited.AsNode()
		}
	case ast.KindFunctionDeclaration, ast.KindFunctionExpression:
		fnName := getFunctionLikeName(parent)
		if fnName != nil && fnName.Kind == ast.KindIdentifier {
			container = factory.NewIdentifier(fnName.AsIdentifier().Text)
		}
		if container == nil {
			container = factory.NewIdentifier("globalThis")
		}
	case ast.KindMethodDeclaration:
		if parent.AsMethodDeclaration().Name() != nil && parent.AsMethodDeclaration().Name().Kind == ast.KindIdentifier {
			container = factory.NewPropertyAccessExpression(
				factory.NewIdentifier("this"), nil, factory.NewIdentifier(parent.AsMethodDeclaration().Name().AsIdentifier().Text), 0)
		}
	case ast.KindConstructor:
		container = factory.NewPropertyAccessExpression(
			factory.NewIdentifier("this"), nil, factory.NewIdentifier("constructor"), 0)
	default:
		container = factory.NewIdentifier("globalThis")
	}

	// For single type param: read Ω directly. For multiple: Ω?.[index]
	var defaultValue *ast.Node
	omegaAccess := factory.NewPropertyAccessExpression(container, nil, factory.NewIdentifier("Ω"), 0)
	if len(typeParameters.Nodes) == 1 {
		defaultValue = omegaAccess
	} else {
		defaultValue = factory.NewElementAccessExpression(omegaAccess, factory.NewToken(ast.KindQuestionDotToken), factory.NewNumericLiteral(strconv.Itoa(typeParamIdx), ast.TokenFlagsNone), 0)
	}

	// Rebuild the parameter with the default value
	return factory.NewParameterDeclaration(
		visited.Modifiers(),
		visited.DotDotDotToken,
		visited.Name(),
		visited.QuestionToken,
		receiveType, // type stays as ReceiveType<T> (will be erased)
		defaultValue,
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

// ─── Call/New Expressions (type argument passing) ───

// visitCallExpression handles calls with type arguments:
// - typeOf<T>() → typeOf(__ΩT) (auto-type functions inline the type as an argument)
// - fn<T>() → fn.Ω = [type]; fn() (Ω side-channel for ReceiveType)
// Currently only auto-type functions (typeOf, valuesOf, propertiesOf) are implemented.
func (tx *reflectionTransformer) visitCallExpression(node *ast.CallExpression) *ast.Node {
	// Resolve ReceiveType info from the ORIGINAL node (before visiting, parent chain intact)
	var callResult *CallReceiveTypeResult
	if node.AsCallExpression().TypeArguments != nil && len(node.AsCallExpression().TypeArguments.Nodes) > 0 {
		// Check for auto-type functions: typeOf, valuesOf, propertiesOf
		if node.Expression != nil && node.Expression.Kind == ast.KindIdentifier {
			fnName := node.Expression.AsIdentifier().Text
			if fnName == "typeOf" || fnName == "valuesOf" || fnName == "propertiesOf" {
				visited := tx.Visitor().VisitEachChild(node.AsNode()).AsCallExpression()
				return tx.handleAutoTypeFunction(visited.AsNode(), false)
			}
		}
		callResult = tx.tc.resolveCallReceiveTypeInfo(node.AsNode())
	}

	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsCallExpression()

	// Only handle calls with type arguments
	if visited.TypeArguments == nil || len(visited.TypeArguments.Nodes) == 0 {
		return visited.AsNode()
	}

	return tx.handleTypeArgumentCall(visited.AsNode(), false, callResult)
}

// visitNewExpression handles `new Foo<T>()` with type arguments.
func (tx *reflectionTransformer) visitNewExpression(node *ast.NewExpression) *ast.Node {
	// Resolve ReceiveType info from the ORIGINAL node
	var callResult *CallReceiveTypeResult
	if node.AsNewExpression().TypeArguments != nil && len(node.AsNewExpression().TypeArguments.Nodes) > 0 {
		callResult = tx.tc.resolveCallReceiveTypeInfo(node.AsNode())
	}

	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsNewExpression()

	if visited.TypeArguments == nil || len(visited.TypeArguments.Nodes) == 0 {
		return visited.AsNode()
	}

	return tx.handleTypeArgumentCall(visited.AsNode(), true, callResult)
}

// handleTypeArgumentCall handles calls/new expressions with type arguments:
// - Direct passing: if target has ReceiveType params, pass type as argument
// - Ω side-channel: fn.Ω = [type]; fn() fallback
func (tx *reflectionTransformer) handleTypeArgumentCall(node *ast.Node, isNew bool, callResult *CallReceiveTypeResult) *ast.Node {
	factory := tx.Factory()

	// Resolve type arguments to encoded expressions
	var typeExpressions []*ast.Node
	var typeArgs []*ast.Node
	if isNew {
		newExpr := node.AsNewExpression()
		typeArgs = newExpr.TypeArguments.Nodes
	} else {
		callExpr := node.AsCallExpression()
		typeArgs = callExpr.TypeArguments.Nodes
	}

	for _, a := range typeArgs {
		typeExpr := tx.tc.getTypeOfType(a)
		if typeExpr == nil {
			typeExpr = factory.NewIdentifier("undefined")
		}
		typeExpressions = append(typeExpressions, typeExpr)
	}

	// Check if expression is an inline arrow function — skip type passing
	var exprToCheck *ast.Node
	if isNew {
		exprToCheck = node.AsNewExpression().Expression
	} else {
		exprToCheck = node.AsCallExpression().Expression
	}
	checkExpr := getAssignTypeExpression(exprToCheck)
	if checkExpr != nil {
		exprToCheck = checkExpr
	}
	if exprToCheck != nil && exprToCheck.Kind == ast.KindArrowFunction {
		// Inline arrow functions are excluded from type passing
		return node
	}

	// Try direct argument passing: use pre-resolved call target ReceiveType info
	if callResult != nil {
		if callResult.Kind == "skip" {
			// Resolved target has no ReceiveType params — skip type passing
			return node
		}
		// Direct passing: place type args at correct parameter positions
		var existingArgs []*ast.Node
		if isNew {
			if node.AsNewExpression().Arguments != nil {
				existingArgs = node.AsNewExpression().Arguments.Nodes
			}
		} else {
			if node.AsCallExpression().Arguments != nil {
				existingArgs = node.AsCallExpression().Arguments.Nodes
			}
		}
		newArgs := tx.tc.buildDirectPassingArgs(existingArgs, typeExpressions, callResult.Info)
		if newArgs != nil {
			if isNew {
				newExpr := node.AsNewExpression()
				return factory.NewNewExpression(
					newExpr.Expression,
					newExpr.TypeArguments,
					factory.NewNodeList(newArgs),
				)
			}
			callExpr := node.AsCallExpression()
			return factory.NewCallExpression(
				callExpr.Expression,
				nil,
				callExpr.TypeArguments,
				factory.NewNodeList(newArgs),
				callExpr.Flags,
			)
		}
	}

	// Fallback: Ω side-channel — fn.Ω = [types]; fn()
	var fnExpr *ast.Node
	if isNew {
		fnExpr = node.AsNewExpression().Expression
	} else {
		fnExpr = node.AsCallExpression().Expression
	}

	// Build the packed type expression
	var packedTypeExpr *ast.Node
	if len(typeExpressions) == 1 {
		packedTypeExpr = typeExpressions[0]
	} else {
		packedTypeExpr = factory.NewArrayLiteralExpression(factory.NewNodeList(typeExpressions), false)
	}

	// fn.Ω = packedTypeExpr
	omegaAssign := factory.NewBinaryExpression(nil,
		factory.NewPropertyAccessExpression(fnExpr, nil, factory.NewIdentifier("Ω"), 0),
		nil,
		factory.NewToken(ast.KindEqualsToken),
		packedTypeExpr,
	)

	// Build the call/new without type arguments (they stay for TS but will be erased)
	var callNode *ast.Node
	if isNew {
		newExpr := node.AsNewExpression()
		callNode = factory.NewNewExpression(
			newExpr.Expression,
			newExpr.TypeArguments,
			newExpr.Arguments,
		)
	} else {
		callExpr := node.AsCallExpression()
		callNode = factory.NewCallExpression(
			callExpr.Expression,
			nil,
			callExpr.TypeArguments,
			callExpr.Arguments,
			callExpr.Flags,
		)
	}

	// (fn.Ω = [types], fn())
	return factory.NewParenthesizedExpression(
		factory.NewBinaryExpression(nil,
			omegaAssign,
			nil,
			factory.NewToken(ast.KindCommaToken),
			callNode,
		),
	)
}

// handleAutoTypeFunction transforms typeOf<T>() → typeOf(__ΩT)
// by resolving the type argument to its encoded form and appending it as a regular argument.
func (tx *reflectionTransformer) handleAutoTypeFunction(node *ast.Node, isNew bool) *ast.Node {
	call := node.AsCallExpression()
	typeArg := call.TypeArguments.Nodes[0]

	// Resolve the type to an encoded expression
	typeExpr := tx.tc.getTypeOfType(typeArg)
	if typeExpr == nil {
		typeExpr = tx.Factory().NewIdentifier("undefined")
	}

	// Build new arguments: existing args + type expression
	var args []*ast.Node
	if call.Arguments != nil {
		args = append(args, call.Arguments.Nodes...)
	}
	// If no existing args, push an empty array as placeholder (matching TS behavior)
	if len(args) == 0 {
		args = append(args, tx.Factory().NewArrayLiteralExpression(tx.Factory().NewNodeList(nil), false))
	}
	args = append(args, typeExpr)

	if isNew {
		newExpr := node.AsNewExpression()
		return tx.Factory().NewNewExpression(
			newExpr.Expression,
			newExpr.TypeArguments,
			tx.Factory().NewNodeList(args),
		)
	}

	return tx.Factory().NewCallExpression(
		call.Expression,
		nil, // questionDotToken
		call.TypeArguments,
		tx.Factory().NewNodeList(args),
		call.Flags,
	)
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

	// For each exported name, check if the resolved symbol is a type that
	// generates __Ω (interface, type alias, enum). Classes do NOT get __Ω
	// re-exports — they use static __type. Value exports don't either.
	var omegaSpecifiers []*ast.Node
	for _, spec := range namedExports.Elements.Nodes {
		specNode := spec.AsExportSpecifier()
		exportedName := specNode.Name().AsIdentifier().Text
		originalName := exportedName
		if specNode.PropertyName != nil {
			originalName = specNode.PropertyName.AsIdentifier().Text
		}

		// Check if this symbol should get a __Ω re-export
		if !tx.tc.shouldReExportOmegaSymbol(originalName, visited.AsNode()) {
			continue
		}

		omegaExportedName := tx.Factory().NewIdentifier("__Ω" + exportedName)
		var omegaPropertyName *ast.Node
		if specNode.PropertyName != nil {
			omegaPropertyName = tx.Factory().NewIdentifier("__Ω" + originalName)
		} else {
			omegaPropertyName = omegaExportedName
		}

		omegaSpecifiers = append(omegaSpecifiers, tx.Factory().NewExportSpecifier(false, omegaPropertyName, omegaExportedName))
	}

	if len(omegaSpecifiers) > 0 {
		newNamedExports := tx.Factory().NewNamedExports(tx.Factory().NewNodeList(omegaSpecifiers))
		reExport := tx.Factory().NewExportDeclaration(nil, false, newNamedExports, visited.ModuleSpecifier, nil)
		tx.additionalReExports = append(tx.additionalReExports, reExport)
	}

	return visited.AsNode()
}
