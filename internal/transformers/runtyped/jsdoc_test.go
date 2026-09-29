package runtyped

import "testing"

// Port of packages/type-compiler/tests/ast.spec.ts 'parse js doc attribute'.
func TestParseJSDocAttributeFromText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		comment   string
		attribute string
		want      string
		wantOK    bool
	}{
		{
			name:      "attribute with content",
			comment:   "/**\n    * @attr attr\n    */",
			attribute: "attr",
			want:      "attr",
			wantOK:    true,
		},
		{
			name:      "attribute not present",
			comment:   "/**\n    * @attr attr\n    */",
			attribute: "attr2",
			want:      "",
			wantOK:    false,
		},
		{
			name:      "second of two attributes",
			comment:   "/**\n    * @attr2 attr2-content\n    * @attr attr-content\n    */",
			attribute: "attr",
			want:      "attr-content",
			wantOK:    true,
		},
		{
			name:      "attribute with no content",
			comment:   "/**\n    * @attr\n    */",
			attribute: "attr",
			want:      "",
			wantOK:    true,
		},
		{
			name:      "attribute with no content after another attribute",
			comment:   "/**\n    * @attr2 attr2-content\n    * @attr\n    */",
			attribute: "attr",
			want:      "",
			wantOK:    true,
		},
		{
			name:      "multiline content strips leading stars",
			comment:   "/**\n    * @attr first line\n    * second line\n    */",
			attribute: "attr",
			want:      "first line\nsecond line",
			wantOK:    true,
		},
		{
			name:      "longer attribute name is not matched by prefix",
			comment:   "/**\n    * @attr2 content2\n    */",
			attribute: "attr",
			want:      "",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseJSDocAttributeFromText(tt.comment, tt.attribute)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.wantOK, got)
			}
			if got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}
