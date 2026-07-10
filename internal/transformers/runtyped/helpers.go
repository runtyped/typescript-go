package runtyped

import (
	"github.com/microsoft/typescript-go/internal/ast"
)

// knownClasses maps built-in class names to their ReflectionOp.
var knownClasses = map[string]int{
	"Int8Array":         OpInt8Array,
	"Uint8Array":        OpUint8Array,
	"Uint8ClampedArray": OpUint8ClampedArray,
	"Int16Array":        OpInt16Array,
	"Uint16Array":       OpUint16Array,
	"Int32Array":        OpInt32Array,
	"Uint32Array":       OpUint32Array,
	"Float32Array":      OpFloat32Array,
	"Float64Array":      OpFloat64Array,
	"ArrayBuffer":       OpArrayBuffer,
	"BigInt64Array":     OpBigInt64Array,
	"Date":              OpDate,
	"RegExp":            OpRegexp,
	"String":            OpString,
	"Number":            OpNumber,
	"BigInt":            OpBigInt,
	"Boolean":           OpBoolean,
}

// typeNameBrands maps type number brand names to their values.
var typeNameBrands = map[string]int{
	"integer":  TypeNumberBrandInteger,
	"int8":      TypeNumberBrandInt8,
	"int16":     TypeNumberBrandInt16,
	"int32":     TypeNumberBrandInt32,
	"uint8":     TypeNumberBrandUint8,
	"uint16":    TypeNumberBrandUint16,
	"uint32":    TypeNumberBrandUint32,
	"float":     TypeNumberBrandFloat,
	"float32":   TypeNumberBrandFloat32,
	"float64":   TypeNumberBrandFloat64,
}

// intrinsicMapping maps intrinsic type names to TypeIntrinsic values.
var intrinsicMapping = map[string]int{
	"Capitalize":   TypeIntrinsicCapitalize,
	"Uppercase":    TypeIntrinsicUppercase,
	"Lowercase":    TypeIntrinsicLowercase,
	"Uncapitalize": TypeIntrinsicUncapitalize,
}

// getIdentifierName extracts the text from an identifier node.
func getIdentifierName(node *ast.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case ast.KindIdentifier:
		return node.AsIdentifier().Text
	case ast.KindPrivateIdentifier:
		return node.AsPrivateIdentifier().Text
	case ast.KindStringLiteral:
		return node.AsStringLiteral().Text
	case ast.KindNumericLiteral:
		return node.AsNumericLiteral().Text
	case ast.KindNoSubstitutionTemplateLiteral:
		return node.AsNoSubstitutionTemplateLiteral().Text
	}
	return ""
}

// getNameAsString gets a name from a PropertyName or QualifiedName.
func getNameAsString(node *ast.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case ast.KindIdentifier:
		return node.AsIdentifier().Text
	case ast.KindPrivateIdentifier:
		return node.AsPrivateIdentifier().Text
	case ast.KindStringLiteral:
		return node.AsStringLiteral().Text
	case ast.KindNumericLiteral:
		return node.AsNumericLiteral().Text
	case ast.KindBigIntLiteral:
		return node.AsBigIntLiteral().Text
	case ast.KindNoSubstitutionTemplateLiteral:
		return node.AsNoSubstitutionTemplateLiteral().Text
	case ast.KindComputedPropertyName:
		// Can't easily convert computed property names to string
		return ""
	case ast.KindQualifiedName:
		return joinQualifiedName(node)
	}
	return ""
}

func joinQualifiedName(name *ast.Node) string {
	if name.Kind == ast.KindIdentifier {
		return name.AsIdentifier().Text
	}
	qn := name.AsQualifiedName()
	return joinQualifiedName(qn.Left) + "_" + getIdentifierName(qn.Right)
}

// getPropertyName extracts a property name as a string (or returns "" for computed names).
func getPropertyName(node *ast.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case ast.KindIdentifier:
		return node.AsIdentifier().Text
	case ast.KindPrivateIdentifier:
		return node.AsPrivateIdentifier().Text
	case ast.KindStringLiteral:
		return node.AsStringLiteral().Text
	case ast.KindNumericLiteral:
		return node.AsNumericLiteral().Text
	case ast.KindNoSubstitutionTemplateLiteral:
		return node.AsNoSubstitutionTemplateLiteral().Text
	}
	return ""
}

// hasModifier checks if a node has a specific modifier flag.
func hasModifier(node *ast.Node, flag ast.ModifierFlags) bool {
	if node == nil {
		return false
	}
	return node.ModifierFlags()&flag != 0
}

// hasModifierKind checks if a node has a specific modifier kind.
func hasModifierKind(node *ast.Node, kind ast.Kind) bool {
	if node == nil {
		return false
	}
	return node.ModifierFlags()&kindToModifierFlag(kind) != 0
}

func kindToModifierFlag(kind ast.Kind) ast.ModifierFlags {
	switch kind {
	case ast.KindStaticKeyword:
		return ast.ModifierFlagsStatic
	case ast.KindExportKeyword:
		return ast.ModifierFlagsExport
	case ast.KindDeclareKeyword:
		return ast.ModifierFlagsAmbient
	case ast.KindReadonlyKeyword:
		return ast.ModifierFlagsReadonly
	case ast.KindPrivateKeyword:
		return ast.ModifierFlagsPrivate
	case ast.KindProtectedKeyword:
		return ast.ModifierFlagsProtected
	case ast.KindAbstractKeyword:
		return ast.ModifierFlagsAbstract
	case ast.KindPublicKeyword:
		return ast.ModifierFlagsPublic
	default:
		return 0
	}
}

// findSourceFile walks up parent pointers to find the containing SourceFile.
func findSourceFile(node *ast.Node) *ast.SourceFile {
	if node == nil {
		return nil
	}
	if node.Kind == ast.KindSourceFile {
		return node.AsSourceFile()
	}
	current := node.Parent
	for current != nil {
		if current.Kind == ast.KindSourceFile {
			return current.AsSourceFile()
		}
		current = current.Parent
	}
	return nil
}

// getEffectiveConstraintOfTypeParameter returns the constraint of a type parameter, if any.
func getEffectiveConstraintOfTypeParameter(node *ast.Node) *ast.Node {
	if node == nil || node.Kind != ast.KindTypeParameter {
		return nil
	}
	tp := node.AsTypeParameterDeclaration()
	if tp.Constraint != nil {
		return tp.Constraint
	}
	return nil
}
