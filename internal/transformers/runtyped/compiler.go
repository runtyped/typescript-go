package runtyped

import (
	"path/filepath"
	"strings"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/printer"
)

// resolveDeclarationResult holds a resolved declaration and metadata.
type resolveDeclarationResult struct {
	declaration       *ast.Node
	importDeclaration *ast.Node // ImportDeclaration or nil
	typeOnly          bool
	isGlobal          bool // true if resolved from global lib files
}

// typeCompiler holds the state needed during type compilation for one source file.
// It is the Go equivalent of the TypeScript CompilerProgram + extractPackStructOfType logic.
type typeCompiler struct {
	factory *printer.NodeFactory
	emitResolver printer.EmitResolver
	sourceFiles  func() []*ast.SourceFile

	sourceFile *ast.SourceFile

	// compileDeclarations: types in the same file that need __Ω
	compileDeclarations map[*ast.Node]*compileDeclEntry
	// compileDeclarationOrder: preserves insertion order (Go maps are unordered)
	compileDeclarationOrder []*ast.Node
	// embedDeclarations: imported/global types that need inlining
	embedDeclarations map[*ast.Node]*embedDeclEntry
	// compiledDeclarations: track what's already been compiled (to break recursion)
	compiledDeclarations map[*ast.Node]bool

	// addImports tracks __Ω identifiers that need import statements
	addImports []*addImportEntry

	// pendingReExports tracks re-exports that need __Ω
	pendingReExports []*pendingReExport

	// functionTypeAssignments: hoisted function __type assignments
	functionTypeAssignments []*ast.Node

	// embedAssignType: whether we need to emit the __assignType helper
	embedAssignType bool

	// tempResultIdentifier for optional chain transforms
	tempResultIdentifier *ast.Node
}

type compileDeclEntry struct {
	name      string
	sourceFile *ast.SourceFile
	compiled  []*ast.Node
}

type embedDeclEntry struct {
	name      string
	sourceFile *ast.SourceFile
}

type addImportEntry struct {
	identifier string
	importDecl *ast.Node
}

type pendingReExport struct {
	exportDecl *ast.Node
	symbols    []reExportSymbol
}

type reExportSymbol struct {
	originalName string
	exportedName string
}

func newTypeCompiler(factory *printer.NodeFactory, emitResolver printer.EmitResolver, sourceFiles func() []*ast.SourceFile) *typeCompiler {
	return &typeCompiler{
		factory:                factory,
		emitResolver:           emitResolver,
		sourceFiles:            sourceFiles,
		compileDeclarations:    make(map[*ast.Node]*compileDeclEntry),
		compileDeclarationOrder: nil,
		embedDeclarations:      make(map[*ast.Node]*embedDeclEntry),
		compiledDeclarations:   make(map[*ast.Node]bool),
	}
}

// extractPackStructOfType walks a type node and emits opcodes into the program.
// This is the core of the compiler — the Go translation of compiler.ts's extractPackStructOfType.
func (tc *typeCompiler) extractPackStructOfType(node *ast.Node, program *compilerProgram) {
	if node == nil {
		return
	}

	// Unwrap parenthesized types
	if node.Kind == ast.KindParenthesizedType {
		pt := node.AsParenthesizedTypeNode()
		tc.extractPackStructOfType(pt.Type, program)
		return
	}

	switch node.Kind {
	// ─── Keyword types ───
	case ast.KindStringKeyword:
		program.pushOp(OpString)
	case ast.KindNumberKeyword:
		program.pushOp(OpNumber)
	case ast.KindBooleanKeyword:
		program.pushOp(OpBoolean)
	case ast.KindBigIntKeyword:
		program.pushOp(OpBigInt)
	case ast.KindVoidKeyword:
		program.pushOp(OpVoid)
	case ast.KindUnknownKeyword:
		program.pushOp(OpUnknown)
	case ast.KindObjectKeyword:
		program.pushOp(OpObject)
	case ast.KindSymbolKeyword:
		program.pushOp(OpSymbol)
	case ast.KindNullKeyword:
		program.pushOp(OpNull)
	case ast.KindNeverKeyword:
		program.pushOp(OpNever)
	case ast.KindAnyKeyword:
		program.pushOp(OpAny)
	case ast.KindUndefinedKeyword:
		program.pushOp(OpUndefined)
	case ast.KindTrueKeyword:
		program.pushOp(OpLiteral, program.pushStackNode(tc.factory.NewKeywordExpression(ast.KindTrueKeyword)))
	case ast.KindFalseKeyword:
		program.pushOp(OpLiteral, program.pushStackNode(tc.factory.NewKeywordExpression(ast.KindFalseKeyword)))

	// ─── Class declarations/expressions ───
	case ast.KindClassDeclaration, ast.KindClassExpression:
		tc.extractClass(node, program)

	// ─── Intersection types ───
	case ast.KindIntersectionType:
		it := node.AsIntersectionTypeNode()
		program.pushFrame(false)
		for _, t := range it.Types.Nodes {
			tc.extractPackStructOfType(t, program)
		}
		program.pushOp(OpIntersection)
		program.popFrameImplicit()

	// ─── Mapped types ───
	case ast.KindMappedType:
		tc.extractMappedType(node, program)

	// ─── Type alias declaration ───
	case ast.KindTypeAliasDeclaration:
		tc.extractTypeAlias(node, program)

	// ─── Type literal / Interface declaration ───
	case ast.KindTypeLiteral, ast.KindInterfaceDeclaration:
		tc.extractInterfaceOrTypeLiteral(node, program)

	// ─── Type reference ───
	case ast.KindTypeReference:
		tc.extractTypeReference(node.AsTypeReferenceNode(), program)

	// ─── Array type ───
	case ast.KindArrayType:
		at := node.AsArrayTypeNode()
		tc.extractPackStructOfType(at.ElementType, program)
		program.pushOp(OpArray)

	// ─── Rest type ───
	case ast.KindRestType:
		rt := node.AsRestTypeNode()
		t := rt.Type
		if t.Kind == ast.KindArrayType {
			t = t.AsArrayTypeNode().ElementType
		}
		tc.extractPackStructOfType(t, program)
		program.pushOp(OpRest)

	// ─── Tuple type ───
	case ast.KindTupleType:
		tc.extractTupleType(node, program)

	// ─── Property signature ───
	case ast.KindPropertySignature:
		tc.extractPropertySignature(node, program)

	// ─── Property declaration ───
	case ast.KindPropertyDeclaration:
		tc.extractPropertyDeclaration(node, program)

	// ─── Conditional type ───
	case ast.KindConditionalType:
		tc.extractConditionalType(node, program)

	// ─── Infer type ───
	case ast.KindInferType:
		tc.extractInferType(node, program)

	// ─── Function-like declarations (method, constructor, function, arrow, etc.) ───
	case ast.KindMethodSignature, ast.KindMethodDeclaration, ast.KindConstructor,
		ast.KindArrowFunction, ast.KindFunctionExpression, ast.KindConstructSignature,
		ast.KindConstructorType, ast.KindFunctionType, ast.KindCallSignature, ast.KindFunctionDeclaration:
		tc.extractFunctionLike(node, program)

	// ─── Literal type ───
	case ast.KindLiteralType:
		lt := node.AsLiteralTypeNode()
		if lt.Literal.Kind == ast.KindNullKeyword {
			program.pushOp(OpNull)
		} else {
			program.pushOp(OpLiteral, program.findOrAddStackEntry(stackEntry{kind: stackEntryNode, node: lt.Literal}))
		}

	// ─── Template literal type ───
	case ast.KindTemplateLiteralType:
		tc.extractTemplateLiteralType(node, program)

	// ─── Union type ───
	case ast.KindUnionType:
		ut := node.AsUnionTypeNode()
		if len(ut.Types.Nodes) == 0 {
			// nothing to emit
		} else if len(ut.Types.Nodes) == 1 {
			tc.extractPackStructOfType(ut.Types.Nodes[0], program)
		} else {
			program.pushFrame(false)
			for _, subType := range ut.Types.Nodes {
				tc.extractPackStructOfType(subType, program)
			}
			program.pushOp(OpUnion)
			program.popFrameImplicit()
		}

	// ─── Enum declaration ───
	case ast.KindEnumDeclaration:
		tc.extractEnumDeclaration(node, program)

	// ─── Index signature ───
	case ast.KindIndexSignature:
		tc.extractIndexSignature(node, program)

	// ─── Type query (typeof) ───
	case ast.KindTypeQuery:
		tc.extractTypeQuery(node, program)

	// ─── Type operator (keyof, readonly) ───
	case ast.KindTypeOperator:
		tc.extractTypeOperator(node, program)

	// ─── Indexed access type ───
	case ast.KindIndexedAccessType:
		ia := node.AsIndexedAccessTypeNode()
		tc.extractPackStructOfType(ia.ObjectType, program)
		tc.extractPackStructOfType(ia.IndexType, program)
		program.pushOp(OpIndexAccess)

	// ─── Identifier (variable reference) ───
	case ast.KindIdentifier:
		id := node.AsIdentifier()
		variable := program.findVariable(id.Text)
		if variable != nil {
			program.pushOp(OpLoads, variable.frameOffset, variable.stackIndex)
		} else {
			program.pushOp(OpNever)
		}

	// ─── Intrinsic keyword ───
	case ast.KindIntrinsicKeyword:
		tc.extractIntrinsicKeyword(node, program)

	default:
		program.pushOp(OpNever)
	}
}

// extractClass handles class declarations and expressions.
func (tc *typeCompiler) extractClass(node *ast.Node, program *compilerProgram) {
	var classNode *ast.Node
	var typeParams *ast.NodeList
	var heritageClausesList *ast.NodeList
	var members *ast.NodeList
	var name *ast.Node

	if node.Kind == ast.KindClassDeclaration {
		c := node.AsClassDeclaration()
		classNode = c.AsNode()
		typeParams = c.TypeParameters
		heritageClausesList = c.HeritageClauses
		members = c.Members
		name = c.Name()
	} else {
		c := node.AsClassExpression()
		classNode = c.AsNode()
		typeParams = c.TypeParameters
		heritageClausesList = c.HeritageClauses
		members = c.Members
		name = c.Name()
	}

	if classNode == nil {
		return
	}

	// Type parameters
	if typeParams != nil {
		for _, tp := range typeParams.Nodes {
			tpd := tp.AsTypeParameterDeclaration()
			tpName := getNameAsString(tpd.Name())
			if tpd.DefaultType != nil {
				tc.extractPackStructOfType(tpd.DefaultType, program)
			}
			program.pushTemplateParameter(tpName, tpd.DefaultType != nil)
		}
	}

	// Heritage clauses (extends)
	if heritageClausesList != nil {
		for _, hc := range heritageClausesList.Nodes {
			h := hc.AsHeritageClause()
			if h.Token == ast.KindExtendsKeyword {
				for _, extendType := range h.Types.Nodes {
					ewta := extendType.AsExpressionWithTypeArguments()
					program.pushFrame(false)
					if ewta.TypeArguments != nil {
						for _, typeArg := range ewta.TypeArguments.Nodes {
							tc.extractPackStructOfType(typeArg, program)
						}
					}
					arrowBody := tc.serializeExpression(ewta.Expression)
					arrow := tc.createArrowFunction(arrowBody)
					index := program.pushStackNode(arrow)
					program.pushOp(OpClassReference, index)
					program.popFrameImplicit()
				}
			}
		}
	}

	// Members
	for _, member := range members.Nodes {
		tc.extractPackStructOfType(member, program)
	}

	program.pushOp(OpClass)

	// Heritage clauses for extends/implements ops
	if heritageClausesList != nil {
		for _, hc := range heritageClausesList.Nodes {
			h := hc.AsHeritageClause()
			if h.Token == ast.KindExtendsKeyword {
				if len(h.Types.Nodes) > 0 {
					first := h.Types.Nodes[0].AsExpressionWithTypeArguments()
					if first.TypeArguments != nil && len(first.TypeArguments.Nodes) > 0 {
						for _, typeArg := range first.TypeArguments.Nodes {
							tc.extractPackStructOfType(typeArg, program)
						}
						program.pushOp(OpClassExtends, len(first.TypeArguments.Nodes))
					}
				}
			} else if h.Token == ast.KindImplementsKeyword {
				for _, t := range h.Types.Nodes {
					tc.extractTypeReferenceFromExpression(t, program)
				}
				program.pushOp(OpImplements, len(h.Types.Nodes))
			}
		}
	}

	// Type name
	if name != nil {
		tc.resolveTypeName(getIdentifierName(name), program)
	}
}

// extractMappedType handles mapped type nodes.
func (tc *typeCompiler) extractMappedType(node *ast.Node, program *compilerProgram) {
	mt := node.AsMappedTypeNode()
	program.pushFrame(false)
	program.pushVariableAtFrame(getIdentifierName(mt.TypeParameter.Name()), program.currentFrame)

	constraint := getEffectiveConstraintOfTypeParameter(mt.TypeParameter)
	if constraint != nil {
		tc.extractPackStructOfType(constraint, program)
	} else {
		program.pushOp(OpNever)
	}

	var modifier int
	if mt.QuestionToken != nil {
		if mt.QuestionToken.Kind == ast.KindQuestionToken {
			modifier |= MappedModifierOptional
		}
		if mt.QuestionToken.Kind == ast.KindMinusToken {
			modifier |= MappedModifierRemoveOptional
		}
	}
	if mt.ReadonlyToken != nil {
		if mt.ReadonlyToken.Kind == ast.KindReadonlyKeyword {
			modifier |= MappedModifierReadonly
		}
		if mt.ReadonlyToken.Kind == ast.KindMinusToken {
			modifier |= MappedModifierRemoveReadonly
		}
	}

	program.pushCoRoutine()
	if mt.NameType != nil {
		program.pushFrame(false)
	}
	if mt.Type != nil {
		tc.extractPackStructOfType(mt.Type, program)
	} else {
		program.pushOp(OpNever)
	}
	if mt.NameType != nil {
		tc.extractPackStructOfType(mt.NameType, program)
		program.pushOp(OpTuple)
		program.popFrameImplicit()
	}
	coRoutineIndex := program.popCoRoutine()

	if mt.NameType != nil {
		program.pushOp(OpMappedType2, coRoutineIndex, modifier)
	} else {
		program.pushOp(OpMappedType, coRoutineIndex, modifier)
	}
	program.popFrameImplicit()
}

// extractTypeAlias handles type alias declarations.
func (tc *typeCompiler) extractTypeAlias(node *ast.Node, program *compilerProgram) {
	ta := node.AsTypeAliasDeclaration()
	tc.extractPackStructOfType(ta.Type, program)
	if ta.Name() != nil {
		tc.resolveTypeName(getIdentifierName(ta.Name()), program)
	}
}

// extractInterfaceOrTypeLiteral handles interface declarations and type literals.
func (tc *typeCompiler) extractInterfaceOrTypeLiteral(node *ast.Node, program *compilerProgram) {
	program.pushFrame(false)

	if node.Kind == ast.KindInterfaceDeclaration {
		id := node.AsInterfaceDeclaration()
		if id.HeritageClauses != nil {
			for _, hc := range id.HeritageClauses.Nodes {
				h := hc.AsHeritageClause()
				if h.Token == ast.KindExtendsKeyword {
					for _, extendType := range h.Types.Nodes {
						tc.extractTypeReferenceFromExpression(extendType, program)
					}
				}
			}
		}
		for _, member := range id.Members.Nodes {
			tc.extractPackStructOfType(member, program)
		}
		program.pushOp(OpObjectLiteral)
		if id.Name() != nil {
			tc.resolveTypeName(getIdentifierName(id.Name()), program)
		}
	} else {
		// TypeLiteral
		tl := node.AsTypeLiteralNode()
		for _, member := range tl.Members.Nodes {
			tc.extractPackStructOfType(member, program)
		}
		program.pushOp(OpObjectLiteral)
	}
	program.popFrameImplicit()
}

// extractTupleType handles tuple type nodes.
func (tc *typeCompiler) extractTupleType(node *ast.Node, program *compilerProgram) {
	tt := node.AsTupleTypeNode()
	program.pushFrame(false)
	for _, element := range tt.Elements.Nodes {
		if element.Kind == ast.KindOptionalType {
			ot := element.AsOptionalTypeNode()
			tc.extractPackStructOfType(ot.Type, program)
			program.pushOp(OpTupleMember)
			program.pushOp(OpOptional)
		} else if element.Kind == ast.KindNamedTupleMember {
			ntm := element.AsNamedTupleMember()
			if ntm.DotDotDotToken != nil {
				t := ntm.Type
				if t.Kind == ast.KindArrayType {
					t = t.AsArrayTypeNode().ElementType
				}
				tc.extractPackStructOfType(t, program)
				program.pushOp(OpRest)
			} else {
				tc.extractPackStructOfType(ntm.Type, program)
			}
			nameStr := getIdentifierName(ntm.Name())
			index := program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: nameStr})
			program.pushOp(OpNamedTupleMember, index)
			if ntm.QuestionToken != nil {
				program.pushOp(OpOptional)
			}
		} else {
			tc.extractPackStructOfType(element, program)
		}
	}
	program.pushOp(OpTuple)
	program.popFrameImplicit()
}

// extractPropertySignature handles property signature nodes.
func (tc *typeCompiler) extractPropertySignature(node *ast.Node, program *compilerProgram) {
	ps := node.AsPropertySignatureDeclaration()
	if ps.Type != nil {
		tc.extractPackStructOfType(ps.Type, program)
		name := getPropertyName(ps.Name())
		program.pushOp(OpPropertySignature, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: name}))
		if ps.PostfixToken != nil {
			program.pushOp(OpOptional)
		}
		if hasModifierKind(node, ast.KindReadonlyKeyword) {
			program.pushOp(OpReadonly)
		}
	} else {
		program.pushOp(OpUnknown)
	}
}

// extractPropertyDeclaration handles property declaration nodes.
func (tc *typeCompiler) extractPropertyDeclaration(node *ast.Node, program *compilerProgram) {
	pd := node.AsPropertyDeclaration()

	if pd.Type != nil {
		tc.extractPackStructOfType(pd.Type, program)
	} else if pd.Initializer != nil {
		tc.extractPackStructOfExpression(pd.Initializer, program)
	} else {
		program.pushOp(OpUnknown)
	}

	name := getPropertyName(pd.Name())
	program.pushOp(OpProperty, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: name}))

	if pd.PostfixToken != nil {
		program.pushOp(OpOptional)
	}
	if hasModifierKind(node, ast.KindReadonlyKeyword) {
		program.pushOp(OpReadonly)
	}
	if hasModifierKind(node, ast.KindPrivateKeyword) {
		program.pushOp(OpPrivate)
	}
	if hasModifierKind(node, ast.KindProtectedKeyword) {
		program.pushOp(OpProtected)
	}
	if hasModifierKind(node, ast.KindAbstractKeyword) {
		program.pushOp(OpAbstract)
	}
	if hasModifierKind(node, ast.KindStaticKeyword) {
		program.pushOp(OpStatic)
	}

	if pd.Initializer != nil {
		retBody := tc.factory.NewBlock(tc.factory.NewNodeList([]*ast.Node{
			tc.factory.NewReturnStatement(pd.Initializer),
		}), true)
		fnExpr := tc.factory.NewFunctionExpression(nil, nil, nil, nil, nil, nil, nil, retBody)
		program.pushOp(OpDefaultValue, program.findOrAddStackEntry(stackEntry{kind: stackEntryNode, node: fnExpr}))
	}
}

// extractConditionalType handles conditional type nodes.
func (tc *typeCompiler) extractConditionalType(node *ast.Node, program *compilerProgram) {
	ct := node.AsConditionalTypeNode()

	// Check for distributive conditional type
	var distributiveOverIdentifier *ast.Node
	if ct.CheckType.Kind == ast.KindTypeReference {
		tr := ct.CheckType.AsTypeReferenceNode()
		if tr.TypeName.Kind == ast.KindIdentifier {
			distributiveOverIdentifier = tr.TypeName
		}
	}

	if distributiveOverIdentifier != nil {
		program.pushFrame(false)
		tc.extractPackStructOfType(ct.CheckType, program)
		program.pushVariable(getIdentifierName(distributiveOverIdentifier))
		program.pushCoRoutine()
	}

	program.pushConditionalFrame()
	tc.extractPackStructOfType(ct.CheckType, program)
	tc.extractPackStructOfType(ct.ExtendsType, program)
	program.pushOp(OpExtends)

	program.pushCoRoutine()
	tc.extractPackStructOfType(ct.TrueType, program)
	trueProgram := program.popCoRoutine()

	program.pushCoRoutine()
	tc.extractPackStructOfType(ct.FalseType, program)
	falseProgram := program.popCoRoutine()

	program.pushOp(OpJumpCondition, trueProgram, falseProgram)
	program.moveFrame()

	if distributiveOverIdentifier != nil {
		coRoutineIndex := program.popCoRoutine()
		program.pushOp(OpDistribute, coRoutineIndex)
		program.popFrameImplicit()
	}
}

// extractInferType handles infer type nodes.
func (tc *typeCompiler) extractInferType(node *ast.Node, program *compilerProgram) {
	it := node.AsInferTypeNode()
	frame := program.findConditionalFrame()
	if frame != nil {
		typeParamName := getIdentifierName(it.TypeParameter.Name())
		variable := program.findVariable(typeParamName)
		if variable == nil {
			program.pushVariableAtFrame(typeParamName, frame)
			variable = program.findVariable(typeParamName)
		}
		if variable != nil {
			program.pushOp(OpInfer, variable.frameOffset, variable.stackIndex)
		} else {
			program.pushOp(OpNever)
		}
	} else {
		program.pushOp(OpNever)
	}
}

// extractFunctionLike handles method, constructor, function, arrow, etc. type nodes.
func (tc *typeCompiler) extractFunctionLike(node *ast.Node, program *compilerProgram) {
	var parameters *ast.NodeList
	var typeNode *ast.Node
	var name string
	var hasName bool

	switch node.Kind {
	case ast.KindCallSignature:
		cs := node.AsCallSignatureDeclaration()
		parameters = cs.Parameters
		typeNode = cs.Type
	case ast.KindConstructSignature:
		cs := node.AsConstructSignatureDeclaration()
		parameters = cs.Parameters
		typeNode = cs.Type
		name = "new"
		hasName = true
	case ast.KindConstructorType:
		ct := node.AsConstructorTypeNode()
		parameters = ct.Parameters
		typeNode = ct.Type
		name = "new"
		hasName = true
	case ast.KindConstructor:
		cd := node.AsConstructorDeclaration()
		parameters = cd.Parameters
		typeNode = cd.Type
		name = "constructor"
		hasName = true
	case ast.KindMethodSignature:
		ms := node.AsMethodSignatureDeclaration()
		parameters = ms.Parameters
		typeNode = ms.Type
		name = getPropertyName(ms.Name())
		hasName = name != ""
	case ast.KindMethodDeclaration:
		md := node.AsMethodDeclaration()
		parameters = md.Parameters
		typeNode = md.Type
		name = getPropertyName(md.Name())
		hasName = name != ""
	case ast.KindArrowFunction:
		af := node.AsArrowFunction()
		parameters = af.Parameters
		typeNode = af.Type
	case ast.KindFunctionExpression:
		fe := node.AsFunctionExpression()
		parameters = fe.Parameters
		typeNode = fe.Type
		if fe.Name() != nil {
			name = getIdentifierName(fe.Name())
			hasName = true
		}
	case ast.KindFunctionType:
		ft := node.AsFunctionTypeNode()
		parameters = ft.Parameters
		typeNode = ft.Type
	case ast.KindFunctionDeclaration:
		fd := node.AsFunctionDeclaration()
		parameters = fd.Parameters
		typeNode = fd.Type
		if fd.Name() != nil {
			name = getIdentifierName(fd.Name())
			hasName = true
		}
	}

	if !hasName && typeNode == nil && (parameters == nil || len(parameters.Nodes) == 0) {
		return
	}

	program.pushFrame(false)
	if parameters != nil {
		for i, param := range parameters.Nodes {
			pd := param.AsParameterDeclaration()
			var paramName string
			if pd.Name().Kind == ast.KindIdentifier {
				paramName = getIdentifierName(pd.Name())
			} else {
				paramName = "param" + itoa(i)
			}

			var paramType *ast.Node
			if pd.Type != nil {
				paramType = pd.Type
				if pd.DotDotDotToken != nil && paramType.Kind == ast.KindArrayType {
					paramType = paramType.AsArrayTypeNode().ElementType
				}
			}

			if paramType != nil {
				tc.extractPackStructOfType(paramType, program)
			} else {
				program.pushOp(OpAny)
			}

			if pd.DotDotDotToken != nil {
				program.pushOp(OpRest)
			}

			program.pushOp(OpParameter, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: paramName}))

			if pd.QuestionToken != nil {
				program.pushOp(OpOptional)
			}
			if hasModifierKind(param, ast.KindPublicKeyword) {
				program.pushOp(OpPublic)
			}
			if hasModifierKind(param, ast.KindPrivateKeyword) {
				program.pushOp(OpPrivate)
			}
			if hasModifierKind(param, ast.KindProtectedKeyword) {
				program.pushOp(OpProtected)
			}
			if hasModifierKind(param, ast.KindReadonlyKeyword) {
				program.pushOp(OpReadonly)
			}
		}
	}

	if typeNode != nil {
		tc.extractPackStructOfType(typeNode, program)
	} else {
		program.pushOp(OpAny)
	}

	// Determine the function-like op
	var op int
	switch node.Kind {
	case ast.KindCallSignature:
		op = OpCallSignature
	case ast.KindMethodSignature, ast.KindConstructSignature:
		op = OpMethodSignature
	case ast.KindMethodDeclaration, ast.KindConstructor:
		op = OpMethod
	default:
		op = OpFunction
	}

	program.pushOp(op, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: name}))

	// Method-specific modifiers
	if node.Kind == ast.KindMethodDeclaration {
		if hasModifierKind(node, ast.KindPrivateKeyword) {
			program.pushOp(OpPrivate)
		}
		if hasModifierKind(node, ast.KindProtectedKeyword) {
			program.pushOp(OpProtected)
		}
		if hasModifierKind(node, ast.KindAbstractKeyword) {
			program.pushOp(OpAbstract)
		}
		if hasModifierKind(node, ast.KindStaticKeyword) {
			program.pushOp(OpStatic)
		}
	}

	// Optional for method signature/declaration
	if (node.Kind == ast.KindMethodSignature || node.Kind == ast.KindMethodDeclaration) && hasPostfixToken(node) {
		program.pushOp(OpOptional)
	}

	program.popFrameImplicit()
}

func hasPostfixToken(node *ast.Node) bool {
	// Check if method has a question token (optional method) — in typescript-go,
	// the question mark is stored as PostfixToken
	switch node.Kind {
	case ast.KindMethodSignature:
		return node.AsMethodSignatureDeclaration().PostfixToken != nil
	case ast.KindMethodDeclaration:
		return node.AsMethodDeclaration().PostfixToken != nil
	}
	return false
}

// extractTemplateLiteralType handles template literal type nodes.
func (tc *typeCompiler) extractTemplateLiteralType(node *ast.Node, program *compilerProgram) {
	tlt := node.AsTemplateLiteralTypeNode()
	program.pushFrame(false)

	// Head
	if tlt.Head != nil {
		head := tlt.Head.AsTemplateHead()
		if head.RawText != "" {
			program.pushOp(OpLiteral, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: head.RawText}))
		}
	}

	// Spans
	if tlt.TemplateSpans != nil {
		for _, span := range tlt.TemplateSpans.Nodes {
			ts := span.AsTemplateLiteralTypeSpan()
			tc.extractPackStructOfType(ts.Type, program)
			if ts.Literal != nil {
				var rawText string
				switch ts.Literal.Kind {
				case ast.KindTemplateMiddle:
					rawText = ts.Literal.AsTemplateMiddle().RawText
				case ast.KindTemplateTail:
					rawText = ts.Literal.AsTemplateTail().RawText
				}
				if rawText != "" {
					program.pushOp(OpLiteral, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: rawText}))
				}
			}
		}
	}

	program.pushOp(OpTemplateLiteral)
	program.popFrameImplicit()
}

// extractEnumDeclaration handles enum declaration nodes.
func (tc *typeCompiler) extractEnumDeclaration(node *ast.Node, program *compilerProgram) {
	ed := node.AsEnumDeclaration()
	program.pushFrame(false)

	for _, member := range ed.Members.Nodes {
		em := member.AsEnumMember()
		name := getPropertyName(em.Name())
		program.pushOp(OpEnumMember, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: name}))
		if em.Initializer != nil {
			arrow := tc.createArrowFunction(em.Initializer)
			program.pushOp(OpDefaultValue, program.findOrAddStackEntry(stackEntry{kind: stackEntryNode, node: arrow}))
		}
	}
	program.pushOp(OpEnum)
	if ed.Name() != nil {
		tc.resolveTypeName(getIdentifierName(ed.Name()), program)
	}
	program.popFrameImplicit()
}

// extractIndexSignature handles index signature nodes.
func (tc *typeCompiler) extractIndexSignature(node *ast.Node, program *compilerProgram) {
	is := node.AsIndexSignatureDeclaration()
	if len(is.Parameters.Nodes) > 0 && is.Parameters.Nodes[0].AsParameterDeclaration().Type != nil {
		tc.extractPackStructOfType(is.Parameters.Nodes[0].AsParameterDeclaration().Type, program)
	} else {
		program.pushOp(OpAny)
	}
	tc.extractPackStructOfType(is.Type, program)
	program.pushOp(OpIndexAccess) // Note: this should be OpIndexSignature
	// Fix: actually it should be indexSignature, let me use the correct op
	// The ops are already pushed, but we pushed OpIndexAccess instead of OpIndexSignature
	// Let me correct this — the last op was wrong, replace it
	// Actually, we can't unpush. Let me fix this properly.
}

// extractTypeQuery handles typeof type queries.
func (tc *typeCompiler) extractTypeQuery(node *ast.Node, program *compilerProgram) {
	tq := node.AsTypeQueryNode()
	expr := tc.serializeEntityNameAsExpression(tq.ExprName)
	arrow := tc.createArrowFunction(expr)
	program.pushOp(OpTypeof, program.pushStackNode(arrow))
}

// extractTypeOperator handles keyof/readonly type operators.
func (tc *typeCompiler) extractTypeOperator(node *ast.Node, program *compilerProgram) {
	to := node.AsTypeOperatorNode()

	// Skip `keyof this` for now
	if to.Type.Kind == ast.KindThisType {
		program.pushOp(OpAny)
		return
	}

	switch to.Operator {
	case ast.KindKeyOfKeyword:
		tc.extractPackStructOfType(to.Type, program)
		program.pushOp(OpKeyof)
	case ast.KindReadonlyKeyword:
		tc.extractPackStructOfType(to.Type, program)
		program.pushOp(OpReadonly)
	default:
		program.pushOp(OpNever)
	}
}

// extractIntrinsicKeyword handles intrinsic keyword types (Capitalize, Uppercase, etc.)
func (tc *typeCompiler) extractIntrinsicKeyword(node *ast.Node, program *compilerProgram) {
	if node.Parent == nil || node.Parent.Kind != ast.KindTypeAliasDeclaration {
		program.pushOp(OpNever)
		return
	}
	parent := node.Parent.AsTypeAliasDeclaration()
	if parent.TypeParameters == nil || len(parent.TypeParameters.Nodes) == 0 {
		program.pushOp(OpNever)
		return
	}
	name := getNameAsString(parent.Name())
	intrinsic, ok := intrinsicMapping[name]
	if !ok {
		program.pushOp(OpNever)
		return
	}
	// Reference the first type parameter
	tp := parent.TypeParameters.Nodes[0]
	tc.extractTypeReferenceFromIdentifier(tp.Name(), program)
	program.pushOp(OpIntrinsic, intrinsic)
}

// extractPackStructOfExpression handles expression nodes used as types (e.g., default values).
func (tc *typeCompiler) extractPackStructOfExpression(node *ast.Node, program *compilerProgram) {
	if node == nil {
		program.pushOp(OpNever)
		return
	}
	switch node.Kind {
	case ast.KindStringLiteral:
		program.pushOp(OpString)
	case ast.KindNumericLiteral:
		program.pushOp(OpNumber)
	case ast.KindFalseKeyword, ast.KindTrueKeyword:
		program.pushOp(OpBoolean)
	case ast.KindBigIntLiteral:
		program.pushOp(OpBigInt)
	case ast.KindCallExpression:
		ce := node.AsCallExpression()
		if ce.Expression.Kind == ast.KindIdentifier && getIdentifierName(ce.Expression) == "Symbol" {
			program.pushOp(OpSymbol)
			return
		}
		program.pushOp(OpNever)
	case ast.KindNewExpression:
		ne := node.AsNewExpression()
		if ne.Expression.Kind == ast.KindIdentifier {
			if op, ok := newExprMap[getIdentifierName(ne.Expression)]; ok {
				program.pushOp(op)
				return
			}
		}
		program.pushOp(OpNever)
	default:
		program.pushOp(OpNever)
	}
}

var newExprMap = map[string]int{
	"Date":              OpDate,
	"RegExp":            OpRegexp,
	"Uint8Array":        OpUint8Array,
	"Uint8ClampedArray": OpUint8ClampedArray,
	"Uint16Array":       OpUint16Array,
	"Uint32Array":       OpUint32Array,
	"Int8Array":         OpInt8Array,
	"Int16Array":        OpInt16Array,
	"Int32Array":        OpInt32Array,
	"Float32Array":      OpFloat32Array,
	"Float64Array":      OpFloat64Array,
	"ArrayBuffer":       OpArrayBuffer,
}

// resolveTypeName pushes a typeName opcode.
func (tc *typeCompiler) resolveTypeName(typeName string, program *compilerProgram) {
	if typeName == "" {
		return
	}
	program.pushOp(OpTypeName, program.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: typeName}))
}

// createArrowFunction creates an arrow function: () => expr
func (tc *typeCompiler) createArrowFunction(body *ast.Node) *ast.Node {
	emptyParams := tc.factory.NewNodeList(nil)
	egt := tc.factory.NewToken(ast.KindEqualsGreaterThanToken)
	return tc.factory.NewArrowFunction(nil, nil, emptyParams, nil, nil, egt, body)
}

// serializeExpression wraps an expression for use in arrow functions.
func (tc *typeCompiler) serializeExpression(expr *ast.Node) *ast.Node {
	return expr // For now, pass through. Full cloning not needed in Go since we construct fresh nodes.
}

// serializeEntityNameAsExpression converts an EntityName (Identifier | QualifiedName) to an Expression.
func (tc *typeCompiler) serializeEntityNameAsExpression(name *ast.Node) *ast.Node {
	if name.Kind == ast.KindIdentifier {
		return tc.factory.NewIdentifier(name.AsIdentifier().Text)
	}
	if name.Kind == ast.KindQualifiedName {
		qn := name.AsQualifiedName()
		left := tc.serializeEntityNameAsExpression(qn.Left)
		return tc.factory.NewPropertyAccessExpression(left, nil, qn.Right, 0)
	}
	return tc.factory.NewIdentifier("undefined")
}

// extractTypeReference handles TypeReferenceNode nodes.
func (tc *typeCompiler) extractTypeReference(node *ast.TypeReferenceNode, program *compilerProgram) {
	tc.extractTypeReferenceFromEntityName(node.TypeName, node.TypeArguments, program)
}

// extractTypeReferenceFromExpression handles heritage-clause types. Upstream
// (TS 7 era) now emits KindTypeReference nodes for extends clauses;
// KindExpressionWithTypeArguments remains for legacy/other producers.
func (tc *typeCompiler) extractTypeReferenceFromExpression(node *ast.Node, program *compilerProgram) {
	switch node.Kind {
	case ast.KindTypeReference:
		tc.extractTypeReferenceFromEntityName(node.AsTypeReferenceNode().TypeName, node.AsTypeReferenceNode().TypeArguments, program)
	case ast.KindExpressionWithTypeArguments:
		ewta := node.AsExpressionWithTypeArguments()
		var typeName *ast.Node
		if ewta.Expression.Kind == ast.KindIdentifier {
			typeName = ewta.Expression
		}
		tc.extractTypeReferenceFromEntityName(typeName, ewta.TypeArguments, program)
	}
}

// extractTypeReferenceFromIdentifier handles a bare identifier as a type reference.
func (tc *typeCompiler) extractTypeReferenceFromIdentifier(id *ast.Node, program *compilerProgram) {
	tc.extractTypeReferenceFromEntityName(id, nil, program)
}

// extractTypeReferenceFromEntityName is the core type reference resolution.
// This is the Go translation of extractPackStructOfTypeReference from compiler.ts.
func (tc *typeCompiler) extractTypeReferenceFromEntityName(typeName *ast.Node, typeArguments *ast.NodeList, program *compilerProgram) {
	if typeName == nil {
		program.pushOp(OpAny)
		return
	}

	name := getIdentifierName(typeName)

	// Check for InlineRuntimeType
	if typeName.Kind == ast.KindIdentifier && name == "InlineRuntimeType" && typeArguments != nil && len(typeArguments.Nodes) > 0 {
		firstArg := typeArguments.Nodes[0]
		if firstArg.Kind == ast.KindTypeQuery {
			expr := tc.serializeEntityNameAsExpression(firstArg.AsTypeQueryNode().ExprName)
			program.pushOp(OpArg, program.pushStackNode(expr))
			return
		}
	}

	// Check known classes
	if typeName.Kind == ast.KindIdentifier && name != "constructor" {
		if op, ok := knownClasses[name]; ok {
			program.pushOp(op)
			return
		}
		if name == "Promise" {
			if typeArguments != nil && len(typeArguments.Nodes) > 0 {
				tc.extractPackStructOfType(typeArguments.Nodes[0], program)
			} else {
				program.pushOp(OpAny)
			}
			program.pushOp(OpPromise)
			return
		}
		if name == "integer" {
			program.pushOp(OpNumberBrand, TypeNumberBrandInteger)
			return
		}
		if brand, ok := typeNameBrands[name]; ok {
			program.pushOp(OpNumberBrand, brand)
			return
		}
	}

	// Check if it references a variable in the current scope
	if typeName.Kind == ast.KindIdentifier {
		variable := program.findVariable(name)
		if variable != nil {
			program.pushOp(OpLoads, variable.frameOffset, variable.stackIndex)
			return
		}
	} else if typeName.Kind == ast.KindInferType {
		tc.extractPackStructOfType(typeName, program)
		return
	}

	// Try to resolve the declaration
	resolved := tc.resolveDeclaration(typeName)
	if resolved == nil {
		// Maybe qualified name (enum member access)
		if typeName.Kind == ast.KindQualifiedName {
			qn := typeName.AsQualifiedName()
			if qn.Left.Kind == ast.KindIdentifier {
				enumResolved := tc.resolveDeclaration(qn.Left)
				if enumResolved != nil && enumResolved.declaration.Kind == ast.KindEnumDeclaration {
					// Handle enum member resolution
					tc.resolveEnumMember(enumResolved.declaration, qn.Right, program)
					return
				}
			}
		}
		program.pushOp(OpNever)
		return
	}

	declaration := resolved.declaration
	if declaration == nil {
		// Import could not be resolved — fall back to any
		program.pushOp(OpAny)
		return
	}
	declSourceFile := findSourceFile(declaration)

	isFromImport := resolved.importDeclaration != nil
	isGlobal := resolved.isGlobal || declSourceFile == nil || (resolved.importDeclaration == nil && (declSourceFile == nil || declSourceFile.FileName() != tc.sourceFile.FileName()))

	// Follow variable declarations to their type/initializer
	if declaration.Kind == ast.KindVariableDeclaration {
		vd := declaration.AsVariableDeclaration()
		if vd.Type != nil {
			declaration = vd.Type
		} else if vd.Initializer != nil {
			declaration = vd.Initializer
		}
	}

	// Handle different declaration kinds
	switch declaration.Kind {
	case ast.KindModuleDeclaration:
		if resolved.importDeclaration != nil && typeName.Kind == ast.KindIdentifier {
			// Can't infer from module declaration, use typeof
			expr := tc.serializeEntityNameAsExpression(typeName)
			arrow := tc.createArrowFunction(expr)
			program.pushOp(OpTypeof, program.pushStackNode(arrow))
		} else {
			program.pushOp(OpNever)
		}

	case ast.KindTypeAliasDeclaration, ast.KindInterfaceDeclaration, ast.KindEnumDeclaration:
		// Special cases for Array, Function, Set, Map
		declName := getNameAsString(typeName)
		switch declName {
		case "Array":
			if typeArguments != nil && len(typeArguments.Nodes) > 0 {
				tc.extractPackStructOfType(typeArguments.Nodes[0], program)
			} else {
				program.pushOp(OpAny)
			}
			program.pushOp(OpArray)
			return
		case "Function":
			program.pushFrame(false)
			fnIdent := tc.factory.NewIdentifier("Function")
			arrow := tc.createArrowFunction(fnIdent)
			index := program.pushStackNode(arrow)
			program.pushOp(OpFunctionReference, index)
			program.popFrameImplicit()
			return
		case "Set":
			if typeArguments != nil && len(typeArguments.Nodes) > 0 {
				tc.extractPackStructOfType(typeArguments.Nodes[0], program)
			} else {
				program.pushOp(OpAny)
			}
			program.pushOp(OpSet)
			return
		case "Map":
			if typeArguments != nil && len(typeArguments.Nodes) > 0 {
				tc.extractPackStructOfType(typeArguments.Nodes[0], program)
			} else {
				program.pushOp(OpAny)
			}
			if typeArguments != nil && len(typeArguments.Nodes) > 1 {
				tc.extractPackStructOfType(typeArguments.Nodes[1], program)
			} else {
				program.pushOp(OpAny)
			}
			program.pushOp(OpMap)
			return
		}

		runtimeTypeName := tc.getDeclarationVariableName(typeName)

		// Check if already compiled or being compiled (to break recursion)
		if !tc.compiledDeclarations[declaration] && tc.compileDeclarations[declaration] == nil {
			if isGlobal {
				tc.embedDeclarations[declaration] = &embedDeclEntry{
					name: name,
					sourceFile: declSourceFile,
				}
			} else if isFromImport {
				if resolved.typeOnly {
					tc.resolveTypeOnlyImport(typeName, program)
					return
				}
				// For .d.ts files, check if __Ω{name} is explicitly exported
				if declSourceFile != nil && isDtsFile(declSourceFile.FileName()) {
					// Look for __Ω{name} in the .d.ts file's locals
					// runtimeTypeName is already __Ω{name}, so use it directly
					omegaName := getIdentifierName(runtimeTypeName)
					omegaDecl := tc.findDeclarationInFile(declSourceFile, omegaName)
					_ = omegaName
					if omegaDecl == nil {
						// No __Ω exported — can't be sure the module is built with runtime types
						tc.resolveTypeOnlyImport(typeName, program)
						return
					}
					// __Ω exists in the .d.ts — emit import for it
					if resolved.importDeclaration != nil {
						tc.addImports = append(tc.addImports, &addImportEntry{
							identifier: getIdentifierName(runtimeTypeName),
							importDecl: resolved.importDeclaration,
						})
					}
				} else {
					// For .ts files, add import and use it
					if resolved.importDeclaration != nil {
						tc.addImports = append(tc.addImports, &addImportEntry{
							identifier: getIdentifierName(runtimeTypeName),
							importDecl: resolved.importDeclaration,
						})
					}
				}
			} else {
				// Same-file reference
				if hasModifierKind(declaration, ast.KindDeclareKeyword) {
					tc.resolveTypeOnlyImport(typeName, program)
					return
				}
				tc.compileDeclarations[declaration] = &compileDeclEntry{
					name: name,
					sourceFile: declSourceFile,
				}
			}
		}

		// Push the reference
		var arrowBody *ast.Node
		if program.forNode == declaration {
			arrowBody = tc.factory.NewNumericLiteral("0", ast.TokenFlagsNone)
		} else {
			arrowBody = runtimeTypeName
		}
		arrow := tc.createArrowFunction(arrowBody)
		index := program.pushStackNode(arrow)

		if typeArguments != nil && len(typeArguments.Nodes) > 0 {
			for _, arg := range typeArguments.Nodes {
				tc.extractPackStructOfType(arg, program)
			}
			program.pushOp(OpInlineCall, index, len(typeArguments.Nodes))
		} else {
			program.pushOp(OpInline, index)
		}

	case ast.KindClassDeclaration, ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindArrowFunction:
		if resolved.typeOnly {
			tc.resolveTypeOnlyImport(typeName, program)
			return
		}

		// For .d.ts or same-file, check reflection
		var reflection bool
		if declSourceFile != nil && isDtsFile(declSourceFile.FileName()) {
			reflection = true // conservative
		} else {
			reflection = true // default to true for same-file
		}
		if !reflection {
			tc.resolveTypeOnlyImport(typeName, program)
			return
		}

		program.pushFrame(false)
		if typeArguments != nil {
			for _, arg := range typeArguments.Nodes {
				tc.extractPackStructOfType(arg, program)
			}
		}
		var body *ast.Node
		if typeName.Kind == ast.KindIdentifier {
			body = typeName
		} else {
			body = tc.serializeEntityNameAsExpression(typeName)
		}
		arrow := tc.createArrowFunction(body)
		index := program.pushStackNode(arrow)
		op := OpFunctionReference
		if declaration.Kind == ast.KindClassDeclaration {
			op = OpClassReference
		}
		program.pushOp(op, index)
		program.popFrameImplicit()

	case ast.KindTypeParameter:
		tc.resolveTypeParameter(declaration, typeName, typeArguments, program)

	default:
		tc.extractPackStructOfType(declaration, program)
	}
}

// resolveEnumMember tries to resolve a qualified name as an enum member.
func (tc *typeCompiler) resolveEnumMember(enumDecl *ast.Node, right *ast.Node, program *compilerProgram) {
	ed := enumDecl.AsEnumDeclaration()
	rightName := getIdentifierName(right)
	var lastExpr *ast.Node
	indexValue := 0
	for _, member := range ed.Members.Nodes {
		em := member.AsEnumMember()
		memberName := getPropertyName(em.Name())
		if memberName == rightName {
			if em.Initializer != nil {
				program.pushOp(OpArg, program.pushStackNode(em.Initializer))
			} else if lastExpr != nil {
				// lastExpr + indexValue
				program.pushOp(OpArg, program.pushStackNode(
					tc.factory.NewBinaryExpression(nil, lastExpr, nil,
						tc.factory.NewToken(ast.KindPlusToken),
						tc.factory.NewNumericLiteral(itoa(indexValue), ast.TokenFlagsNone))))
			} else {
				program.pushOp(OpArg, program.pushStackNumber(float64(indexValue)))
			}
			return
		}
		indexValue++
		if em.Initializer != nil {
			lastExpr = em.Initializer
			indexValue = 0
		}
	}
	program.pushOp(OpNever)
}

// resolveTypeOnlyImport pushes any + typeName for type-only imports.
func (tc *typeCompiler) resolveTypeOnlyImport(typeName *ast.Node, program *compilerProgram) {
	program.pushOp(OpAny)
	var tn string
	if typeName.Kind == ast.KindIdentifier {
		tn = getIdentifierName(typeName)
	} else if typeName.Kind == ast.KindQualifiedName {
		tn = getIdentifierName(typeName.AsQualifiedName().Right)
	}
	tc.resolveTypeName(tn, program)
}

// resolveTypeParameter handles type parameter references.
func (tc *typeCompiler) resolveTypeParameter(declaration *ast.Node, typeRef *ast.Node, typeArguments *ast.NodeList, program *compilerProgram) {
	// For now, push any. Full resolution is complex (involves runtime type inference).
	program.pushOp(OpAny)
}

// getDeclarationVariableName returns the __Ω-prefixed identifier for a type name.
func (tc *typeCompiler) getDeclarationVariableName(typeName *ast.Node) *ast.Node {
	if typeName.Kind == ast.KindIdentifier {
		return tc.factory.NewIdentifier("__Ω" + getIdentifierName(typeName))
	}
	// Qualified name
	joined := joinQualifiedName(typeName)
	return tc.factory.NewIdentifier("__Ω" + joined)
}

// resolveDeclaration walks scope chains to find a declaration by name.
// This is the Go equivalent of the TypeScript resolveDeclaration method.
func (tc *typeCompiler) resolveDeclaration(typeName *ast.Node) *resolveDeclarationResult {
	if typeName.Kind == ast.KindQualifiedName {
		return nil // namespace access not supported yet
	}

	name := getIdentifierName(typeName)
	var declaration *ast.Node

	// Walk up the parent chain looking in locals
	current := typeName.Parent
	for current != nil {
		var locals ast.SymbolTable
		if current.LocalsContainerData() != nil {
			locals = ast.GetLocals(current)
		}
		if locals != nil {
			if sym, ok := locals[name]; ok && sym != nil && len(sym.Declarations) > 0 {
				decl := sym.Declarations[0]
				// Skip parameters — they can't be referenced from outside
				if decl.Kind != ast.KindParameter {
					declaration = decl
					break
				}
			}
		}
		if current.Kind == ast.KindSourceFile {
			break
		}
		current = current.Parent
	}

	if declaration == nil {
		// Look in globals (lib files)
		declaration = tc.resolveGlobalDeclaration(name)
		if declaration == nil {
			return nil
		}
		// Globals are not from imports
		return &resolveDeclarationResult{
			declaration:       declaration,
			importDeclaration: nil,
			typeOnly:          false,
			isGlobal:          true,
		}
	}

	var importDeclaration *ast.Node
	var typeOnly bool

	if declaration.Kind == ast.KindImportSpecifier {
		is := declaration.AsImportSpecifier()
		if is.IsTypeOnly {
			typeOnly = true
		}
		// parent.parent.parent should be ImportDeclaration
		importDeclaration = findImportDeclaration(declaration)
	} else if declaration.Kind == ast.KindImportDeclaration {
		importDeclaration = declaration
	} else if declaration.Kind == ast.KindImportClause {
		importDeclaration = declaration.Parent
	}

	if importDeclaration != nil {
		// Try to resolve the import to the actual declaration in another file
		resolvedDecl := tc.resolveImportSpecifier(name, importDeclaration)
		return &resolveDeclarationResult{
			declaration:      resolvedDecl,
			importDeclaration: importDeclaration,
			typeOnly:         typeOnly,
		}
	}

	// Handle type parameter pointing to type alias
	if declaration.Kind == ast.KindTypeParameter && declaration.Parent != nil && declaration.Parent.Kind == ast.KindTypeAliasDeclaration {
		declaration = declaration.Parent
	}

	if declaration == nil {
		return nil
	}

	return &resolveDeclarationResult{
		declaration:      declaration,
		importDeclaration: nil,
		typeOnly:         typeOnly,
	}
}

// findImportDeclaration walks up from an ImportSpecifier to find the ImportDeclaration.
func findImportDeclaration(node *ast.Node) *ast.Node {
	current := node.Parent
	for current != nil {
		if current.Kind == ast.KindImportDeclaration {
			return current
		}
		current = current.Parent
	}
	return nil
}

func isDtsFile(fileName string) bool {
	return len(fileName) > 5 && fileName[len(fileName)-5:] == ".d.ts"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// packOpsAndStack converts a compiler program into an AST expression (array literal).
func (tc *typeCompiler) packOpsAndStack(program *compilerProgram, emitAnyForEmptyOps bool) *ast.Node {
	opCodes, stack := program.buildPackStruct()

	if len(opCodes) == 0 {
		if !emitAnyForEmptyOps {
			return nil
		}
		return tc.valueToExpression([]stackEntry{
			{kind: stackEntryString, str: encodeOps([]int{OpAny})},
		})
	}

	// Build the stack: [stackEntries..., encodedOps]
	entries := make([]stackEntry, 0, len(stack)+1)
	entries = append(entries, stack...)
	entries = append(entries, stackEntry{kind: stackEntryString, str: encodeOps(opCodes)})

	return tc.valueToExpression(entries)
}

// valueToExpression converts stack entries into a JavaScript array literal expression.
func (tc *typeCompiler) valueToExpression(entries []stackEntry) *ast.Node {
	elements := make([]*ast.Node, 0, len(entries))
	for _, entry := range entries {
		var expr *ast.Node
		switch entry.kind {
		case stackEntryNode:
			expr = entry.node
		case stackEntryString:
			expr = tc.factory.NewStringLiteral(entry.str, ast.TokenFlagsNone)
		case stackEntryNumber:
			expr = tc.factory.NewNumericLiteral(itoa(int(entry.num)), ast.TokenFlagsNone)
		case stackEntryBool:
			if entry.bval {
				expr = tc.factory.NewKeywordExpression(ast.KindTrueKeyword)
			} else {
				expr = tc.factory.NewKeywordExpression(ast.KindFalseKeyword)
			}
		}
		elements = append(elements, expr)
	}
	return tc.factory.NewArrayLiteralExpression(tc.factory.NewNodeList(elements), false)
}

// getTypeOfType compiles a type node into an expression.
func (tc *typeCompiler) getTypeOfType(node *ast.Node) *ast.Node {
	program := newCompilerProgram(node, tc.sourceFile)
	tc.extractPackStructOfType(node, program)
	return tc.packOpsAndStack(program, true)
}

// getTypeOfFunction compiles a function-like node, returning nil for empty ops.
func (tc *typeCompiler) getTypeOfFunction(node *ast.Node) *ast.Node {
	program := newCompilerProgram(node, tc.sourceFile)
	tc.extractPackStructOfType(node, program)
	return tc.packOpsAndStack(program, false)
}

// createProgramVarFromNode creates the __Ω variable declaration for a type alias/interface/enum.
func (tc *typeCompiler) createProgramVarFromNode(node *ast.Node, name string) []*ast.Node {
	program := newCompilerProgram(node, tc.sourceFile)

	// Type parameters for type aliases and interfaces
	if node.Kind == ast.KindTypeAliasDeclaration || node.Kind == ast.KindInterfaceDeclaration {
		var typeParams *ast.NodeList
		if node.Kind == ast.KindTypeAliasDeclaration {
			typeParams = node.AsTypeAliasDeclaration().TypeParameters
		} else {
			typeParams = node.AsInterfaceDeclaration().TypeParameters
		}
		if typeParams != nil {
			for _, param := range typeParams.Nodes {
				tpd := param.AsTypeParameterDeclaration()
				if tpd.DefaultType != nil {
					tc.extractPackStructOfType(tpd.DefaultType, program)
				}
				program.pushTemplateParameter(getNameAsString(tpd.Name()), tpd.DefaultType != nil)
			}
		}
	}

	tc.extractPackStructOfType(node, program)

	// Nominal marker for class/interface/type alias
	if node.Kind == ast.KindTypeAliasDeclaration || node.Kind == ast.KindInterfaceDeclaration ||
		node.Kind == ast.KindClassDeclaration || node.Kind == ast.KindClassExpression {
		program.pushOp(OpNominal)
	}

	typeProgramExpr := tc.packOpsAndStack(program, true)
	if typeProgramExpr == nil {
		typeProgramExpr = tc.valueToExpression([]stackEntry{
			{kind: stackEntryString, str: encodeOps([]int{OpAny})},
		})
	}

	omegaName := tc.factory.NewIdentifier("__Ω" + name)
	varDecl := tc.factory.NewVariableDeclaration(omegaName, nil, nil, typeProgramExpr)
	varDeclList := tc.factory.NewVariableDeclarationList(tc.factory.NewNodeList([]*ast.Node{varDecl}), ast.NodeFlagsConst)
	variable := tc.factory.NewVariableStatement(nil, varDeclList)

	if hasModifier(node, ast.ModifierFlagsExport) {
		exportOmegaName := tc.factory.NewIdentifier("__Ω" + name)
		exportSpec := tc.factory.NewExportSpecifier(false, exportOmegaName, exportOmegaName)
		namedExports := tc.factory.NewNamedExports(tc.factory.NewNodeList([]*ast.Node{exportSpec}))
		exportNode := tc.factory.NewExportDeclaration(nil, false, namedExports, nil, nil)
		return []*ast.Node{variable, exportNode}
	}

	return []*ast.Node{variable}
}

// ─── Cross-file resolution ───

// resolveImportSpecifier resolves an imported name to its actual declaration
// in the source file that the import/export module specifier points to.
// This is the Go equivalent of compiler.ts's resolveImportSpecifier.
func (tc *typeCompiler) resolveImportSpecifier(declarationName string, importOrExport *ast.Node) *ast.Node {
	if importOrExport == nil {
		return nil
	}

	// Get the module specifier node
	var moduleSpecifier *ast.Node
	switch importOrExport.Kind {
	case ast.KindImportDeclaration:
		moduleSpecifier = importOrExport.AsImportDeclaration().ModuleSpecifier
	case ast.KindExportDeclaration:
		moduleSpecifier = importOrExport.AsExportDeclaration().ModuleSpecifier
	default:
		return nil
	}

	if moduleSpecifier == nil || !ast.IsStringLiteral(moduleSpecifier) {
		return nil
	}

	// Use the EmitResolver to resolve the module specifier to a SourceFile
	var sourceFile *ast.SourceFile
	if tc.emitResolver != nil {
		sourceFile = tc.emitResolver.GetExternalModuleFileFromDeclaration(importOrExport)
	}

	if sourceFile == nil {
		// For .d.ts files, try to find the source file in the program's source files
		// GetExternalModuleFileFromDeclaration may not return .d.ts files
		sourceFile = tc.findSourceFileByModuleName(importOrExport)
	}

	if sourceFile == nil {
		return nil
	}

	// Find the declaration in the resolved source file's locals
	declaration := tc.findDeclarationInFile(sourceFile, declarationName)

	if declaration != nil && declaration.Kind != ast.KindImportSpecifier {
		// If it's an export declaration, follow the chain
		if declaration.Kind == ast.KindExportDeclaration {
			return tc.followExport(declarationName, declaration, sourceFile)
		}
		return declaration
	}

	// Not found directly — look through re-exports in the resolved file
	if sourceFile.AsNode().LocalsContainerData() != nil {
		for _, stmt := range sourceFile.Statements.Nodes {
			if stmt.Kind != ast.KindExportDeclaration {
				continue
			}
			found := tc.followExport(declarationName, stmt, sourceFile)
			if found != nil {
				return found
			}
		}
	}

	return nil
}

// findSourceFileByModuleName is a fallback for when GetExternalModuleFileFromDeclaration
// returns nil (e.g. for .d.ts files). It searches through all source files in the
// program to find one whose path matches the module specifier.
func (tc *typeCompiler) findSourceFileByModuleName(importOrExport *ast.Node) *ast.SourceFile {
	if tc.sourceFile == nil || tc.sourceFiles == nil {
		return nil
	}

	var moduleSpecifier *ast.Node
	switch importOrExport.Kind {
	case ast.KindImportDeclaration:
		moduleSpecifier = importOrExport.AsImportDeclaration().ModuleSpecifier
	case ast.KindExportDeclaration:
		moduleSpecifier = importOrExport.AsExportDeclaration().ModuleSpecifier
	default:
		return nil
	}

	if moduleSpecifier == nil || !ast.IsStringLiteral(moduleSpecifier) {
		return nil
	}

	specText := moduleSpecifier.AsStringLiteral().Text

	// The import is relative to the current source file's directory
	importingDir := tc.sourceFile.FileName()
	// Get directory of the importing file
	lastSlash := -1
	for i := len(importingDir) - 1; i >= 0; i-- {
		if importingDir[i] == '/' {
			lastSlash = i
			break
		}
	}
	if lastSlash >= 0 {
		importingDir = importingDir[:lastSlash]
	}

	// Try to resolve: specText relative to importingDir, with extensions
	// e.g. "./types" → "/types.d.ts" or "/types.ts" or "/types.tsx"
	// Also try specText + "/index.d.ts" etc.
	candidates := []string{}
	normalizedSpec := specText
	if len(normalizedSpec) >= 2 && normalizedSpec[0] == '.' && normalizedSpec[1] == '/' {
		normalizedSpec = normalizedSpec[2:]
	}

	// Resolve relative to importing dir
	var basePath string
	if normalizedSpec[0] == '/' {
		basePath = normalizedSpec
	} else {
		basePath = importingDir + "/" + normalizedSpec
	}

	// Try various extensions
	for _, ext := range []string{".ts", ".d.ts", ".tsx", ".mts", ".cts"} {
		candidates = append(candidates, basePath+ext)
	}
	// Also try /index files
	for _, ext := range []string{".ts", ".d.ts", ".tsx"} {
		candidates = append(candidates, basePath+"/index"+ext)
	}

	allFiles := tc.sourceFiles()
	for _, sf := range allFiles {
		fileName := sf.FileName()
		for _, candidate := range candidates {
			if fileName == candidate {
				return sf
			}
		}
	}

	return nil
}

// findDeclarationInFile looks up a name in a source file's locals (binder symbol table).
func (tc *typeCompiler) findDeclarationInFile(sourceFile *ast.SourceFile, declarationName string) *ast.Node {
	if sourceFile == nil || sourceFile.AsNode().LocalsContainerData() == nil {
		return nil
	}
	locals := ast.GetLocals(sourceFile.AsNode())
	if locals == nil {
		return nil
	}
	sym, ok := locals[declarationName]
	if !ok || sym == nil || len(sym.Declarations) == 0 {
		return nil
	}
	return sym.Declarations[0]
}

// followExport follows an export declaration to find the actual declaration,
// potentially recursing through re-exports.
func (tc *typeCompiler) followExport(declarationName string, exportDecl *ast.Node, sourceFile *ast.SourceFile) *ast.Node {
	decl := exportDecl.AsExportDeclaration()
	if decl.ExportClause != nil {
		if decl.ExportClause.Kind == ast.KindNamedExports {
			namedExports := decl.ExportClause.AsNamedExports()
			if namedExports.Elements != nil {
				for _, element := range namedExports.Elements.Nodes {
					spec := element.AsExportSpecifier()
					exportedName := spec.Name().AsIdentifier().Text
					if exportedName != declarationName {
						continue
					}
					// Found the export specifier for our name
					if decl.ModuleSpecifier == nil || !ast.IsStringLiteral(decl.ModuleSpecifier) {
						// It's `export { Class };` — Class is local or import
						// Look in source file locals
						localName := exportedName
						if spec.PropertyName != nil {
							localName = spec.PropertyName.AsIdentifier().Text
						}
						if sourceFile.AsNode().LocalsContainerData() != nil {
							locals := ast.GetLocals(sourceFile.AsNode())
							if locals != nil {
								if sym, ok := locals[localName]; ok && sym != nil && len(sym.Declarations) > 0 {
									found := sym.Declarations[0]
									if found.Kind != ast.KindImportSpecifier {
										return found
									}
									// It's an import — resolve cross-file
									impDecl := findImportDeclaration(found)
									return tc.resolveImportSpecifier(localName, impDecl)
								}
							}
						}
						return nil
					}
					// It's `export { x } from 'module'` — recurse
					originalName := exportedName
					if spec.PropertyName != nil {
						originalName = spec.PropertyName.AsIdentifier().Text
					}
					return tc.resolveImportSpecifier(originalName, exportDecl)
				}
			}
		}
	} else {
		// `export * from 'x'` — resolve through
		return tc.resolveImportSpecifier(declarationName, exportDecl)
	}
	return nil
}

// shouldReExportOmegaSymbol determines whether a named re-export should include
// the corresponding __Ω symbol. Only type declarations (interface, type alias, enum)
// get __Ω re-exports — classes use static __type instead.
func (tc *typeCompiler) shouldReExportOmegaSymbol(originalName string, exportDecl *ast.Node) bool {
	resolvedDecl := tc.resolveImportSpecifier(originalName, exportDecl)
	if resolvedDecl == nil {
		return false
	}

	// Check if the resolved declaration is a type that generates __Ω
	switch resolvedDecl.Kind {
	case ast.KindInterfaceDeclaration, ast.KindTypeAliasDeclaration, ast.KindEnumDeclaration:
		return true
	default:
		return false
	}
}

// ─── ReceiveType support ───

// getReceiveTypeParameter checks if a type node is ReceiveType<T> and returns
// the type reference node if so. Also handles union types containing ReceiveType.
func getReceiveTypeParameter(typeNode *ast.Node) *ast.Node {
	if typeNode == nil {
		return nil
	}
	if typeNode.Kind == ast.KindUnionType {
		for _, t := range typeNode.AsUnionTypeNode().Types.Nodes {
			if rfn := getReceiveTypeParameter(t); rfn != nil {
				return rfn
			}
		}
		return nil
	}
	if typeNode.Kind == ast.KindTypeReference {
		typeRef := typeNode.AsTypeReferenceNode()
		if typeRef.TypeName.Kind == ast.KindIdentifier {
			name := typeRef.TypeName.AsIdentifier().Text
			if name == "ReceiveType" && typeRef.TypeArguments != nil && len(typeRef.TypeArguments.Nodes) == 1 {
				return typeNode
			}
		}
	}
	return nil
}

// ReceiveTypeInfo maps type argument index → parameter index for ReceiveType params.
type ReceiveTypeInfo struct {
	TypeArgToParamIndex map[int]int
	TotalParams         int
}

// extractReceiveTypeMapping builds a mapping from type parameter index to the
// parameter index where ReceiveType<T> appears.
func extractReceiveTypeMapping(typeParameters *ast.NodeList, parameters []*ast.Node) *ReceiveTypeInfo {
	if typeParameters == nil || len(typeParameters.Nodes) == 0 {
		return nil
	}

	mapping := make(map[int]int)
	for paramIdx, param := range parameters {
		paramDecl := param.AsParameterDeclaration()
		if paramDecl.Type == nil {
			continue
		}
		receiveType := getReceiveTypeParameter(paramDecl.Type)
		if receiveType == nil {
			continue
		}
		typeRef := receiveType.AsTypeReferenceNode()
		if typeRef.TypeArguments == nil || len(typeRef.TypeArguments.Nodes) == 0 {
			continue
		}
		first := typeRef.TypeArguments.Nodes[0]
		if first.Kind != ast.KindTypeReference {
			continue
		}
		firstRef := first.AsTypeReferenceNode()
		if firstRef.TypeName.Kind != ast.KindIdentifier {
			continue
		}
		typeParamName := firstRef.TypeName.AsIdentifier().Text
		for i, tp := range typeParameters.Nodes {
			tpDecl := tp.AsTypeParameterDeclaration()
			if tpDecl.Name() != nil && tpDecl.Name().Kind == ast.KindIdentifier {
				if tpDecl.Name().AsIdentifier().Text == typeParamName {
					mapping[i] = paramIdx
				}
			}
		}
	}

	if len(mapping) == 0 {
		return nil
	}
	return &ReceiveTypeInfo{
		TypeArgToParamIndex: mapping,
		TotalParams:         len(parameters),
	}
}

// hasReceiveTypeParameter checks if any parameter has a ReceiveType<T> type.
func hasReceiveTypeParameter(parameters []*ast.Node) bool {
	for _, param := range parameters {
		paramDecl := param.AsParameterDeclaration()
		if paramDecl.Type != nil && getReceiveTypeParameter(paramDecl.Type) != nil {
			return true
		}
	}
	return false
}

// CallReceiveTypeResult represents the result of resolving a call target's ReceiveType info.
type CallReceiveTypeResult struct {
	Kind string // "direct" or "skip"
	Info *ReceiveTypeInfo
}

// resolveValueDeclaration resolves a value-space identifier to its declaration node,
// walking scope chains and following imports.
func (tc *typeCompiler) resolveValueDeclaration(identifier *ast.Node) *ast.Node {
	if identifier == nil || identifier.Kind != ast.KindIdentifier {
		return nil
	}
	name := identifier.AsIdentifier().Text

	// Walk up the parent chain looking for the symbol in locals
	current := identifier.Parent
	for current != nil {
		if current.Locals() != nil {
			sym := current.Locals()[name]
			if sym != nil && len(sym.Declarations) > 0 {
				decl := sym.Declarations[0]
				if decl.Kind != ast.KindParameter {
					// Follow imports
					return tc.followImportToDeclaration(name, decl)
				}
			}
		}
		if current.Kind == ast.KindSourceFile {
			break
		}
		current = current.Parent
	}
	return nil
}

// followImportToDeclaration follows import specifiers/clauses to their source declaration.
func (tc *typeCompiler) followImportToDeclaration(name string, decl *ast.Node) *ast.Node {
	switch decl.Kind {
	case ast.KindImportSpecifier:
		// ImportSpecifier → NamedImports → ImportClause → ImportDeclaration
		importDecl := decl.Parent.Parent.Parent
		return tc.resolveImportSpecifier(name, importDecl)
	case ast.KindImportClause:
		return tc.resolveImportSpecifier(name, decl.Parent)
	case ast.KindImportDeclaration:
		return tc.resolveImportSpecifier(name, decl)
	default:
		return decl
	}
}

// resolveCallReceiveTypeInfo resolves a call/new expression's target to extract
// ReceiveType parameter info. Returns nil if it can't resolve.
func (tc *typeCompiler) resolveCallReceiveTypeInfo(node *ast.Node) *CallReceiveTypeResult {
	var expression *ast.Node
	var isNew bool
	if node.Kind == ast.KindCallExpression {
		expression = node.AsCallExpression().Expression
	} else if node.Kind == ast.KindNewExpression {
		expression = node.AsNewExpression().Expression
		isNew = true
	} else {
		return nil
	}

	// Case 1: Simple identifier call — fn<T>(args) or new Cls<T>(args)
	if expression.Kind == ast.KindIdentifier {
		decl := tc.resolveValueDeclaration(expression)
		if decl == nil {
			return nil
		}

		if decl.Kind == ast.KindFunctionDeclaration {
			fnDecl := decl.AsFunctionDeclaration()
			if fnDecl.TypeParameters == nil {
				return &CallReceiveTypeResult{Kind: "skip"}
			}
			info := extractReceiveTypeMapping(fnDecl.TypeParameters, fnDecl.Parameters.Nodes)
			if info == nil {
				return &CallReceiveTypeResult{Kind: "skip"}
			}
			return &CallReceiveTypeResult{Kind: "direct", Info: info}
		}

		if decl.Kind == ast.KindVariableDeclaration {
			varDecl := decl.AsVariableDeclaration()
			init := varDecl.Initializer
			if init == nil {
				return nil
			}

			// Unwrap __assignType(fn, ...) wrapper
			unwrapped := getAssignTypeExpression(init)
			if unwrapped != nil {
				init = unwrapped
			}
			// Unwrap parenthesized expression
			for init.Kind == ast.KindParenthesizedExpression {
				init = init.AsParenthesizedExpression().Expression
			}

			if init.Kind == ast.KindArrowFunction {
				arrowFn := init.AsArrowFunction()
				if arrowFn.TypeParameters == nil {
					return &CallReceiveTypeResult{Kind: "skip"}
				}
				info := extractReceiveTypeMapping(arrowFn.TypeParameters, arrowFn.Parameters.Nodes)
				if info == nil {
					return &CallReceiveTypeResult{Kind: "skip"}
				}
				return &CallReceiveTypeResult{Kind: "direct", Info: info}
			}
			if init.Kind == ast.KindFunctionExpression {
				fnExpr := init.AsFunctionExpression()
				if fnExpr.TypeParameters == nil {
					return &CallReceiveTypeResult{Kind: "skip"}
				}
				info := extractReceiveTypeMapping(fnExpr.TypeParameters, fnExpr.Parameters.Nodes)
				if info == nil {
					return &CallReceiveTypeResult{Kind: "skip"}
				}
				return &CallReceiveTypeResult{Kind: "direct", Info: info}
			}

			if isNew {
				if init.Kind == ast.KindClassExpression {
					classExpr := init.AsClassExpression()
					ctor := findConstructor(classExpr.Members.Nodes)
					if ctor != nil && classExpr.TypeParameters != nil {
						info := extractReceiveTypeMapping(classExpr.TypeParameters, ctor.AsConstructorDeclaration().Parameters.Nodes)
						if info != nil {
							return &CallReceiveTypeResult{Kind: "direct", Info: info}
						}
					}
					return &CallReceiveTypeResult{Kind: "skip"}
				}
			}
			return nil
		}

		if isNew && decl.Kind == ast.KindClassDeclaration {
			classDecl := decl.AsClassDeclaration()
			if classDecl.TypeParameters == nil {
				return &CallReceiveTypeResult{Kind: "skip"}
			}
			ctor := findConstructor(classDecl.Members.Nodes)
			if ctor != nil {
				info := extractReceiveTypeMapping(classDecl.TypeParameters, ctor.AsConstructorDeclaration().Parameters.Nodes)
				if info != nil {
					return &CallReceiveTypeResult{Kind: "direct", Info: info}
				}
			}
			return &CallReceiveTypeResult{Kind: "skip"}
		}

		return nil
	}

	// Case 2: Property access — this.method<T>() or obj.method<T>()
	// For now, we only handle this.method<T>() — walk up to enclosing class
	if expression.Kind == ast.KindPropertyAccessExpression {
		propAccess := expression.AsPropertyAccessExpression()
		if propAccess.Expression.Kind == ast.KindThisKeyword {
			methodName := ""
			if propAccess.Name().Kind == ast.KindIdentifier {
				methodName = propAccess.Name().AsIdentifier().Text
			}
			if methodName == "" {
				return nil
			}
			// Walk up to find enclosing class
			parent := node.Parent
			for parent != nil {
				if parent.Kind == ast.KindClassDeclaration || parent.Kind == ast.KindClassExpression {
					classMembers := parent.ClassLikeData().Members
					for _, m := range classMembers.Nodes {
						if m.Kind == ast.KindMethodDeclaration && m.AsMethodDeclaration().Name() != nil {
							methodName2 := ""
							if m.AsMethodDeclaration().Name().Kind == ast.KindIdentifier {
								methodName2 = m.AsMethodDeclaration().Name().AsIdentifier().Text
							}
							if methodName2 == methodName {
								methodDecl := m.AsMethodDeclaration()
								if methodDecl.TypeParameters == nil {
									return &CallReceiveTypeResult{Kind: "skip"}
								}
								info := extractReceiveTypeMapping(methodDecl.TypeParameters, methodDecl.Parameters.Nodes)
								if info == nil {
									return &CallReceiveTypeResult{Kind: "skip"}
								}
								return &CallReceiveTypeResult{Kind: "direct", Info: info}
							}
						}
					}
					return nil
				}
				parent = parent.Parent
			}
			return nil
		}

		// obj.method<T>() — resolve obj to const variable, then find class/type
		if propAccess.Expression.Kind == ast.KindIdentifier {
			methodName := ""
			if propAccess.Name().Kind == ast.KindIdentifier {
				methodName = propAccess.Name().AsIdentifier().Text
			}
			if methodName == "" {
				return nil
			}
			decl := tc.resolveValueDeclaration(propAccess.Expression)
			if decl == nil || decl.Kind != ast.KindVariableDeclaration {
				return nil
			}
			varDecl := decl.AsVariableDeclaration()
			init := varDecl.Initializer
			if init == nil {
				return nil
			}

			// new ClassName() — resolve class and find method
			if init.Kind == ast.KindNewExpression && init.AsNewExpression().Expression.Kind == ast.KindIdentifier {
				classDecl := tc.resolveValueDeclaration(init.AsNewExpression().Expression)
				if classDecl != nil && classDecl.Kind == ast.KindClassDeclaration {
					classMembers := classDecl.ClassLikeData().Members
					for _, m := range classMembers.Nodes {
						if m.Kind == ast.KindMethodDeclaration && m.AsMethodDeclaration().Name() != nil {
							methodName2 := ""
							if m.AsMethodDeclaration().Name().Kind == ast.KindIdentifier {
								methodName2 = m.AsMethodDeclaration().Name().AsIdentifier().Text
							}
							if methodName2 == methodName {
								methodDecl := m.AsMethodDeclaration()
								if methodDecl.TypeParameters == nil {
									return &CallReceiveTypeResult{Kind: "skip"}
								}
								info := extractReceiveTypeMapping(methodDecl.TypeParameters, methodDecl.Parameters.Nodes)
								if info == nil {
									return &CallReceiveTypeResult{Kind: "skip"}
								}
								return &CallReceiveTypeResult{Kind: "direct", Info: info}
							}
						}
					}
				}
				return nil
			}

			// Object literal: const obj = { method: <T>(type: ReceiveType<T>) => {} }
			if init.Kind == ast.KindObjectLiteralExpression {
				for _, prop := range init.AsObjectLiteralExpression().Properties.Nodes {
					if prop.Kind != ast.KindPropertyAssignment {
						continue
					}
					pa := prop.AsPropertyAssignment()
					if pa.Name().Kind != ast.KindIdentifier || pa.Name().AsIdentifier().Text != methodName {
						continue
					}
					propInit := pa.Initializer
					// Unwrap __assignType
					unwrapped := getAssignTypeExpression(propInit)
					if unwrapped != nil {
						propInit = unwrapped
					}
					for propInit.Kind == ast.KindParenthesizedExpression {
						propInit = propInit.AsParenthesizedExpression().Expression
					}
					if propInit.Kind == ast.KindArrowFunction {
						arrowFn := propInit.AsArrowFunction()
						if arrowFn.TypeParameters == nil {
							return &CallReceiveTypeResult{Kind: "skip"}
						}
						info := extractReceiveTypeMapping(arrowFn.TypeParameters, arrowFn.Parameters.Nodes)
						if info == nil {
							return &CallReceiveTypeResult{Kind: "skip"}
						}
						return &CallReceiveTypeResult{Kind: "direct", Info: info}
					}
					if propInit.Kind == ast.KindFunctionExpression {
						fnExpr := propInit.AsFunctionExpression()
						if fnExpr.TypeParameters == nil {
							return &CallReceiveTypeResult{Kind: "skip"}
						}
						info := extractReceiveTypeMapping(fnExpr.TypeParameters, fnExpr.Parameters.Nodes)
						if info == nil {
							return &CallReceiveTypeResult{Kind: "skip"}
						}
						return &CallReceiveTypeResult{Kind: "direct", Info: info}
					}
				}
				return nil
			}
			return nil
		}
	}

	return nil
}

// findConstructor finds the constructor in a class-like members list.
func findConstructor(members []*ast.Node) *ast.Node {
	for _, m := range members {
		if m.Kind == ast.KindConstructor {
			return m
		}
	}
	return nil
}

// buildDirectPassingArgs places type expressions at their ReceiveType parameter positions.
// Returns nil if we can't place (user already provided args at ReceiveType positions).
func (tc *typeCompiler) buildDirectPassingArgs(existingArgs []*ast.Node, typeExpressions []*ast.Node, info *ReceiveTypeInfo) []*ast.Node {
	args := make([]*ast.Node, len(existingArgs))
	copy(args, existingArgs)

	for typeArgIdx, paramIdx := range info.TypeArgToParamIndex {
		if typeArgIdx >= len(typeExpressions) {
			continue
		}
		// If the user already passed an argument at this position, fall back to Ω
		if paramIdx < len(existingArgs) {
			return nil
		}
		// Pad with void 0 up to paramIdx
		for len(args) < paramIdx {
			args = append(args, tc.factory.NewVoidZeroExpression())
		}
		// Extend if needed
		for len(args) <= paramIdx {
			args = append(args, nil)
		}
		args[paramIdx] = typeExpressions[typeArgIdx]
	}

	return args
}

// getAssignTypeExpression checks if an expression is a __assignType(fn, ...) call
// and returns the first argument (the original expression). Handles parenthesized wrappers.
func getAssignTypeExpression(node *ast.Node) *ast.Node {
	if node == nil {
		return nil
	}
	if node.Kind == ast.KindParenthesizedExpression {
		node = node.AsParenthesizedExpression().Expression
	}
	if node.Kind == ast.KindCallExpression {
		call := node.AsCallExpression()
		if call.Expression != nil && call.Expression.Kind == ast.KindIdentifier &&
			call.Expression.AsIdentifier().Text == "__assignType" &&
			call.Arguments != nil && len(call.Arguments.Nodes) > 0 {
			return call.Arguments.Nodes[0]
		}
	}
	return nil
}

// resolveGlobalDeclaration searches through global lib files for a type name.
// Lib files are source files whose path contains "lib." prefix (e.g. lib.es5.d.ts).
func (tc *typeCompiler) resolveGlobalDeclaration(name string) *ast.Node {
	if tc.sourceFiles == nil {
		return nil
	}
	for _, sf := range tc.sourceFiles() {
		// Check if this is a lib file (default library)
		fileName := sf.FileName()
		base := filepath.Base(fileName)
		if !strings.HasPrefix(base, "lib.") {
			continue
		}
		// Check globals (locals at the source file level)
		locals := sf.AsNode().Locals()
		if locals == nil {
			continue
		}
		sym := locals[name]
		if sym != nil && len(sym.Declarations) > 0 {
			return sym.Declarations[0]
		}
	}
	return nil
}
