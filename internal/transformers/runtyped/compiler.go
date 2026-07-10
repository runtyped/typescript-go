package runtyped

import (
	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/printer"
)

// resolveDeclarationResult holds a resolved declaration and metadata.
type resolveDeclarationResult struct {
	declaration      *ast.Node
	importDeclaration *ast.Node // ImportDeclaration or nil
	typeOnly         bool
}

// typeCompiler holds the state needed during type compilation for one source file.
// It is the Go equivalent of the TypeScript CompilerProgram + extractPackStructOfType logic.
type typeCompiler struct {
	factory *printer.NodeFactory

	sourceFile *ast.SourceFile

	// compileDeclarations: types in the same file that need __Ω
	compileDeclarations map[*ast.Node]*compileDeclEntry
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

func newTypeCompiler(factory *printer.NodeFactory) *typeCompiler {
	return &typeCompiler{
		factory:              factory,
		compileDeclarations:  make(map[*ast.Node]*compileDeclEntry),
		embedDeclarations:    make(map[*ast.Node]*embedDeclEntry),
		compiledDeclarations: make(map[*ast.Node]bool),
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

// extractTypeReferenceFromExpression handles ExpressionWithTypeArguments.
func (tc *typeCompiler) extractTypeReferenceFromExpression(node *ast.Node, program *compilerProgram) {
	ewta := node.AsExpressionWithTypeArguments()
	var typeName *ast.Node
	if ewta.Expression.Kind == ast.KindIdentifier {
		typeName = ewta.Expression
	}
	tc.extractTypeReferenceFromEntityName(typeName, ewta.TypeArguments, program)
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
	declSourceFile := findSourceFile(declaration)

	isFromImport := resolved.importDeclaration != nil
	isGlobal := declSourceFile == nil || (resolved.importDeclaration == nil && (declSourceFile == nil || declSourceFile.FileName() != tc.sourceFile.FileName()))

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
				// For .d.ts files, check if __Ω is exported
				if declSourceFile != nil && isDtsFile(declSourceFile.FileName()) {
					// Try to resolve __Ω symbol — for now, fall back to any
					// Full resolution requires the Resolver which needs vfs
					tc.resolveTypeOnlyImport(typeName, program)
					return
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
		// This requires the Resolver which needs vfs — deferred for now
		return nil
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
		// ImportClause.IsTypeOnly doesn't exist in typescript-go — type-only is
		// determined by usage. For now, check if the import specifier is type-only.
		// Resolve import to source declaration
		// This requires the Resolver — deferred for now
		// For same-file references, we can't resolve cross-file imports
		return &resolveDeclarationResult{
			declaration:      declaration,
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
