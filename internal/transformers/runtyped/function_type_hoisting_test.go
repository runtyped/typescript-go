package runtyped_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/testutil/harnessutil"
	"github.com/microsoft/typescript-go/internal/tsoptions"
)

// compileSingleFileTS compiles a single TS file and returns the emitted JS.
func compileHoistFile(t *testing.T, content string) string {
	t.Helper()
	return compileHoistFiles(t, map[string]string{"app.ts": content})
}

// compileHoistFiles compiles multiple TS files and returns the app.js output.
func compileHoistFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	var inputFiles []*harnessutil.TestFile
	var fileNames []string
	for name, content := range files {
		inputFiles = append(inputFiles, &harnessutil.TestFile{UnitName: name, Content: content})
		fileNames = append(fileNames, "/"+name)
	}
	result := harnessutil.CompileFiles(t,
		inputFiles,
		nil,
		harnessutil.TestConfiguration{},
		&tsoptions.ParsedCommandLine{
			ParsedConfig: &core.ParsedOptions{
				CompilerOptions: &core.CompilerOptions{
					Module:           core.ModuleKindCommonJS,
					ModuleResolution: core.ModuleResolutionKindNode10,
					Target:           core.ScriptTargetES2016,
				},
				FileNames: fileNames,
			},
		},
		"/",
		nil,
	)
	appJS := result.JS.GetOrZero("/app.js")
	if appJS == nil {
		t.Fatal("no app.js output")
	}
	return appJS.Content
}

// runNodeJS runs the given JS with node and returns trimmed stdout.
func runNodeJS(t *testing.T, js string) string {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "runtyped-hoist-*.js")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(js); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	cmd := exec.Command("node", tmpFile.Name())
	// Ensure @runtyped/type is resolvable from the temp file location
	cmd.Env = append(os.Environ(), "NODE_PATH=/app/node_modules")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node failed: %v\nstderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// assertBefore checks that a appears before b in s.
func assertBefore(t *testing.T, s, a, b string) {
	t.Helper()
	ai := strings.Index(s, a)
	bi := strings.Index(s, b)
	if ai < 0 {
		t.Fatalf("%q not found in output", a)
	}
	if bi < 0 {
		t.Fatalf("%q not found in output", b)
	}
	if ai >= bi {
		t.Errorf("%q should appear before %q (at %d vs %d)", a, b, ai, bi)
	}
}

// assertAfter checks that a appears after b in s.
func assertAfter(t *testing.T, s, a, b string) {
	t.Helper()
	ai := strings.Index(s, a)
	bi := strings.Index(s, b)
	if ai < 0 {
		t.Fatalf("%q not found in output", a)
	}
	if bi < 0 {
		t.Fatalf("%q not found in output", b)
	}
	if ai <= bi {
		t.Errorf("%q should appear after %q (at %d vs %d)", a, b, ai, bi)
	}
}

// ============================================================================
// MODULE-LEVEL FUNCTION __type HOISTING
// ============================================================================

func TestHoist_ModuleLevelFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function greet(name: string): string {
    return "Hello, " + name;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "function greet") {
		t.Error("missing function greet")
	}
	if !strings.Contains(js, "greet.__type") {
		t.Error("missing greet.__type")
	}
	assertBefore(t, js, "greet.__type", "function greet")
}

func TestHoist_ModuleLevelComplexParams(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `interface User {
    id: number;
    name: string;
}

function processUser(user: User, count: number): User {
    return user;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "processUser.__type") {
		t.Error("missing processUser.__type")
	}
	if !strings.Contains(js, "__ΩUser") {
		t.Error("missing __ΩUser")
	}
	assertBefore(t, js, "processUser.__type", "function processUser")
}

func TestHoist_MultipleModuleLevelFunctions(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function first(a: string): void {}
function second(b: number): boolean { return true; }
function third(c: boolean): string { return ""; }`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "first.__type") {
		t.Error("missing first.__type")
	}
	if !strings.Contains(js, "second.__type") {
		t.Error("missing second.__type")
	}
	if !strings.Contains(js, "third.__type") {
		t.Error("missing third.__type")
	}
	// All __type assignments should be hoisted before the first function declaration
	firstFuncIdx := strings.Index(js, "function first")
	firstTypeIdx := strings.Index(js, "first.__type")
	secondTypeIdx := strings.Index(js, "second.__type")
	thirdTypeIdx := strings.Index(js, "third.__type")
	if firstTypeIdx >= firstFuncIdx {
		t.Errorf("first.__type should be before function first")
	}
	if secondTypeIdx >= firstFuncIdx {
		t.Errorf("second.__type should be before function first")
	}
	if thirdTypeIdx >= firstFuncIdx {
		t.Errorf("third.__type should be before function first")
	}
}

func TestHoist_UseStrict(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `"use strict";
function greet(name: string): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "greet.__type") {
		t.Error("missing greet.__type")
	}
	assertBefore(t, js, `"use strict"`, "greet.__type")
}

func TestHoist_UseClient(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `"use client";
function component(props: { name: string }): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "component.__type") {
		t.Error("missing component.__type")
	}
	assertBefore(t, js, `"use client"`, "component.__type")
}

// ============================================================================
// BLOCK-SCOPED FUNCTION DECLARATIONS - __type stays inline
// ============================================================================

func TestHoist_BlockScopedIf(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `if (true) {
    function insideIf(x: number): void {}
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "insideIf.__type") {
		t.Error("missing insideIf.__type")
	}
	assertAfter(t, js, "insideIf.__type", "function insideIf")
}

func TestHoist_BlockScopedFor(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `for (let i = 0; i < 10; i++) {
    function insideFor(x: number): void {}
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "insideFor.__type") {
		t.Error("missing insideFor.__type")
	}
	assertAfter(t, js, "insideFor.__type", "function insideFor")
}

func TestHoist_BlockScopedWhile(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `while (true) {
    function insideWhile(x: number): void {}
    break;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "insideWhile.__type") {
		t.Error("missing insideWhile.__type")
	}
	assertAfter(t, js, "insideWhile.__type", "function insideWhile")
}

func TestHoist_BlockScopedBlock(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `{
    function insideBlock(x: number): void {}
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "insideBlock.__type") {
		t.Error("missing insideBlock.__type")
	}
	assertAfter(t, js, "insideBlock.__type", "function insideBlock")
}

// ============================================================================
// NESTED FUNCTION DECLARATIONS - inner function __type stays inline
// ============================================================================

func TestHoist_NestedFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function outer(a: string): void {
    function inner(b: number): boolean {
        return b > 0;
    }
    inner(1);
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "outer.__type") {
		t.Error("missing outer.__type")
	}
	if !strings.Contains(js, "inner.__type") {
		t.Error("missing inner.__type")
	}
	assertBefore(t, js, "outer.__type", "function outer")
	assertAfter(t, js, "inner.__type", "function inner")
}

func TestHoist_DeeplyNested(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function level1(a: string): void {
    function level2(b: number): void {
        function level3(c: boolean): string {
            return String(c);
        }
    }
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "level1.__type") {
		t.Error("missing level1.__type")
	}
	if !strings.Contains(js, "level2.__type") {
		t.Error("missing level2.__type")
	}
	if !strings.Contains(js, "level3.__type") {
		t.Error("missing level3.__type")
	}
	assertBefore(t, js, "level1.__type", "function level1")
	assertAfter(t, js, "level2.__type", "function level2")
	assertAfter(t, js, "level3.__type", "function level3")
}

// ============================================================================
// EXPORTED FUNCTION DECLARATIONS
// ============================================================================

func TestHoist_ExportedFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `export function exportedFunc(x: string): number {
    return x.length;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "exportedFunc.__type") {
		t.Error("missing exportedFunc.__type")
	}
	assertBefore(t, js, "exportedFunc.__type", "function exportedFunc")
}

func TestHoist_ExportDefaultNamedFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `export default function namedDefault(x: string): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "namedDefault.__type") {
		t.Error("missing namedDefault.__type")
	}
	assertBefore(t, js, "namedDefault.__type", "function namedDefault")
}

func TestHoist_ExportDefaultAnonymousFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `export default function(x: string): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "__assignType") {
		t.Error("missing __assignType wrapper for anonymous default export")
	}
}

// ============================================================================
// ARROW FUNCTIONS AND FUNCTION EXPRESSIONS - use __assignType wrapper
// ============================================================================

func TestHoist_ArrowFunctionAssignType(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `const arrowFn = (x: string): number => x.length;`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "__assignType") {
		t.Error("arrow function should use __assignType")
	}
}

func TestHoist_FunctionExpressionAssignType(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `const funcExpr = function(x: string): number {
    return x.length;
};`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "__assignType") {
		t.Error("function expression should use __assignType")
	}
}

func TestHoist_NamedFunctionExpressionAssignType(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `const funcExpr = function namedExpr(x: string): number {
    return x.length;
};`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "__assignType") {
		t.Error("named function expression should use __assignType")
	}
}

// ============================================================================
// FUNCTION WITH TYPES FROM OTHER FILES
// ============================================================================

func TestHoist_ImportedTypeReference(t *testing.T) {
	t.Parallel()
	js := compileHoistFiles(t, map[string]string{
		"app.ts": `import { Logger } from './logger.js';

function logMessage(logger: Logger, message: string): void {
    logger.log(message);
}`,
		"logger.ts": `export class Logger {
    log(msg: string): void {}
}`,
	})
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "logMessage.__type") {
		t.Error("missing logMessage.__type")
	}
	assertBefore(t, js, "logMessage.__type", "function logMessage")
}

func TestHoist_TypeAliasReference(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `type UserId = string & { __brand: 'UserId' };

function getUser(id: UserId): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "getUser.__type") {
		t.Error("missing getUser.__type")
	}
	if !strings.Contains(js, "__ΩUserId") {
		t.Error("missing __ΩUserId")
	}
	assertBefore(t, js, "getUser.__type", "function getUser")
}

func TestHoist_InterfaceDefinedLater(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function processItem(item: Item): void {}

interface Item {
    id: number;
    name: string;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "processItem.__type") {
		t.Error("missing processItem.__type")
	}
	if !strings.Contains(js, "__ΩItem") {
		t.Error("missing __ΩItem")
	}
	assertBefore(t, js, "processItem.__type", "function processItem")
}

// ============================================================================
// ASYNC FUNCTIONS AND GENERATORS
// ============================================================================

func TestHoist_AsyncFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `async function fetchData(url: string): Promise<string> {
    return url;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "fetchData.__type") {
		t.Error("missing fetchData.__type")
	}
	assertBefore(t, js, "fetchData.__type", "function fetchData")
}

func TestHoist_GeneratorFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function* generateNumbers(max: number): Generator<number> {
    for (let i = 0; i < max; i++) {
        yield i;
    }
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "generateNumbers.__type") {
		t.Error("missing generateNumbers.__type")
	}
	assertBefore(t, js, "generateNumbers.__type", "function* generateNumbers")
}

func TestHoist_AsyncGeneratorFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `async function* asyncGenerator(items: string[]): AsyncGenerator<string> {
    for (const item of items) {
        yield item;
    }
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "asyncGenerator.__type") {
		t.Error("missing asyncGenerator.__type")
	}
	assertBefore(t, js, "asyncGenerator.__type", "function asyncGenerator")
}

// ============================================================================
// RUNTIME TESTS - REFLECTION FUNCTION
// ============================================================================

func TestHoist_Runtime_ReflectionAfterDeclaration(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function greet(name: string): string {
    return "Hello, " + name;
}

const rf = require('@runtyped/type').ReflectionFunction;
const reflection = rf.from(greet);
console.log(JSON.stringify(reflection.getParameters().length));`)
	got := runNodeJS(t, js)
	if got != "1" {
		t.Errorf("got %q, want %q", got, "1")
	}
}

func TestHoist_Runtime_ReflectionBeforeDeclaration(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `const rf = require('@runtyped/type').ReflectionFunction;
const reflection = rf.from(greet);
const count = reflection.getParameters().length;
console.log(JSON.stringify(count));

function greet(name: string): string {
    return "Hello, " + name;
}`)
	got := runNodeJS(t, js)
	if got != "1" {
		t.Errorf("got %q, want %q — __type should be hoisted before function declaration", got, "1")
	}
}

func TestHoist_Runtime_MultipleParams(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function multi(a: string, b: number, c: boolean): void {}

const rf = require('@runtyped/type').ReflectionFunction;
const reflection = rf.from(multi);
console.log(JSON.stringify(reflection.getParameters().length));`)
	got := runNodeJS(t, js)
	if got != "3" {
		t.Errorf("got %q, want %q", got, "3")
	}
}

func TestHoist_Runtime_ArrowFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `const arrowFn = (x: string, y: number): boolean => true;

const rf = require('@runtyped/type').ReflectionFunction;
const reflection = rf.from(arrowFn);
console.log(JSON.stringify(reflection.getParameters().length));`)
	got := runNodeJS(t, js)
	if got != "2" {
		t.Errorf("got %q, want %q", got, "2")
	}
}

func TestHoist_Runtime_FunctionExpression(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `const funcExpr = function(a: string, b: string): string {
    return a + b;
};

const rf = require('@runtyped/type').ReflectionFunction;
const reflection = rf.from(funcExpr);
console.log(JSON.stringify(reflection.getParameters().length));`)
	got := runNodeJS(t, js)
	if got != "2" {
		t.Errorf("got %q, want %q", got, "2")
	}
}

// ============================================================================
// GENERIC FUNCTIONS
// ============================================================================

func TestHoist_GenericFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function identity<T>(value: T): T {
    return value;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "identity.__type") {
		t.Error("missing identity.__type")
	}
	assertBefore(t, js, "identity.__type", "function identity")
}

func TestHoist_MultipleTypeParameters(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function map<T, U>(items: T[], transform: (item: T) => U): U[] {
    return items.map(transform);
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "map.__type") {
		t.Error("missing map.__type")
	}
}

func TestHoist_ConstrainedTypeParameter(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `interface Lengthwise {
    length: number;
}

function logLength<T extends Lengthwise>(arg: T): number {
    return arg.length;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "logLength.__type") {
		t.Error("missing logLength.__type")
	}
}

// ============================================================================
// OVERLOADED FUNCTIONS
// ============================================================================

func TestHoist_OverloadedFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function process(x: string): string;
function process(x: number): number;
function process(x: string | number): string | number {
    return x;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "process.__type") {
		t.Error("missing process.__type on overloaded function")
	}
}

// ============================================================================
// EDGE CASES
// ============================================================================

func TestHoist_DestructuredParams(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function destructured({ name, age }: { name: string; age: number }): string {
    return name;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "destructured.__type") {
		t.Error("missing destructured.__type")
	}
}

func TestHoist_RestParams(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function withRest(first: string, ...rest: number[]): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "withRest.__type") {
		t.Error("missing withRest.__type")
	}
}

func TestHoist_DefaultParams(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function withDefaults(name: string = "default", count: number = 0): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "withDefaults.__type") {
		t.Error("missing withDefaults.__type")
	}
}

func TestHoist_OptionalParams(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function withOptional(required: string, optional?: number): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "withOptional.__type") {
		t.Error("missing withOptional.__type")
	}
}

func TestHoist_ReturnVoid(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function returnsVoid(): void {}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "returnsVoid.__type") {
		t.Error("missing returnsVoid.__type")
	}
}

func TestHoist_ReturnNever(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function throwsError(): never {
    throw new Error("Always throws");
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "throwsError.__type") {
		t.Error("missing throwsError.__type")
	}
}

// ============================================================================
// MIXED SCENARIOS
// ============================================================================

func TestHoist_MixedFunctionTypes(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `// Regular function declaration
function declaredFunc(a: string): void {}

// Arrow function
const arrowFunc = (b: number): boolean => true;

// Function expression
const exprFunc = function(c: boolean): string { return ""; };

// Exported function
export function exportedFunc(d: object): number { return 0; }`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "declaredFunc.__type") {
		t.Error("missing declaredFunc.__type")
	}
	if !strings.Contains(js, "exportedFunc.__type") {
		t.Error("missing exportedFunc.__type")
	}
	if !strings.Contains(js, "__assignType") {
		t.Error("missing __assignType for arrow/expression functions")
	}
	assertBefore(t, js, "declaredFunc.__type", "function declaredFunc")
}

// ============================================================================
// DECLARATION FILES
// ============================================================================

func TestHoist_DeclareFunction(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `declare function externalFunc(x: number): string;

function localFunc(y: number): string {
    return String(y);
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "localFunc.__type") {
		t.Error("missing localFunc.__type")
	}
}

// ============================================================================
// IIFE (Immediately Invoked Function Expression)
// ============================================================================

func TestHoist_IIFE(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `const result = (function(x: number): number {
    return x * 2;
})(5);`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "__assignType") {
		t.Error("IIFE should use __assignType")
	}
}

// ============================================================================
// CLASS METHODS VS STANDALONE FUNCTIONS
// ============================================================================

func TestHoist_ClassMethodsVsStandalone(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `class MyClass {
    method(x: string): void {}
}

function standaloneFunc(x: string): void {}`)
	t.Logf("output:\n%s", js)
	// Class gets __type (either static member or post-class assignment)
	if !strings.Contains(js, "MyClass.__type") {
		t.Error("missing MyClass.__type on class")
	}
	if !strings.Contains(js, "standaloneFunc.__type") {
		t.Error("missing standaloneFunc.__type")
	}
	assertBefore(t, js, "standaloneFunc.__type", "function standaloneFunc")
}

// ============================================================================
// TRANSPILE TESTS (Full compilation)
// ============================================================================

func TestHoist_TranspileHoistedTypeBefore(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function greet(name: string): string {
    return "Hello, " + name;
}`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "greet.__type") {
		t.Error("missing greet.__type")
	}
	if !strings.Contains(js, "function greet") {
		t.Error("missing function greet")
	}
	assertBefore(t, js, "greet.__type", "function greet")
}

func TestHoist_TranspileMultipleFunctions(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `function first(a: string): void {}
function second(b: number): boolean { return true; }
function third(c: boolean): string { return ""; }`)
	t.Logf("output:\n%s", js)
	if !strings.Contains(js, "first.__type") {
		t.Error("missing first.__type")
	}
	if !strings.Contains(js, "second.__type") {
		t.Error("missing second.__type")
	}
	if !strings.Contains(js, "third.__type") {
		t.Error("missing third.__type")
	}
	assertBefore(t, js, "first.__type", "function first")
}

// ============================================================================
// UNTYPED ARROW FUNCTIONS - should not break reflection
// ============================================================================

func TestHoist_Runtime_UntypedArrowNoParams(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `class Container {
    value = 42;

    getFactory() {
        return () => this.value;
    }
}

const container = new Container();
const factory = container.getFactory();

const rf = require('@runtyped/type').ReflectionFunction;
const reflection = rf.from(factory);
console.log(JSON.stringify(reflection.type.kind));`)
	got := runNodeJS(t, js)
	// ReflectionKind.function = 17
	if got != "17" {
		t.Errorf("got %q, want %q (ReflectionKind.function)", got, "17")
	}
}

func TestHoist_Runtime_UntypedArrowProviderPattern(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `class ServiceContainer {
    private contextValue = 'test-context';

    addProvider(config: { useFactory: () => any }) {
        const rf = require('@runtyped/type').ReflectionFunction;
        const reflection = rf.from(config.useFactory);
        return reflection.type.kind;
    }
}

const container = new ServiceContainer();
const result = container.addProvider({
    useFactory: () => container['contextValue']
});

console.log(JSON.stringify(result));`)
	got := runNodeJS(t, js)
	// ReflectionKind.function = 17
	if got != "17" {
		t.Errorf("got %q, want %q (ReflectionKind.function)", got, "17")
	}
}

func TestHoist_Transform_UntypedArrowNoInvalidAssignType(t *testing.T) {
	t.Parallel()
	js := compileHoistFile(t, `class Container {
    value = 42;
    getFactory() {
        return () => this.value;
    }
}`)
	t.Logf("output:\n%s", js)
	// Check that if __assignType is present, it doesn't have just ['"'] (which is just 'any')
	hasInvalidAnyOnly := strings.Contains(js, `__assignType(() => this.value, ['"`)
	if hasInvalidAnyOnly {
		t.Error("arrow function should not have __assignType with just ['\"'] (any-only type)")
	}
}
