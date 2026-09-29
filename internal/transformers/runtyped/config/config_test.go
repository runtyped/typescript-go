package config

import (
	"strings"
	"testing"

	"github.com/microsoft/TypeScript/tsc/internal/tsoptions/tsoptionstest"
)

func buildHost(files map[string]string) *tsoptionstest.VfsParseConfigHost {
	// VFS requires rooted paths — prefix all with "/"
	rooted := make(map[string]string, len(files))
	for k, v := range files {
		if !strings.HasPrefix(k, "/") {
			k = "/" + k
		}
		rooted[k] = v
	}
	return tsoptionstest.NewVFSParseConfigHost(rooted, "/", true)
}

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"test.ts", []string{"test.ts"}, true},
		{"test.ts", []string{"*.ts"}, true},
		{"test.ts", []string{"**/*.ts"}, true},

		{"/app/src/tests/test.ts", []string{"/app/src/tests/test.ts"}, true},
		{"/app/src/tests/test.ts", []string{"/app/src/tests/*.ts"}, true},
		{"/app/src/tests/test.ts", []string{"/app/src/tests/**/*.ts"}, true},

		{"/app/src/tests/test.ts", []string{"/app/src/tests/**/test.ts"}, true},
		{"/app/src/tests/bla/test.ts", []string{"/app/src/tests/**/test.ts"}, true},
		{"/app/src/tests/bla/bla2/test.ts", []string{"/app/src/tests/**/test.ts"}, true},
	}
	for _, tt := range tests {
		got := PatternMatch(tt.path, tt.patterns)
		if got != tt.want {
			t.Errorf("PatternMatch(%q, %v) = %v, want %v", tt.path, tt.patterns, got, tt.want)
		}
	}
}

func TestEmptyConfig(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.json": `{}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	if resolver.Config.Path != "/tsconfig.json" {
		t.Errorf("Config.Path = %q, want %q", resolver.Config.Path, "/tsconfig.json")
	}
	if resolver.Config.MergeStrategy != "merge" {
		t.Errorf("Config.MergeStrategy = %q, want %q", resolver.Config.MergeStrategy, "merge")
	}
	if resolver.Config.Reflection != nil {
		t.Errorf("Config.Reflection = %v, want nil", resolver.Config.Reflection)
	}
	if len(resolver.Config.Exclude) != len(DefaultExcluded) {
		t.Errorf("Config.Exclude len = %d, want %d", len(resolver.Config.Exclude), len(DefaultExcluded))
	}

	match := resolver.Match("test.ts")
	if match.Mode != ModeNever {
		t.Errorf("match('test.ts').Mode = %q, want %q", match.Mode, ModeNever)
	}
	if match.TSConfigPath != "/tsconfig.json" {
		t.Errorf("match('test.ts').TSConfigPath = %q, want %q", match.TSConfigPath, "/tsconfig.json")
	}

	// Default excludes should match
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
	assertMode(t, resolver, "lib.dom.iterable.d.ts", ModeNever)
	assertMode(t, resolver, "lib.es2017.typedarrays.d.ts", ModeNever)
}

func TestSimpleConfig(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.json": `{"reflection": true}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	if resolver.Config.Reflection != ModeDefault {
		t.Errorf("Config.Reflection = %v, want %q", resolver.Config.Reflection, ModeDefault)
	}

	match := resolver.Match("test.ts")
	if match.Mode != ModeDefault {
		t.Errorf("match('test.ts').Mode = %q, want %q", match.Mode, ModeDefault)
	}
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestSimpleConfigWithExclude(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.json": `{"deepkitCompilerOptions": {"reflection": true, "exclude": ["test.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	// reflection should be "default"
	if resolver.Config.Reflection != ModeDefault {
		t.Errorf("Config.Reflection = %v, want %q", resolver.Config.Reflection, ModeDefault)
	}

	// exclude should have default + test.ts
	if !contains(resolver.Config.Exclude, "test.ts") {
		t.Errorf("Config.Exclude should contain 'test.ts', got %v", resolver.Config.Exclude)
	}

	match := resolver.Match("test.ts")
	if match.Mode != ModeNever {
		t.Errorf("match('test.ts').Mode = %q, want %q", match.Mode, ModeNever)
	}
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestDisableParent(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.base.json": `{"reflection": true}`,
		"/tsconfig.json":      `{"extends": "./tsconfig.base.json", "reflection": false}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	if resolver.Config.Reflection != ModeNever {
		t.Errorf("Config.Reflection = %v, want %q", resolver.Config.Reflection, ModeNever)
	}

	match := resolver.Match("test.ts")
	if match.Mode != ModeNever {
		t.Errorf("match('test.ts').Mode = %q, want %q", match.Mode, ModeNever)
	}
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestReplaceStrategyDoesNotReplaceDefaultExcludes(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.json": `{"deepkitCompilerOptions": {"reflection": true, "mergeStrategy": "replace", "exclude": ["test.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	if resolver.Config.MergeStrategy != "replace" {
		t.Errorf("Config.MergeStrategy = %q, want %q", resolver.Config.MergeStrategy, "replace")
	}
	if resolver.Config.Reflection != ModeDefault {
		t.Errorf("Config.Reflection = %v, want %q", resolver.Config.Reflection, ModeDefault)
	}

	// Default excludes should still be present even with replace strategy
	for _, de := range DefaultExcluded {
		if !contains(resolver.Config.Exclude, de) {
			t.Errorf("Config.Exclude should contain default exclude %q, got %v", de, resolver.Config.Exclude)
		}
	}
	if !contains(resolver.Config.Exclude, "test.ts") {
		t.Errorf("Config.Exclude should contain 'test.ts', got %v", resolver.Config.Exclude)
	}

	assertMode(t, resolver, "test.ts", ModeNever)
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestReplaceParentConfigExclude(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.json": `{"deepkitCompilerOptions": {"reflection": true, "exclude": ["test.ts"]}}`,
		"/tsconfig2.json": `{"extends": "./tsconfig.json", "deepkitCompilerOptions": {"mergeStrategy": "replace", "exclude": ["test2.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig2.json")

	if resolver.Config.MergeStrategy != "replace" {
		t.Errorf("Config.MergeStrategy = %q, want %q", resolver.Config.MergeStrategy, "replace")
	}
	if resolver.Config.Reflection != ModeDefault {
		t.Errorf("Config.Reflection = %v, want %q", resolver.Config.Reflection, ModeDefault)
	}

	// With replace, the parent's exclude (test.ts) should be replaced by child's (test2.ts)
	for _, de := range DefaultExcluded {
		if !contains(resolver.Config.Exclude, de) {
			t.Errorf("Config.Exclude should contain default exclude %q, got %v", de, resolver.Config.Exclude)
		}
	}
	if !contains(resolver.Config.Exclude, "test2.ts") {
		t.Errorf("Config.Exclude should contain 'test2.ts', got %v", resolver.Config.Exclude)
	}

	assertMode(t, resolver, "test.ts", ModeDefault)
	assertMode(t, resolver, "test2.ts", ModeNever)
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestExtendReflectionArray(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.base.json": `{"deepkitCompilerOptions": {"reflection": ["test.ts"]}}`,
		"/tsconfig.json":      `{"extends": "./tsconfig.base.json", "deepkitCompilerOptions": {"reflection": ["test2.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	reflArr, ok := resolver.Config.Reflection.([]string)
	if !ok {
		t.Fatalf("Config.Reflection should be []string, got %T", resolver.Config.Reflection)
	}
	if len(reflArr) != 2 {
		t.Fatalf("Config.Reflection should have 2 entries, got %d: %v", len(reflArr), reflArr)
	}
	if !contains(reflArr, "test.ts") {
		t.Errorf("Config.Reflection should contain 'test.ts', got %v", reflArr)
	}
	if !contains(reflArr, "test2.ts") {
		t.Errorf("Config.Reflection should contain 'test2.ts', got %v", reflArr)
	}

	assertMode(t, resolver, "test.ts", ModeDefault)
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestReplaceReflectionArray(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.base.json": `{"deepkitCompilerOptions": {"reflection": ["test.ts"]}}`,
		"/tsconfig.json":      `{"extends": "./tsconfig.base.json", "deepkitCompilerOptions": {"mergeStrategy": "replace", "reflection": ["test2.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	reflArr, ok := resolver.Config.Reflection.([]string)
	if !ok {
		t.Fatalf("Config.Reflection should be []string, got %T", resolver.Config.Reflection)
	}
	if len(reflArr) != 1 {
		t.Fatalf("Config.Reflection should have 1 entry, got %d: %v", len(reflArr), reflArr)
	}
	if reflArr[0] != "test2.ts" {
		t.Errorf("Config.Reflection[0] = %q, want %q", reflArr[0], "test2.ts")
	}

	assertMode(t, resolver, "test.ts", ModeNever)
	assertMode(t, resolver, "test2.ts", ModeDefault)
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestCircularExtend(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.base.json": `{"extends": "./tsconfig.json", "deepkitCompilerOptions": {"reflection": ["test.ts"]}}`,
		"/tsconfig.json":      `{"extends": "./tsconfig.base.json", "deepkitCompilerOptions": {"reflection": ["test2.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	reflArr, ok := resolver.Config.Reflection.([]string)
	if !ok {
		t.Fatalf("Config.Reflection should be []string, got %T", resolver.Config.Reflection)
	}
	if !contains(reflArr, "test.ts") {
		t.Errorf("Config.Reflection should contain 'test.ts', got %v", reflArr)
	}
	if !contains(reflArr, "test2.ts") {
		t.Errorf("Config.Reflection should contain 'test2.ts', got %v", reflArr)
	}

	assertMode(t, resolver, "test.ts", ModeDefault)
	assertMode(t, resolver, "test2.ts", ModeDefault)
	assertMode(t, resolver, "lib.dom.d.ts", ModeNever)
}

func TestNegativeMatch1(t *testing.T) {
	host := buildHost(map[string]string{
		"/app/tsconfig.json": `{"deepkitCompilerOptions": {"reflection": ["model/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"/app/test.ts"}, "/app/tsconfig.json")

	assertMode(t, resolver, "/app/model/test.ts", ModeDefault)
	assertMode(t, resolver, "/app/model/controller/test.controller.ts", ModeDefault)
	assertMode(t, resolver, "/app/external/file.ts", ModeNever)
}

func TestNegativeMatch2(t *testing.T) {
	host := buildHost(map[string]string{
		"/path/portal/tsconfig.json": `{"deepkitCompilerOptions": {"reflection": ["server/controllers/**/*.ts", "server/services/**/*.ts", "server/dao/**/*.ts", "!server/dao/mongoose.ts", "shared/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"/path/portal/test.ts"}, "/path/portal/tsconfig.json")

	assertMode(t, resolver, "/path/portal/server/dao/models.ts", ModeDefault)
	assertMode(t, resolver, "/path/portal/server/dao/mongoose.ts", ModeNever)
}

func TestNegativeMatch3(t *testing.T) {
	host := buildHost(map[string]string{
		"/path/portal/tsconfig.json": `{"deepkitCompilerOptions": {"reflection": ["!src/lib/graphql/**/*.ts", "src/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"/path/portal/src/index.ts"}, "/path/portal/tsconfig.json")

	assertMode(t, resolver, "/path/portal/src/lib/types.ts", ModeDefault)
	assertMode(t, resolver, "/path/portal/src/lib/graphql/generated.ts", ModeNever)
}

func TestNegativeMatch4(t *testing.T) {
	host := buildHost(map[string]string{
		"/path/portal/tsconfig.json": `{"deepkitCompilerOptions": {"reflection": ["!src/lib/graphql/generated.ts", "src/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"/path/portal/src/index.ts"}, "/path/portal/tsconfig.json")

	assertMode(t, resolver, "/path/portal/src/lib/types.ts", ModeDefault)
	assertMode(t, resolver, "/path/portal/src/lib/graphql/generated.ts", ModeNever)
}

// === Extends array support (TypeScript 5.0+) ===

func TestExtendsAsArrayWithMultipleConfigs(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.base1.json": `{"deepkitCompilerOptions": {"reflection": ["src/models/**/*.ts"]}}`,
		"/tsconfig.base2.json": `{"deepkitCompilerOptions": {"reflection": ["src/services/**/*.ts"]}}`,
		"/tsconfig.json":      `{"extends": ["./tsconfig.base1.json", "./tsconfig.base2.json"], "deepkitCompilerOptions": {"reflection": ["src/controllers/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	reflArr, ok := resolver.Config.Reflection.([]string)
	if !ok {
		t.Fatalf("Config.Reflection should be []string, got %T", resolver.Config.Reflection)
	}
	// Later extends in array take precedence (base2 comes before base1 in merged result)
	// Order: services (base2), models (base1), controllers (child)
	if len(reflArr) != 3 {
		t.Fatalf("Config.Reflection should have 3 entries, got %d: %v", len(reflArr), reflArr)
	}

	assertMode(t, resolver, "/src/models/user.ts", ModeDefault)
	assertMode(t, resolver, "/src/services/auth.ts", ModeDefault)
	assertMode(t, resolver, "/src/controllers/home.ts", ModeDefault)
	assertMode(t, resolver, "/src/utils/helpers.ts", ModeNever)
}

func TestExtendsWithNestedExtends(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.grandparent.json": `{"deepkitCompilerOptions": {"reflection": ["src/core/**/*.ts"]}}`,
		"/tsconfig.parent1.json":     `{"extends": "./tsconfig.grandparent.json", "deepkitCompilerOptions": {"reflection": ["src/models/**/*.ts"]}}`,
		"/tsconfig.parent2.json":     `{"deepkitCompilerOptions": {"reflection": ["src/services/**/*.ts"]}}`,
		"/tsconfig.json":            `{"extends": ["./tsconfig.parent1.json", "./tsconfig.parent2.json"], "deepkitCompilerOptions": {"reflection": ["src/controllers/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	reflArr, ok := resolver.Config.Reflection.([]string)
	if !ok {
		t.Fatalf("Config.Reflection should be []string, got %T", resolver.Config.Reflection)
	}
	// Processing order: parent1 -> grandparent -> parent2 -> child
	if len(reflArr) != 4 {
		t.Fatalf("Config.Reflection should have 4 entries, got %d: %v", len(reflArr), reflArr)
	}

	assertMode(t, resolver, "/src/core/base.ts", ModeDefault)
	assertMode(t, resolver, "/src/models/user.ts", ModeDefault)
	assertMode(t, resolver, "/src/services/auth.ts", ModeDefault)
	assertMode(t, resolver, "/src/controllers/home.ts", ModeDefault)
	assertMode(t, resolver, "/src/utils/helpers.ts", ModeNever)
}

func TestCircularReferenceDetectionWithExtendsArray(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.base1.json": `{"extends": ["./tsconfig.json"], "deepkitCompilerOptions": {"reflection": ["src/models/**/*.ts"]}}`,
		"/tsconfig.base2.json": `{"extends": ["./tsconfig.base1.json"], "deepkitCompilerOptions": {"reflection": ["src/services/**/*.ts"]}}`,
		"/tsconfig.json":      `{"extends": ["./tsconfig.base1.json", "./tsconfig.base2.json"], "deepkitCompilerOptions": {"reflection": ["src/controllers/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	if resolver.Config.Path != "/tsconfig.json" {
		t.Errorf("Config.Path = %q, want %q", resolver.Config.Path, "/tsconfig.json")
	}
	assertMode(t, resolver, "/src/models/user.ts", ModeDefault)
	assertMode(t, resolver, "/src/services/auth.ts", ModeDefault)
	assertMode(t, resolver, "/src/controllers/home.ts", ModeDefault)
}

func TestExtendsAsSingleStringBackwardCompat(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.base.json": `{"deepkitCompilerOptions": {"reflection": ["src/models/**/*.ts"]}}`,
		"/tsconfig.json":      `{"extends": "./tsconfig.base.json", "deepkitCompilerOptions": {"reflection": ["src/controllers/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	reflArr, ok := resolver.Config.Reflection.([]string)
	if !ok {
		t.Fatalf("Config.Reflection should be []string, got %T", resolver.Config.Reflection)
	}
	if len(reflArr) != 2 {
		t.Fatalf("Config.Reflection should have 2 entries, got %d: %v", len(reflArr), reflArr)
	}

	assertMode(t, resolver, "/src/models/user.ts", ModeDefault)
	assertMode(t, resolver, "/src/controllers/home.ts", ModeDefault)
}

func TestExtendsArrayWithEmptyArray(t *testing.T) {
	host := buildHost(map[string]string{
		"/tsconfig.json": `{"extends": [], "deepkitCompilerOptions": {"reflection": ["src/**/*.ts"]}}`,
	})

	cache := map[string]*ConfigResolver{}
	resolver := GetConfigResolver(cache, host, map[string]any{}, struct{ FileName string }{"test.ts"}, "/tsconfig.json")

	reflArr, ok := resolver.Config.Reflection.([]string)
	if !ok {
		t.Fatalf("Config.Reflection should be []string, got %T", resolver.Config.Reflection)
	}
	if len(reflArr) != 1 {
		t.Fatalf("Config.Reflection should have 1 entry, got %d: %v", len(reflArr), reflArr)
	}

	assertMode(t, resolver, "/src/index.ts", ModeDefault)
}

// === Helpers ===

func assertMode(t *testing.T, resolver *ConfigResolver, path string, expected Mode) {
	t.Helper()
	got := resolver.Match(path)
	if got.Mode != expected {
		t.Errorf("match(%q).Mode = %q, want %q", path, got.Mode, expected)
	}
}

func contains(arr []string, s string) bool {
	for _, v := range arr {
		if v == s {
			return true
		}
	}
	return false
}
