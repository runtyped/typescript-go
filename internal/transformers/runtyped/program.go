package runtyped

import (
	"github.com/microsoft/typescript-go/internal/ast"
)

// stackEntry is an item on the compiler program's stack.
// It can be an AST expression node, a string, a number, or a boolean.
// At emit time, these get serialized into a JavaScript array literal.
type stackEntry struct {
	kind stackEntryKind
	node *ast.Node   // for kindStackNode
	str  string      // for kindStackString
	num  float64     // for kindStackNumber
	bval bool        // for kindStackBool
}

type stackEntryKind int

const (
	stackEntryNode stackEntryKind = iota
	stackEntryString
	stackEntryNumber
	stackEntryBool
)

func (s stackEntry) equals(other stackEntry) bool {
	if s.kind != other.kind {
		return false
	}
	switch s.kind {
	case stackEntryNode:
		return s.node == other.node
	case stackEntryString:
		return s.str == other.str
	case stackEntryNumber:
		return s.num == other.num
	case stackEntryBool:
		return s.bval == other.bval
	}
	return false
}

// frame represents a scope frame for variable tracking.
type frame struct {
	variables []frameVariable
	opIndex   int
	conditional bool
	previous  *frame
}

type frameVariable struct {
	name  string
	index int
}

type variableLocation struct {
	frameOffset int
	stackIndex  int
}

func findVariableInFrame(f *frame, name string, frameOffset int) *variableLocation {
	for i := range f.variables {
		if f.variables[i].name == name {
			return &variableLocation{frameOffset: frameOffset, stackIndex: f.variables[i].index}
		}
	}
	if f.previous != nil {
		return findVariableInFrame(f.previous, name, frameOffset+1)
	}
	return nil
}

func findConditionalFrame(f *frame) *frame {
	if f.conditional {
		return f
	}
	if f.previous != nil {
		return findConditionalFrame(f.previous)
	}
	return nil
}

// coRoutine is a subroutine block within the program.
type coRoutine struct {
	ops []int
}

// compilerProgram accumulates opcodes and stack entries for a single type.
type compilerProgram struct {
	ops          []int
	stack        []stackEntry
	mainOffset   int
	stackPosition int
	currentFrame *frame

	activeCoRoutines []*coRoutine
	coRoutines       []*coRoutine

	forNode    *ast.Node
	sourceFile *ast.SourceFile

	resolveFunctionParams map[*ast.Node]int
}

func newCompilerProgram(forNode *ast.Node, sourceFile *ast.SourceFile) *compilerProgram {
	return &compilerProgram{
		forNode:    forNode,
		sourceFile: sourceFile,
		currentFrame: &frame{},
		resolveFunctionParams: make(map[*ast.Node]int),
	}
}

func (p *compilerProgram) isEmpty() bool {
	return len(p.ops) == 0 && len(p.coRoutines) == 0
}

func (p *compilerProgram) buildPackStruct() ([]int, []stackEntry) {
	result := make([]int, len(p.ops))
	copy(result, p.ops)

	if len(p.coRoutines) > 0 {
		// Prepend co-routines in reverse order
		for i := len(p.coRoutines) - 1; i >= 0; i-- {
			result = append(p.coRoutines[i].ops, result...)
		}
	}

	if p.mainOffset > 0 {
		result = append([]int{OpJump, p.mainOffset}, result...)
	}

	return result, p.stack
}

func (p *compilerProgram) pushConditionalFrame() {
	f := p.pushFrame(false)
	f.conditional = true
}

func (p *compilerProgram) pushStack(item stackEntry) int {
	idx := len(p.stack)
	p.stack = append(p.stack, item)
	p.stackPosition = idx + 1
	return idx
}

func (p *compilerProgram) pushStackNode(node *ast.Node) int {
	return p.pushStack(stackEntry{kind: stackEntryNode, node: node})
}

func (p *compilerProgram) pushStackString(s string) int {
	return p.pushStack(stackEntry{kind: stackEntryString, str: s})
}

func (p *compilerProgram) pushStackNumber(n float64) int {
	return p.pushStack(stackEntry{kind: stackEntryNumber, num: n})
}

func (p *compilerProgram) pushStackBool(b bool) int {
	return p.pushStack(stackEntry{kind: stackEntryBool, bval: b})
}

func (p *compilerProgram) pushCoRoutine() {
	p.pushFrame(true)
	p.activeCoRoutines = append(p.activeCoRoutines, &coRoutine{})
}

func (p *compilerProgram) popCoRoutine() int {
	cr := p.activeCoRoutines[len(p.activeCoRoutines)-1]
	p.activeCoRoutines = p.activeCoRoutines[:len(p.activeCoRoutines)-1]
	p.popFrameImplicit()
	if p.mainOffset == 0 {
		p.mainOffset = 2
	}
	startIndex := p.mainOffset
	cr.ops = append(cr.ops, OpReturn)
	p.coRoutines = append(p.coRoutines, cr)
	p.mainOffset += len(cr.ops)
	return startIndex
}

func (p *compilerProgram) pushOp(opCodes ...int) {
	if len(p.activeCoRoutines) > 0 {
		p.activeCoRoutines[len(p.activeCoRoutines)-1].ops = append(p.activeCoRoutines[len(p.activeCoRoutines)-1].ops, opCodes...)
		return
	}
	p.ops = append(p.ops, opCodes...)
}

func (p *compilerProgram) pushOpAtFrame(f *frame, opCodes ...int) {
	if len(p.activeCoRoutines) > 0 {
		cr := p.activeCoRoutines[len(p.activeCoRoutines)-1]
		cr.ops = append(cr.ops[:f.opIndex], append(opCodes, cr.ops[f.opIndex:]...)...)
		return
	}
	p.ops = append(p.ops[:f.opIndex], append(opCodes, p.ops[f.opIndex:]...)...)
}

func (p *compilerProgram) findOrAddStackEntry(entry stackEntry) int {
	for i, s := range p.stack {
		if s.equals(entry) {
			return i
		}
	}
	return p.pushStack(entry)
}

func (p *compilerProgram) increaseStackPosition() int {
	return p.stackPosition
}

func (p *compilerProgram) resolveFunctionParametersIncrease(fn *ast.Node) {
	p.resolveFunctionParams[fn]++
}

func (p *compilerProgram) resolveFunctionParametersDecrease(fn *ast.Node) {
	p.resolveFunctionParams[fn]--
}

func (p *compilerProgram) isResolveFunctionParameters(fn *ast.Node) bool {
	return p.resolveFunctionParams[fn] > 0
}

func (p *compilerProgram) pushFrame(implicit bool) *frame {
	if !implicit {
		p.pushOp(OpFrame)
	}
	var opIndex int
	if len(p.activeCoRoutines) > 0 {
		opIndex = len(p.activeCoRoutines[len(p.activeCoRoutines)-1].ops)
	} else {
		opIndex = len(p.ops)
	}
	p.currentFrame = &frame{previous: p.currentFrame, opIndex: opIndex}
	return p.currentFrame
}

func (p *compilerProgram) findConditionalFrame() *frame {
	return findConditionalFrame(p.currentFrame)
}

func (p *compilerProgram) popFrameImplicit() {
	if p.currentFrame.previous != nil {
		p.currentFrame = p.currentFrame.previous
	}
}

func (p *compilerProgram) moveFrame() {
	p.pushOp(OpMoveFrame)
	if p.currentFrame.previous != nil {
		p.currentFrame = p.currentFrame.previous
	}
}

func (p *compilerProgram) pushVariable(name string) int {
	return p.pushVariableAtFrame(name, p.currentFrame)
}

func (p *compilerProgram) pushVariableAtFrame(name string, f *frame) int {
	p.pushOpAtFrame(f, OpVar)
	idx := len(f.variables)
	f.variables = append(f.variables, frameVariable{index: idx, name: name})
	return idx
}

func (p *compilerProgram) pushTemplateParameter(name string, withDefault bool) int {
	if withDefault {
		p.pushOp(OpTypeParameterDefault, p.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: name}))
	} else {
		p.pushOp(OpTypeParameter, p.findOrAddStackEntry(stackEntry{kind: stackEntryString, str: name}))
	}
	idx := len(p.currentFrame.variables)
	p.currentFrame.variables = append(p.currentFrame.variables, frameVariable{index: idx, name: name})
	return idx
}

func (p *compilerProgram) findVariable(name string) *variableLocation {
	return findVariableInFrame(p.currentFrame, name, 0)
}
