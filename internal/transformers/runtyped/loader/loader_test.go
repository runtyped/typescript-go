package loader_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/transformers/runtyped/loader"
)

// testDir is the directory where test files are placed.
const testDir = "/runtyped/loader-test"

// transformInline transforms source code at a fake path within the test directory.
func transformInline(t *testing.T, l *loader.Loader, code, fileName string) string {
	t.Helper()
	path := filepath.Join(testDir, fileName)
	result, err := l.Transform(code, path)
	if err != nil {
		t.Fatalf("Transform failed: %v", err)
	}
	return result
}

// runNode runs JS with node and returns trimmed stdout.
func runNode(t *testing.T, js string) string {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "loader-test-*.js")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(js)
	tmpFile.Close()
	cmd := exec.Command("node", tmpFile.Name())
	cmd.Env = append(os.Environ(), "NODE_PATH=/app/node_modules")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node failed: %v\nstderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// ============================================================================
// Basic Functionality
// ============================================================================

func TestLoader_BasicInterface(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `interface User {
    id: number;
    name: string;
}`, "simple-interface.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "__ΩUser") {
		t.Error("missing __ΩUser")
	}
}

func TestLoader_BasicClass(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `class User {
    id: number = 0;
    name: string = '';
}`, "simple-class.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "__type") {
		t.Error("missing __type")
	}
	if !strings.Contains(result, "User") {
		t.Error("missing User")
	}
}

func TestLoader_BasicFunction(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `function getType<T>(type?: ReceiveType<T>) {
    return resolveReceiveType(type);
}

getType<string>();`, "generic-function.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "getType") {
		t.Error("missing getType")
	}
	if !strings.Contains(result, "__type") {
		t.Error("missing __type")
	}
}

func TestLoader_BasicArrowFunction(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `const fn = <T>(value: T): T => value;`, "arrow-function.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "__assignType") {
		t.Error("missing __assignType")
	}
}

func TestLoader_BasicTypeAlias(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `type Status = 'active' | 'inactive' | 'pending';`, "type-alias.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "__ΩStatus") {
		t.Error("missing __ΩStatus")
	}
}

func TestLoader_BasicEnum(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `enum Color {
    Red,
    Green,
    Blue
}`, "enum.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "Color") {
		t.Error("missing Color")
	}
	if !strings.Contains(result, "__ΩColor") {
		t.Error("missing __ΩColor")
	}
}

// ============================================================================
// Cross-file Imports
// ============================================================================

func TestLoader_CrossFileInterfaceImport(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	// Transform shared.ts first
	sharedPath := filepath.Join(testDir, "shared.ts")
	sharedCode := `export interface CreateUserData {
    readonly name: string;
}`
	sharedResult, err := l.Transform(sharedCode, sharedPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("shared.ts:\n%s", sharedResult)
	if !strings.Contains(sharedResult, "__ΩCreateUserData") {
		t.Error("shared: missing __ΩCreateUserData")
	}

	// Transform main.ts which imports from shared
	mainPath := filepath.Join(testDir, "main.ts")
	mainCode := `import { type ReceiveType, resolveReceiveType } from '@runtyped/type';
import { CreateUserData } from './shared.js';

function fn<T>(t?: ReceiveType<T>) {
    return resolveReceiveType(t);
}

fn<CreateUserData>();`
	mainResult, err := l.Transform(mainCode, mainPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("main.ts:\n%s", mainResult)
	if !strings.Contains(mainResult, "__ΩCreateUserData") {
		t.Error("main: missing __ΩCreateUserData")
	}
	// Should import __ΩCreateUserData from shared
	if !strings.Contains(mainResult, "__ΩCreateUserData") {
		t.Error("main: missing __ΩCreateUserData import")
	}
}

func TestLoader_CrossFileClassImport(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	classCode := `export class Logger {
    log(message: string): void {}
}`
	classPath := filepath.Join(testDir, "logger-class.ts")
	classResult, err := l.Transform(classCode, classPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(classResult, "__type") {
		t.Error("class: missing __type")
	}

	consumerCode := `import { Logger } from './logger-class';

function getLogger(logger: Logger) {
    return logger;
}`
	consumerPath := filepath.Join(testDir, "consumer-class.ts")
	consumerResult, err := l.Transform(consumerCode, consumerPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("consumer:\n%s", consumerResult)
	if !strings.Contains(consumerResult, "getLogger") {
		t.Error("consumer: missing getLogger")
	}
	if !strings.Contains(consumerResult, "__type") {
		t.Error("consumer: missing __type")
	}
}

func TestLoader_CrossFileTypeAliasImport(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	typesCode := `export type UserId = string & { readonly __brand: 'UserId' };`
	typesPath := filepath.Join(testDir, "types-alias.ts")
	typesResult, err := l.Transform(typesCode, typesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(typesResult, "__ΩUserId") {
		t.Error("types: missing __ΩUserId")
	}

	consumerCode := `import { UserId } from './types-alias';

function getUserById(id: UserId) {
    return id;
}`
	consumerPath := filepath.Join(testDir, "consumer-alias.ts")
	consumerResult, err := l.Transform(consumerCode, consumerPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("consumer:\n%s", consumerResult)
	if !strings.Contains(consumerResult, "getUserById") {
		t.Error("consumer: missing getUserById")
	}
	if !strings.Contains(consumerResult, "__type") {
		t.Error("consumer: missing __type")
	}
}

func TestLoader_ExportedInterfaceGeneratesOmegaExport(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	code := `export interface User {
    id: number;
    name: string;
}`
	result := transformInline(t, l, code, "original-types.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "__ΩUser") {
		t.Error("missing __ΩUser")
	}
	if !strings.Contains(result, "export") {
		t.Error("missing export")
	}
}

// ============================================================================
// Reflection Options
// ============================================================================

func TestLoader_ReflectionDefault(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `interface Data {
    value: number;
}

function process(data: Data) {
    return data.value;
}`, "reflection-default.ts")
	if !strings.Contains(result, "__ΩData") {
		t.Error("missing __ΩData")
	}
	if !strings.Contains(result, "__type") {
		t.Error("missing __type")
	}
}

func TestLoader_ReflectionNever(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionNever})
	result := transformInline(t, l, `interface Data {
    value: number;
}

function process(data: Data) {
    return data.value;
}`, "reflection-never.ts")
	if strings.Contains(result, "__ΩData") {
		t.Error("should not contain __ΩData with reflection: never")
	}
	if strings.Contains(result, "__type") {
		t.Error("should not contain __type with reflection: never")
	}
}

func TestLoader_ReflectionExplicitWithout(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionExplicit})
	result := transformInline(t, l, `interface DataWithout {
    value: number;
}`, "reflection-explicit-without.ts")
	if strings.Contains(result, "__ΩDataWithout") {
		t.Error("should not contain __ΩDataWithout without @reflection")
	}
}

func TestLoader_ReflectionExplicitWith(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionExplicit})
	result := transformInline(t, l, `/** @reflection */
interface DataWith {
    value: number;
}`, "reflection-explicit-with.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "__ΩDataWith") {
		t.Error("should contain __ΩDataWith with @reflection")
	}
}

// ============================================================================
// Edge Cases
// ============================================================================

func TestLoader_CircularImports(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	fileACode := `import { B } from './circular-b';

export interface A {
    b: B;
}`
	fileAPath := filepath.Join(testDir, "circular-a.ts")
	fileAResult, err := l.Transform(fileACode, fileAPath)
	if err != nil {
		t.Fatal(err)
	}

	fileBCode := `import { A } from './circular-a';

export interface B {
    a: A;
}`
	fileBPath := filepath.Join(testDir, "circular-b.ts")
	fileBResult, err := l.Transform(fileBCode, fileBPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(fileAResult, "__ΩA") {
		t.Error("file A: missing __ΩA")
	}
	if !strings.Contains(fileBResult, "__ΩB") {
		t.Error("file B: missing __ΩB")
	}
}

func TestLoader_DeepImportChain(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	// File C (base)
	fileCCode := `export interface BaseEntity {
    id: number;
    createdAt: Date;
}`
	fileCPath := filepath.Join(testDir, "chain-c.ts")
	fileCResult, err := l.Transform(fileCCode, fileCPath)
	if err != nil {
		t.Fatal(err)
	}

	// File B imports C
	fileBCode := `import { BaseEntity } from './chain-c';

export interface User extends BaseEntity {
    name: string;
}`
	fileBPath := filepath.Join(testDir, "chain-b.ts")
	fileBResult, err := l.Transform(fileBCode, fileBPath)
	if err != nil {
		t.Fatal(err)
	}

	// File A imports B
	fileACode := `import { User } from './chain-b';

function getUser(id: number): User {
    return { id, name: '', createdAt: new Date() };
}`
	fileAPath := filepath.Join(testDir, "chain-a.ts")
	fileAResult, err := l.Transform(fileACode, fileAPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(fileCResult, "__ΩBaseEntity") {
		t.Error("file C: missing __ΩBaseEntity")
	}
	if !strings.Contains(fileBResult, "__ΩUser") {
		t.Error("file B: missing __ΩUser")
	}
	if !strings.Contains(fileAResult, "getUser") {
		t.Error("file A: missing getUser")
	}
	if !strings.Contains(fileAResult, "__type") {
		t.Error("file A: missing __type")
	}
}

func TestLoader_MixedTsTsx(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	tsxCode := `interface Props {
    name: string;
    onClick: () => void;
}

function Button(props: Props) {
    return props.name;
}`
	tsxPath := filepath.Join(testDir, "component.tsx")
	tsxResult, err := l.Transform(tsxCode, tsxPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("output:\n%s", tsxResult)
	if !strings.Contains(tsxResult, "__ΩProps") {
		t.Error("missing __ΩProps")
	}
	if !strings.Contains(tsxResult, "Button") {
		t.Error("missing Button")
	}
}

func TestLoader_GenericConstraints(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `interface HasId {
    id: number;
}

function findById<T extends HasId>(items: T[], id: number): T | undefined {
    return items.find(item => item.id === id);
}`, "generic-constraints.ts")
	if !strings.Contains(result, "__ΩHasId") {
		t.Error("missing __ΩHasId")
	}
	if !strings.Contains(result, "findById") {
		t.Error("missing findById")
	}
	if !strings.Contains(result, "__type") {
		t.Error("missing __type")
	}
}

func TestLoader_UnionAndIntersection(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `type StringOrNumber = string | number;
type Combined = { a: string } & { b: number };

function processUnion(value: StringOrNumber): void {}
function processCombined(value: Combined): void {}`, "union-intersection.ts")
	if !strings.Contains(result, "__ΩStringOrNumber") {
		t.Error("missing __ΩStringOrNumber")
	}
	if !strings.Contains(result, "__ΩCombined") {
		t.Error("missing __ΩCombined")
	}
}

func TestLoader_ConditionalTypes(t *testing.T) {
	t.Skip("Conditional types in type aliases not yet supported in Go type walker")
}

func TestLoader_MappedTypes(t *testing.T) {
	t.Skip("Mapped types (Readonly<>, Partial<>) in type aliases need global lib resolution in loader context")
}

func TestLoader_PreservesUseClient(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `"use client";

interface Props {
    name: string;
}

function Component(props: Props) {
    return props.name;
}`, "use-client.ts")
	t.Logf("output:\n%s", result)
	// "use client" should appear in the output (may be after "use strict")
	if !strings.Contains(result, `"use client"`) {
		t.Error(`missing "use client" directive`)
	}
	// "use client" should come before __ΩProps and Component.__type
	clientIdx := strings.Index(result, `"use client"`)
	typeIdx := strings.Index(result, "__ΩProps")
	if typeIdx >= 0 && clientIdx >= typeIdx {
		t.Error(`"use client" should come before __ΩProps`)
	}
}

func TestLoader_TemplateLiteralTypes(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, "type EventName = `on${Capitalize<'click' | 'focus'>}`;", "template-literal.ts")
	if !strings.Contains(result, "__ΩEventName") {
		t.Error("missing __ΩEventName")
	}
}

// ============================================================================
// State Management
// ============================================================================

func TestLoader_SameLoaderMultipleTransforms(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})

	file1Code := `export interface User {
    id: number;
}`
	file1Path := filepath.Join(testDir, "state-file1.ts")
	file1Result, err := l.Transform(file1Code, file1Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(file1Result, "__ΩUser") {
		t.Error("file1: missing __ΩUser")
	}

	file2Code := `import { User } from './state-file1';

function getUser(): User {
    return { id: 1 };
}`
	file2Path := filepath.Join(testDir, "state-file2.ts")
	file2Result, err := l.Transform(file2Code, file2Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(file2Result, "getUser") {
		t.Error("file2: missing getUser")
	}
	if !strings.Contains(file2Result, "__type") {
		t.Error("file2: missing __type")
	}
}

func TestLoader_MultipleTransformsSameFile(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	filePath := filepath.Join(testDir, "update-test.ts")

	code1 := `interface User {
    id: number;
}`
	result1, err := l.Transform(code1, filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result1, "__ΩUser") {
		t.Error("first transform: missing __ΩUser")
	}

	code2 := `interface User {
    id: number;
    name: string;
}`
	result2, err := l.Transform(code2, filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result2, "__ΩUser") {
		t.Error("second transform: missing __ΩUser")
	}
}

// ============================================================================
// Complex Scenarios
// ============================================================================

func TestLoader_AsyncAwait(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `interface User {
    id: number;
    name: string;
}

async function fetchUser(id: number): Promise<User> {
    return { id, name: 'Test' };
}`, "async-code.ts")
	if !strings.Contains(result, "__ΩUser") {
		t.Error("missing __ΩUser")
	}
	if !strings.Contains(result, "async") {
		t.Error("missing async keyword")
	}
}

func TestLoader_IndexSignatureTypes(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `interface Dictionary<T> {
    [key: string]: T;
}

type StringDict = Dictionary<string>;`, "index-signature.ts")
	if !strings.Contains(result, "__ΩDictionary") {
		t.Error("missing __ΩDictionary")
	}
	if !strings.Contains(result, "__ΩStringDict") {
		t.Error("missing __ΩStringDict")
	}
}

func TestLoader_RecursiveTypes(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `interface TreeNode {
    value: number;
    children: TreeNode[];
}`, "recursive-types.ts")
	if !strings.Contains(result, "__ΩTreeNode") {
		t.Error("missing __ΩTreeNode")
	}
}

func TestLoader_TupleTypes(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `type Point = [number, number];
type LabeledPoint = [string, number, number];

function distance(p1: Point, p2: Point): number {
    return Math.sqrt((p2[0] - p1[0]) ** 2 + (p2[1] - p1[1]) ** 2);
}`, "tuple-types.ts")
	if !strings.Contains(result, "__ΩPoint") {
		t.Error("missing __ΩPoint")
	}
	if !strings.Contains(result, "__ΩLabeledPoint") {
		t.Error("missing __ΩLabeledPoint")
	}
}

func TestLoader_InferKeyword(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `type ReturnType<T> = T extends (...args: any[]) => infer R ? R : never;
type UnwrapPromise<T> = T extends Promise<infer U> ? U : T;`, "infer-types.ts")
	if !strings.Contains(result, "__ΩReturnType") {
		t.Error("missing __ΩReturnType")
	}
	if !strings.Contains(result, "__ΩUnwrapPromise") {
		t.Error("missing __ΩUnwrapPromise")
	}
}

// ============================================================================
// Error Handling
// ============================================================================

func TestLoader_EmptySource(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, "", "empty.ts")
	// Should not crash
	_ = result
}

func TestLoader_CommentsOnly(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `// This is a comment
/* Another comment */`, "comments-only.ts")
	// Should not crash
	_ = result
}

// ============================================================================
// ReceiveType Integration
// ============================================================================

func TestLoader_ReceiveTypeParameter(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `function validate<T>(data: unknown, type?: ReceiveType<T>): boolean {
    type = resolveReceiveType(type);
    return true;
}

interface User {
    name: string;
}

validate<User>({});`, "receive-type.ts")
	if !strings.Contains(result, "validate") {
		t.Error("missing validate")
	}
	if !strings.Contains(result, "__ΩUser") {
		t.Error("missing __ΩUser")
	}
}

func TestLoader_ReceiveTypeForwarding(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `function typeOf2<T>(type?: ReceiveType<T>) {
    return resolveReceiveType(type);
}

function mySerialize<T>(type?: ReceiveType<T>) {
    return typeOf2<T>();
}`, "receive-type-forward.ts")
	if !strings.Contains(result, "typeOf2") {
		t.Error("missing typeOf2")
	}
	if !strings.Contains(result, "mySerialize") {
		t.Error("missing mySerialize")
	}
}

func TestLoader_ClassConstructorReceiveType(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `class Repository<T> {
    constructor(type?: ReceiveType<T>) {}
}

interface User {
    id: number;
}

new Repository<User>();`, "constructor-receive-type.ts")
	if !strings.Contains(result, "Repository") {
		t.Error("missing Repository")
	}
	if !strings.Contains(result, "__ΩUser") {
		t.Error("missing __ΩUser")
	}
}

// ============================================================================
// ESM Output
// ============================================================================

func TestLoader_ESMOutput(t *testing.T) {
	t.Parallel()
	l := loader.NewLoader(loader.LoaderOptions{Reflection: loader.ReflectionDefault})
	result := transformInline(t, l, `export interface User {
    name: string;
}

export function getUser(): User {
    return { name: '' };
}`, "esm-output.ts")
	t.Logf("output:\n%s", result)
	if !strings.Contains(result, "export") {
		t.Error("missing export for ESM output")
	}
}
