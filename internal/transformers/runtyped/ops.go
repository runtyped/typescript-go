package runtyped

// ReflectionOp mirrors @runtyped/type-spec's ReflectionOp enum.
// Values must match the TypeScript enum exactly — they are encoded into
// the emitted __type/__Ω strings and decoded at runtime.
const (
	OpNever            = 0
	OpAny              = 1
	OpUnknown          = 2
	OpVoid             = 3
	OpObject           = 4
	OpString           = 5
	OpNumber           = 6
	OpNumberBrand      = 7
	OpBoolean          = 8
	OpBigInt           = 9
	OpSymbol           = 10
	OpNull             = 11
	OpUndefined        = 12
	OpLiteral          = 13
	OpFunction         = 14
	OpMethod           = 15
	OpMethodSignature  = 16
	OpParameter        = 17
	OpProperty         = 18
	OpPropertySignature = 19
	OpClass            = 20
	OpClassExtends     = 21
	OpClassReference   = 22
	OpOptional         = 23
	OpReadonly         = 24
	OpPublic           = 25
	OpPrivate          = 26
	OpProtected        = 27
	OpAbstract         = 28
	OpDefaultValue     = 29
	OpDescription       = 30
	OpRest             = 31
	OpRegexp           = 32
	OpEnum             = 33
	OpEnumMember       = 34
	OpSet              = 35
	OpMap              = 36
	OpArray            = 37
	OpTuple            = 38
	OpTupleMember      = 39
	OpNamedTupleMember = 40
	OpUnion            = 41
	OpIntersection     = 42
	OpIndexSignature   = 43
	OpObjectLiteral    = 44
	OpMappedType       = 45
	OpIn               = 46
	OpFrame            = 47
	OpMoveFrame        = 48
	OpReturn           = 49
	OpTemplateLiteral  = 50
	OpDate             = 51
	OpInt8Array        = 52
	OpUint8ClampedArray = 53
	OpUint8Array       = 54
	OpInt16Array       = 55
	OpUint16Array      = 56
	OpInt32Array       = 57
	OpUint32Array      = 58
	OpFloat32Array     = 59
	OpFloat64Array     = 60
	OpBigInt64Array    = 61
	OpArrayBuffer      = 62
	OpPromise          = 63
	OpArg              = 64
	OpTypeParameter    = 65
	OpTypeParameterDefault = 66
	OpVar              = 67
	OpLoads            = 68
	OpIndexAccess      = 69
	OpKeyof            = 70
	OpInfer            = 71
	OpTypeof           = 72
	OpCondition        = 73
	OpJumpCondition    = 74
	OpJump             = 75
	OpCall             = 76
	OpInline           = 77
	OpInlineCall       = 78
	OpDistribute       = 79
	OpExtends          = 80
	OpWiden            = 81
	OpStatic           = 82
	OpMappedType2      = 83
	OpFunctionReference = 84
	OpCallSignature    = 85
	OpTypeName         = 86
	OpImplements       = 87
	OpNominal          = 88
	OpIntrinsic        = 89
)

// MappedModifier mirrors the MappedModifier enum.
const (
	MappedModifierOptional       = 1 << 0
	MappedModifierRemoveOptional = 1 << 1
	MappedModifierReadonly       = 1 << 2
	MappedModifierRemoveReadonly  = 1 << 3
)

// TypeNumberBrand mirrors the TypeNumberBrand enum.
const (
	TypeNumberBrandInteger  = 0
	TypeNumberBrandInt8     = 1
	TypeNumberBrandInt16    = 2
	TypeNumberBrandInt32    = 3
	TypeNumberBrandUint8    = 4
	TypeNumberBrandUint16   = 5
	TypeNumberBrandUint32   = 6
	TypeNumberBrandFloat    = 7
	TypeNumberBrandFloat32  = 8
	TypeNumberBrandFloat64  = 9
)

// TypeIntrinsic mirrors the TypeIntrinsic enum.
const (
	TypeIntrinsicUppercase   = 0
	TypeIntrinsicLowercase   = 1
	TypeIntrinsicCapitalize  = 2
	TypeIntrinsicUncapitalize = 3
)

// opInfo describes how many stack-parameter arguments follow an opcode.
type opInfo struct {
	params int
}

// ops maps each ReflectionOp to its parameter count.
var ops = map[int]opInfo{
	OpLiteral:           {1},
	OpClassReference:    {1},
	OpPropertySignature: {1},
	OpProperty:          {1},
	OpJump:              {1},
	OpEnumMember:        {1},
	OpTypeParameter:     {1},
	OpTypeParameterDefault: {1},
	OpMappedType:        {2},
	OpCall:              {1},
	OpInline:            {1},
	OpInlineCall:        {2},
	OpLoads:             {2},
	OpExtends:           {0},
	OpInfer:             {2},
	OpDefaultValue:      {1},
	OpParameter:         {1},
	OpMethod:            {1},
	OpFunction:          {1},
	OpDescription:        {1},
	OpNumberBrand:       {1},
	OpTypeof:            {1},
	OpClassExtends:      {1},
	OpDistribute:        {1},
	OpJumpCondition:     {2},
	OpTypeName:          {1},
	OpImplements:        {1},
	OpMappedType2:       {2},
	OpIntrinsic:         {1},
	OpFunctionReference: {1},
}

// encodeOps encodes a slice of opcodes into a string.
// Each opcode is encoded as a single character (opcode + 33, starting at '!').
func encodeOps(opCodes []int) string {
	buf := make([]byte, len(opCodes))
	for i, op := range opCodes {
		buf[i] = byte(op + 33)
	}
	return string(buf)
}
