package frames

import (
	"strings"
	"testing"
)

func TestWrapTextClampsLines(t *testing.T) {
	face, err := Face(40)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = face.Close() }()
	const width = 200
	lines := WrapText(face, "Phở bò tái chín nạm gầu gân sách bò viên", width, 2)
	if len(lines) != 2 || !strings.HasSuffix(lines[1], Ellipsis) {
		t.Fatalf("lines = %q, want two lines ending in an ellipsis", lines)
	}
	for _, line := range WrapText(face, strings.Repeat("W", 30), width, 10) {
		if TextWidth(face, line) > width {
			t.Fatalf("line %q is wider than %d", line, width)
		}
	}
	if got := WrapText(face, "Pizza", width, 2); len(got) != 1 || got[0] != "Pizza" {
		t.Fatalf("short label = %q", got)
	}
}
