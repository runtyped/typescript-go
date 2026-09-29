package runtyped

import (
	"strconv"
	"sync/atomic"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/printer"
	"github.com/microsoft/TypeScript/tsc/internal/transformers"
)

// NewReflectionTransformer creates a transformer that adds runtime type
// information (__type and __Ω) to classes, type aliases, and imports,
// before type annotations are erased.
func NewReflectionTransformer(opt *transformers.TransformOptions) *transformers.Transformer {
	mode := opt.ReflectionMode
	if mode == "" {
		mode = GetReflectionModeOverride()
	}
	tx := &reflectionTransformer{
		compilerOptions: opt.CompilerOptions,
		emitContext:     opt.Context,
		emitResolver:    opt.EmitResolver,
		sourceFiles:     opt.SourceFiles,
		reflectionMode:  mode,
	}
	return tx.NewTransformer(tx.visit, opt.Context)
}

// SetReflectionModeOverride sets the reflection mode for subsequently created
// reflection transformers. Used by the Loader when TransformOptions isn't available.
// This is not thread-safe — prefer setting TransformOptions.ReflectionMode directly.
// Deprecated: use TransformOptions.ReflectionMode instead.
func SetReflectionModeOverride(mode string) {
	reflectionModeOverride.Store(mode)
}

var reflectionModeOverride atomic.Value

// GetReflectionModeOverride returns the currently set reflection mode override.
func GetReflectionModeOverride() string {
	v := reflectionModeOverride.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

type reflectionTransformer struct {
	transformers.Transformer
	compilerOptions *core.CompilerOptions
	emitContext     *printer.EmitContext
	emitResolver    printer.EmitResolver
	sourceFiles     func() []*ast.SourceFile

	// Reflection mode: "default" (all types reflected), "never" (no reflection),
	// "explicit" (only @reflection-decorated nodes). Empty = "default".
	reflectionMode string

	// Per-file state
	sourceFile *ast.SourceFile
	tc         *typeCompiler

	// omegaStatements collects __Ω variable declarations to prepend at module scope
	omegaStatements []*ast.Node
	// additionalImports collects import { __ΩX } from '...' statements to append
	additionalImports []*ast.Node
	// additionalReExports collects export { __ΩX } from '...' statements to append
	additionalReExports []*ast.Node

	// Optional chain transform state (per-file)
	tempResultIdentifier *ast.Node
	optionalChainTransforms map[*ast.Node]*optionalChainInfo
}

// optionalChainInfo stores metadata about a transformed optional chain expression,
// replacing the TS monkey-patch pattern (__optionalChainTransform on nodes).
type optionalChainInfo struct {
	tempVar     *ast.Node // the Ωr identifier
	condition   *ast.Node // Ωr == null
	whenTrue    *ast.Node // void 0
	assignBase  *ast.Node // Ωr = baseExpression (the left side of the outer comma)
	assignPart  *ast.Node // Ωr.method.Ω = types (the left side of the inner comma)
	callPart    *ast.Node // Ωr.method<T>() (the right side of the inner comma)
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
		result := tx.visitCallExpression(node.AsCallExpression())
		result = tx.handleChainContinuation(result)
		result = tx.fixOrphanedOptionalChain(result)
		return result

	case ast.KindNewExpression:
		return tx.visitNewExpression(node.AsNewExpression())

	case ast.KindPropertyAccessExpression, ast.KindElementAccessExpression:
		visited := tx.Visitor().VisitEachChild(node)
		visited = tx.handleChainContinuation(visited)
		visited = tx.fixOrphanedOptionalChain(visited)
		return visited

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

	// If reflection mode is "never", skip transformation entirely
	if tx.reflectionMode == "never" {
		return node.AsNode()
	}

	// Reset per-file state
	tx.omegaStatements = nil
	tx.additionalImports = nil
	tx.additionalReExports = nil
	tx.tempResultIdentifier = nil
	tx.optionalChainTransforms = make(map[*ast.Node]*optionalChainInfo)
	tx.sourceFile = node
	tx.tc = newTypeCompiler(tx.Factory(), tx.emitResolver, tx.sourceFiles)
	tx.tc.sourceFile = node

	// Visit all children (which collects omegaStatements, additionalImports, etc.)
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsSourceFile()

	// Collect all top-level statements
	originalStatements := visited.Statements.Nodes

	// Process compileDeclarations and embedDeclarations
	// This is the iterative loop from compiler.ts that compiles all pending declarations
	tx.processDeclarations()

	// Build new top statements: omega variables + __assignType helper + hoisted function __type assignments
	var newTopStatements []*ast.Node
	newTopStatements = append(newTopStatements, tx.omegaStatements...)

	// Emit __assignType helper if any function expressions/arrows were wrapped
	if tx.tc.embedAssignType {
		newTopStatements = append(newTopStatements, tx.createAssignTypeHelper())
	}

	newTopStatements = append(newTopStatements, tx.tc.functionTypeAssignments...)

	// If a temp result identifier (Ωr) was used for chained/optional calls, declare it at the top
	if tx.tempResultIdentifier != nil {
		varDecl := tx.Factory().NewVariableStatement(
			nil,
			tx.Factory().NewVariableDeclarationList(
				tx.Factory().NewNodeList([]*ast.Node{
					tx.Factory().NewVariableDeclaration(
						tx.tempResultIdentifier,
						nil, nil, nil,
					),
				}),
				ast.NodeFlagsNone,
			),
		)
		newTopStatements = append(newTopStatements, varDecl)
	}

	// Find "use strict" / "use client" directive to keep it at the top
	literalIdx := -1
	for i, stmt := range originalStatements {
		if stmt.Kind == ast.KindExpressionStatement {
			expr := stmt.AsExpressionStatement().Expression
			if expr != nil && expr.Kind == ast.KindStringLiteral {
				literalIdx = i
				break
			}
		}
	}

	// Insert newTopStatements after any literal expression directive
	var statements []*ast.Node
	if literalIdx >= 0 {
		statements = append(statements, originalStatements[:literalIdx+1]...)
		statements = append(statements, newTopStatements...)
		statements = append(statements, originalStatements[literalIdx+1:]...)
	} else {
		statements = append(statements, newTopStatements...)
		statements = append(statements, originalStatements...)
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

// shouldReflect checks if a declaration should be reflected based on the reflection mode.
// - "default" or "": all declarations are reflected
// - "never": no declarations are reflected (handled at source file level, but this is a safety check)
// - "explicit": only declarations with @reflection JSDoc tag are reflected
func (tx *reflectionTransformer) shouldReflect(node *ast.Node) bool {
	if tx.reflectionMode == "explicit" {
		return tx.hasReflectionJSDoc(node)
	}
	return true
}

// hasReflectionJSDoc checks if a node has a @reflection JSDoc tag.
func (tx *reflectionTransformer) hasReflectionJSDoc(node *ast.Node) bool {
	jsdocs := node.JSDoc(tx.sourceFile)
	for _, jsdoc := range jsdocs {
		if jsdoc.Kind != ast.KindJSDoc {
			continue
		}
		doc := jsdoc.AsJSDoc()
		if doc.Tags == nil {
			continue
		}
		for _, tag := range doc.Tags.Nodes {
			if tag.Kind == ast.KindJSDocUnknownTag {
				tagName := tag.AsJSDocUnknownTag().TagName
				if tagName != nil && tagName.AsIdentifier().Text == "reflection" {
					return true
				}
			}
		}
	}
	return false
}

// ─── Classes ───

func (tx *reflectionTransformer) visitClassDeclaration(node *ast.ClassDeclaration) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node.AsNode()).AsClassDeclaration()

	// In explicit mode, only reflect nodes with @reflection JSDoc tag
	if !tx.shouldReflect(node.AsNode()) {
		return visited.AsNode()
	}

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

	if !tx.shouldReflect(node.AsNode()) {
		return visited.AsNode()
	}

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

	if !tx.shouldReflect(node.AsNode()) {
		return visited.AsNode()
	}

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

	if !tx.shouldReflect(node.AsNode()) {
		return visited.AsNode()
	}

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

	if !tx.shouldReflect(node.AsNode()) {
		return visited.AsNode()
	}

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

	if !tx.shouldReflect(node) {
		return visited.AsNode()
	}

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
	if node.Parent == nil || node.Parent.Kind == ast.KindSourceFile {
		tx.tc.functionTypeAssignments = append(tx.tc.functionTypeAssignments, typeAssignment)
		return visited.AsNode()
	}

	// Block-scoped: return a block containing [function, __type assignment]
	// since the visitor can only return one node, we wrap in a Block
	return tx.Factory().NewBlock(tx.Factory().NewNodeList([]*ast.Node{
		visited.AsNode(),
		typeAssignment,
	}), false)
}

func (tx *reflectionTransformer) visitFunctionExpression(node *ast.Node) *ast.Node {
	visited := tx.Visitor().VisitEachChild(node).AsFunctionExpression()

	if !tx.shouldReflect(node) {
		return visited.AsNode()
	}

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

	if !tx.shouldReflect(node) {
		return visited.AsNode()
	}

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

// createAssignTypeHelper builds the __assignType function declaration:
//
//	function __assignType(fn, args) { fn.__type = args; return fn; }
func (tx *reflectionTransformer) createAssignTypeHelper() *ast.Node {
	f := tx.Factory()
	fnParam := f.NewParameterDeclaration(nil, nil, f.NewIdentifier("fn"), nil, nil, nil)
	argsParam := f.NewParameterDeclaration(nil, nil, f.NewIdentifier("args"), nil, nil, nil)
	body := f.NewBlock(f.NewNodeList([]*ast.Node{
		f.NewExpressionStatement(
			f.NewBinaryExpression(nil,
				f.NewPropertyAccessExpression(f.NewIdentifier("fn"), nil, f.NewIdentifier("__type"), 0),
				nil,
				f.NewToken(ast.KindEqualsToken),
				f.NewIdentifier("args"))),
		f.NewReturnStatement(f.NewIdentifier("fn")),
	}), true)
	return f.NewFunctionDeclaration(nil, nil, f.NewIdentifier("__assignType"), nil,
		f.NewNodeList([]*ast.Node{fnParam, argsParam}), nil, nil, body)
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

// getTempResultIdentifier returns the Ωr identifier for optional chain transforms.
// Generates Ωr, Ωr0, Ωr1, etc. — checking locals to avoid collisions.
func (tx *reflectionTransformer) getTempResultIdentifier() *ast.Node {
	if tx.tempResultIdentifier != nil {
		return tx.tempResultIdentifier
	}
	// In Go we don't have easy access to source file locals for name collision checks.
	// Default to Ωr — collisions are extremely unlikely in practice since Ω-prefixed
	// names are reserved by the runtime type system.
	tx.tempResultIdentifier = tx.Factory().NewIdentifier("Ωr")
	return tx.tempResultIdentifier
}

// expressionContainsOptionalChain recursively checks if an expression contains
// optional chaining (?.) — used to detect patterns like:
// this.service?.getClient().method<T>()
// where the optional chain is in a nested call expression.
func (tx *reflectionTransformer) expressionContainsOptionalChain(expr *ast.Node) bool {
	if expr == nil {
		return false
	}
	if ast.IsOptionalChain(expr) {
		return true
	}
	switch expr.Kind {
	case ast.KindCallExpression:
		return tx.expressionContainsOptionalChain(expr.AsCallExpression().Expression)
	case ast.KindPropertyAccessExpression:
		return tx.expressionContainsOptionalChain(expr.AsPropertyAccessExpression().Expression)
	case ast.KindParenthesizedExpression:
		return tx.expressionContainsOptionalChain(expr.AsParenthesizedExpression().Expression)
	}
	return false
}

// buildOptionalChainTransform rewrites this.service?.doSomething<string>() into:
// (Ωr = this.service, Ωr == null ? void 0 : (Ωr.doSomething.Ω = [types], Ωr.doSomething<string>()))
// Returns nil if the expression is not an optional chain that needs transformation.
func (tx *reflectionTransformer) buildOptionalChainTransform(
	callExpr *ast.CallExpression,
	packedTypeExpr *ast.Node,
) *ast.Node {
	expr := callExpr.Expression

	// Case 1: Direct optional chain — obj?.method<T>()
	// The expression is a PropertyAccessChain (has NodeFlagsOptionalChain and QuestionDotToken)
	if expr.Kind == ast.KindPropertyAccessExpression && ast.IsOptionalChain(expr) {
		propChain := expr.AsPropertyAccessExpression()
		if propChain.QuestionDotToken != nil {
			return tx.doOptionalChainTransform(
				propChain.Expression,  // base: this.service
				propChain.Name(),      // method name
				callExpr.TypeArguments,
				callExpr.Arguments,
				packedTypeExpr,
			)
		}
	}

	// Case 2: Nested optional chain in call expression — obj?.getClient().method<T>()
	// The call expression's expression is PropertyAccess(method) whose expression is a CallExpression
	// that contains an optional chain somewhere inside.
	if expr.Kind == ast.KindPropertyAccessExpression {
		propAccess := expr.AsPropertyAccessExpression()
		if propAccess.Expression != nil && propAccess.Expression.Kind == ast.KindCallExpression &&
			tx.expressionContainsOptionalChain(propAccess.Expression) {
			return tx.doOptionalChainTransform(
				propAccess.Expression,  // base: this.service?.getClient()
				propAccess.Name(),       // method name
				callExpr.TypeArguments,
				callExpr.Arguments,
				packedTypeExpr,
			)
		}
	}

	return nil
}

// doOptionalChainTransform builds the actual ternary rewrite for optional chains.
// baseExpr is the expression to capture into Ωr, methodName is the property to access,
// typeArgs and args come from the original call.
func (tx *reflectionTransformer) doOptionalChainTransform(
	baseExpr *ast.Node,
	methodName *ast.Node,
	typeArgs *ast.NodeList,
	args *ast.NodeList,
	packedTypeExpr *ast.Node,
) *ast.Node {
	factory := tx.Factory()
	r := tx.getTempResultIdentifier()

	// Ωr = baseExpr
	assignBase := factory.NewBinaryExpression(nil,
		r, nil, factory.NewToken(ast.KindEqualsToken), baseExpr,
	)

	// Ωr.method
	rMethod := factory.NewPropertyAccessExpression(r, nil, methodName, 0)

	// Ωr.method.Ω = packedTypeExpr
	assignTypes := factory.NewBinaryExpression(nil,
		factory.NewPropertyAccessExpression(rMethod, nil, factory.NewIdentifier("Ω"), 0),
		nil, factory.NewToken(ast.KindEqualsToken), packedTypeExpr,
	)

	// Ωr.method<T>() — regular call (no optional chain flag)
	var callArgs *ast.NodeList
	if args != nil {
		callArgs = args
	} else {
		callArgs = factory.NewNodeList(nil)
	}
	regularCall := factory.NewCallExpression(rMethod, nil, typeArgs, callArgs, 0)

	// (Ωr.method.Ω = [types], Ωr.method<T>())
	assignAndCall := factory.NewParenthesizedExpression(
		factory.NewBinaryExpression(nil,
			assignTypes, nil, factory.NewToken(ast.KindCommaToken), regularCall,
		),
	)

	// Ωr == null ? void 0 : (...)
	condition := factory.NewBinaryExpression(nil,
		r, nil, factory.NewToken(ast.KindEqualsEqualsToken), factory.NewKeywordExpression(ast.KindNullKeyword),
	)
	conditional := factory.NewConditionalExpression(
		condition,
		factory.NewToken(ast.KindQuestionToken),
		factory.NewVoidZeroExpression(),
		factory.NewToken(ast.KindColonToken),
		assignAndCall,
	)

	// (Ωr = base, Ωr == null ? void 0 : (assign, call))
	result := factory.NewParenthesizedExpression(
		factory.NewBinaryExpression(nil,
			assignBase, nil, factory.NewToken(ast.KindCommaToken), conditional,
		),
	)

	// Record the transform for chain continuation handling
	tx.optionalChainTransforms[result] = &optionalChainInfo{
		tempVar:    r,
		condition:  condition,
		whenTrue:   factory.NewVoidZeroExpression(),
		assignBase: assignBase,
		assignPart: assignTypes,
		callPart:   regularCall,
	}

	return result
}

// handleChainContinuation checks if a call or property access expression has a
// chain continuation on a previously-transformed optional chain, and if so,
// restructures the expression to move the continuation inside the ternary.
func (tx *reflectionTransformer) handleChainContinuation(node *ast.Node) *ast.Node {
	if !ast.IsCallExpression(node) && !ast.IsPropertyAccessExpression(node) {
		return node
	}

	// Get the inner expression to start walking.
	// For call expressions, start from the expression (skip the call itself).
	// For property access expressions, start from the node itself (so it gets added to the chain).
	var innerExpr *ast.Node
	if ast.IsCallExpression(node) {
		innerExpr = node.AsCallExpression().Expression
	} else {
		innerExpr = node
	}

	baseExpr := innerExpr
	factory := tx.Factory()

	// Build the access chain as we walk down to find a transformed optional chain
	type chainAccess struct {
		kind  string // "prop", "call", "elem"
		name  *ast.Node
		args  *ast.NodeList
		typeArgs *ast.NodeList
	}
	var accessChain []chainAccess

	for baseExpr != nil {
		if baseExpr.Kind == ast.KindPropertyAccessExpression && !ast.IsOptionalChain(baseExpr) {
			pa := baseExpr.AsPropertyAccessExpression()
			accessChain = append([]chainAccess{{kind: "prop", name: pa.Name()}}, accessChain...)
			baseExpr = pa.Expression
		} else if baseExpr.Kind == ast.KindCallExpression && baseExpr != node {
			ce := baseExpr.AsCallExpression()
			var ta *ast.NodeList
			if ce.TypeArguments != nil {
				ta = ce.TypeArguments
			}
			accessChain = append([]chainAccess{{kind: "call", args: ce.Arguments, typeArgs: ta}}, accessChain...)
			baseExpr = ce.Expression
		} else if baseExpr.Kind == ast.KindElementAccessExpression && !ast.IsOptionalChain(baseExpr) {
			ea := baseExpr.AsElementAccessExpression()
			accessChain = append([]chainAccess{{kind: "elem", name: ea.ArgumentExpression}}, accessChain...)
			baseExpr = ea.Expression
		} else if baseExpr.Kind == ast.KindParenthesizedExpression {
			info, ok := tx.optionalChainTransforms[baseExpr]
			if ok {
				// Found our transformed optional chain! Restructure.
				// The assignAndCall part is: (assignPart, callPart)
				// We need to rebuild the chain continuation on the callPart.
				chainTarget := info.callPart

				// Apply the access chain
				for _, access := range accessChain {
					switch access.kind {
					case "prop":
						chainTarget = factory.NewPropertyAccessExpression(chainTarget, nil, access.name, 0)
					case "call":
						var args *ast.NodeList
						if access.args != nil {
							args = access.args
						} else {
							args = factory.NewNodeList(nil)
						}
						chainTarget = factory.NewCallExpression(chainTarget, nil, access.typeArgs, args, 0)
					case "elem":
						chainTarget = factory.NewElementAccessExpression(chainTarget, nil, access.name, 0)
					}
				}

				// If the original node was a call, add that final call
				if ast.IsCallExpression(node) {
					var nodeArgs *ast.NodeList
					if node.AsCallExpression().Arguments != nil {
						nodeArgs = node.AsCallExpression().Arguments
					} else {
						nodeArgs = factory.NewNodeList(nil)
					}
					var nodeTypeArgs *ast.NodeList
					if node.AsCallExpression().TypeArguments != nil {
						nodeTypeArgs = node.AsCallExpression().TypeArguments
					}
					chainTarget = factory.NewCallExpression(chainTarget, nil, nodeTypeArgs, nodeArgs, 0)
				}

				// Rebuild: (assignPart, chain.continuation())
				newAssignAndChain := factory.NewParenthesizedExpression(
					factory.NewBinaryExpression(nil,
						info.assignPart, nil, factory.NewToken(ast.KindCommaToken), chainTarget,
					),
				)

				newConditional := factory.NewConditionalExpression(
					info.condition,
					factory.NewToken(ast.KindQuestionToken),
					info.whenTrue,
					factory.NewToken(ast.KindColonToken),
					newAssignAndChain,
				)

				// Get the assignment part (Ωr = base) from the outer binary expression
				result := factory.NewParenthesizedExpression(
					factory.NewBinaryExpression(nil,
						info.assignBase, nil, factory.NewToken(ast.KindCommaToken), newConditional,
					),
				)

				// Update the transform map — the new outer paren is the marker
				tx.optionalChainTransforms[result] = &optionalChainInfo{
					tempVar:    info.tempVar,
					condition:  info.condition,
					whenTrue:   info.whenTrue,
					assignBase: info.assignBase,
					assignPart: info.assignPart,
					callPart:   chainTarget,
				}
				delete(tx.optionalChainTransforms, baseExpr)

				return result
			}
			break
		} else {
			break
		}
	}

	return node
}

// fixOrphanedOptionalChain converts optional chain continuation nodes (those without
// their own ?. token) back to regular expressions when their base is no longer
// an optional chain (because a child transform removed the ?).
func (tx *reflectionTransformer) fixOrphanedOptionalChain(node *ast.Node) *ast.Node {
	factory := tx.Factory()

	if ast.IsOptionalChain(node) {
		// Check if this node has a questionDotToken — if so, it's a chain root, not a continuation
		if node.QuestionDotToken() != nil {
			return node
		}

		// This is a chain continuation (no ?. of its own) — check if its expression
		// is still an optional chain. If not, convert to regular expression.
		var expr *ast.Node
		switch node.Kind {
		case ast.KindPropertyAccessExpression:
			expr = node.AsPropertyAccessExpression().Expression
		case ast.KindCallExpression:
			expr = node.AsCallExpression().Expression
		case ast.KindElementAccessExpression:
			expr = node.AsElementAccessExpression().Expression
		default:
			return node
		}

		if expr != nil && !ast.IsOptionalChain(expr) {
			switch node.Kind {
			case ast.KindPropertyAccessExpression:
				pa := node.AsPropertyAccessExpression()
				return factory.NewPropertyAccessExpression(pa.Expression, nil, pa.Name(), 0)
			case ast.KindCallExpression:
				ce := node.AsCallExpression()
				return factory.NewCallExpression(ce.Expression, nil, ce.TypeArguments, ce.Arguments, 0)
			case ast.KindElementAccessExpression:
				ea := node.AsElementAccessExpression()
				return factory.NewElementAccessExpression(ea.Expression, nil, ea.ArgumentExpression, 0)
			}
		}
	}

	return node
}

// handleTypeArgumentCall handles calls/new expressions with type arguments:
// - Direct passing: if target has ReceiveType params, pass type as argument
// - Ω side-channel: fn.Ω = [type]; fn() fallback
// - Optional chain: rewrite ?.method<T>() to ternary with Ωr temp
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

	// Build the packed type expression (needed for Ω side-channel and optional chain)
	var packedTypeExpr *ast.Node
	if len(typeExpressions) == 1 {
		packedTypeExpr = typeExpressions[0]
	} else {
		packedTypeExpr = factory.NewArrayLiteralExpression(factory.NewNodeList(typeExpressions), false)
	}

	// Optional chain check: if this is a call on an optional chain (?.method<T>()),
	// rewrite to a ternary with a temp variable for null-safe Ω assignment.
	if !isNew && node.Kind == ast.KindCallExpression {
		if result := tx.buildOptionalChainTransform(node.AsCallExpression(), packedTypeExpr); result != nil {
			return result
		}
	}

	// Fallback: Ω side-channel — fn.Ω = [types]; fn()
	var fnExpr *ast.Node
	if isNew {
		fnExpr = node.AsNewExpression().Expression
	} else {
		fnExpr = node.AsCallExpression().Expression
	}

	// Chained call handling: when the call expression is on a method whose
	// receiver is itself a call expression (e.g. http.response<1>().response<2>()),
	// introduce a temp variable (Ωr) to avoid double-evaluation of the inner call.
	// Transform: (Ωr = innerCall, Ωr.method.Ω = [types], Ωr).method()
	if !isNew && fnExpr.Kind == ast.KindPropertyAccessExpression {
		callExpr := node.AsCallExpression()
		propAccess := fnExpr.AsPropertyAccessExpression()
		if propAccess.Expression.Kind == ast.KindCallExpression {
			r := tx.getTempResultIdentifier()
			innerCall := propAccess.Expression
			methodName := propAccess.Name()

			// Ωr = innerCall
			assignBase := factory.NewBinaryExpression(nil,
				r, nil,
				factory.NewToken(ast.KindEqualsToken),
				innerCall,
			)

			// Ωr.method.Ω = [types]
			assignOmega := factory.NewBinaryExpression(nil,
				factory.NewPropertyAccessExpression(
					factory.NewPropertyAccessExpression(r, nil, methodName, 0),
					nil, factory.NewIdentifier("Ω"), 0,
				),
				nil,
				factory.NewToken(ast.KindEqualsToken),
				packedTypeExpr,
			)

			// (Ωr = innerCall, Ωr.method.Ω = [types], Ωr)
			innerExpr := factory.NewBinaryExpression(nil,
				factory.NewBinaryExpression(nil,
					assignBase, nil,
					factory.NewToken(ast.KindCommaToken),
					assignOmega,
				),
				nil,
				factory.NewToken(ast.KindCommaToken),
				r,
			)

			// (Ωr = innerCall, Ωr.method.Ω = [types], Ωr).method(args)
			return factory.NewCallExpression(
				factory.NewPropertyAccessExpression(
					factory.NewParenthesizedExpression(innerExpr),
					nil, methodName, 0,
				),
				nil, // type args erased
				callExpr.TypeArguments,
				callExpr.Arguments,
				callExpr.Flags,
			)
		}

		// Also handle parenthesized call expression: ((expr)).method<T>()
		if propAccess.Expression.Kind == ast.KindParenthesizedExpression {
			pe := propAccess.Expression.AsParenthesizedExpression()
			if pe.Expression.Kind == ast.KindCallExpression {
				r := tx.getTempResultIdentifier()
				innerCall := pe.Expression
				methodName := propAccess.Name()

				// Ωr = innerCall
				assignBase := factory.NewBinaryExpression(nil,
					r, nil,
					factory.NewToken(ast.KindEqualsToken),
					innerCall,
				)

				// Ωr.method.Ω = [types]
				assignOmega := factory.NewBinaryExpression(nil,
					factory.NewPropertyAccessExpression(
						factory.NewPropertyAccessExpression(r, nil, methodName, 0),
						nil, factory.NewIdentifier("Ω"), 0,
					),
					nil,
					factory.NewToken(ast.KindEqualsToken),
					packedTypeExpr,
				)

				// (Ωr = innerCall, Ωr.method.Ω = [types], Ωr).method(args)
				innerExpr := factory.NewBinaryExpression(nil,
					factory.NewBinaryExpression(nil,
						assignBase, nil,
						factory.NewToken(ast.KindCommaToken),
						assignOmega,
					),
					nil,
					factory.NewToken(ast.KindCommaToken),
					r,
				)

				return factory.NewParenthesizedExpression(
					factory.NewCallExpression(
						factory.NewPropertyAccessExpression(
							factory.NewParenthesizedExpression(innerExpr),
							nil, methodName, 0,
						),
						nil, callExpr.TypeArguments, callExpr.Arguments, callExpr.Flags,
					),
				)
			}
		}
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
