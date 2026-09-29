package runtyped

import "strings"

// ParseJSDocAttributeFromText extracts the content of a JSDoc attribute tag
// (e.g. `@attr attr-content`) from a JSDoc comment's raw text.
//
// Port of the JS reference's parseJSDocAttributeFromText
// (packages/type-compiler/src/reflection-ast.ts). Deliberately regex-free,
// like the original.
//
// Returns (content, true) when the attribute is found; ("", false) when the
// attribute does not appear at all; ("", true) when it appears with no content.
func ParseJSDocAttributeFromText(comment string, attribute string) (string, bool) {
	tag := "@" + attribute

	index := strings.Index(comment, tag+" ")
	if index == -1 {
		start := 0
		for {
			withoutContent := strings.Index(comment[start:], tag)
			if withoutContent == -1 {
				return "", false
			}
			withoutContent += start
			// make sure next character is space, newline, tab, or end of comment
			nextIdx := withoutContent + len(attribute) + 1
			if nextIdx >= len(comment) {
				return "", true
			}
			nextCharacter := comment[nextIdx]
			if nextCharacter == ' ' || nextCharacter == '\n' || nextCharacter == '\r' || nextCharacter == '\t' {
				return "", true
			}
			start = nextIdx
		}
	}

	start := index + len(attribute) + 2
	// end is either next attribute @ or end of comment.
	nextAttribute := strings.Index(comment[start:], "@")
	endOfComment := strings.Index(comment[start:], "*/")
	var end int
	switch {
	case nextAttribute == -1 && endOfComment == -1:
		end = len(comment)
	case nextAttribute == -1:
		end = start + endOfComment
	case endOfComment == -1:
		end = start + nextAttribute
	default:
		if nextAttribute < endOfComment {
			end = start + nextAttribute
		} else {
			end = start + endOfComment
		}
	}
	content := strings.TrimSpace(comment[start:end])

	// make sure multiline comments are supported: each line is trimmed and
	// leading `\s*\*` removed.
	lines := strings.Split(content, "\n")
	for i, v := range lines {
		indexOfStar := strings.Index(v, "*")
		if indexOfStar == -1 {
			lines[i] = strings.TrimSpace(v)
		} else {
			lines[i] = strings.TrimSpace(v[indexOfStar+1:])
		}
	}
	return strings.Join(lines, "\n"), true
}
