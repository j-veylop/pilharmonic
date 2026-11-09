package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownRenderingAndBounds(t *testing.T) {
	input := "# Heading\n\n**Bold text**\n\n- item\n\n```go\n" + strings.Repeat("verylongtoken", 20) + "\n```\n"
	for _, width := range []int{8, 20, 50} {
		lines := renderedOutput(input, width)
		plain := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(strings.Join(strings.Fields(plain), ""), "Heading") || !strings.Contains(plain, "item") || strings.Contains(plain, "```") || strings.Contains(plain, "**Bold") {
			t.Fatalf("Markdown not rendered: %q", plain)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d: %q", width, line)
			}
		}
	}
}
func TestMarkdownSanitizesControlsAndPreservesLogs(t *testing.T) {
	lines := renderedOutput("line-01\nline-02\n\x1b[999Cunsafe\ttext", 40)
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "\x1b[999C") || strings.Contains(all, "\t") {
		t.Fatalf("unsafe control: %q", all)
	}
	first, second := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "line-01") {
			first = i
		}
		if strings.Contains(line, "line-02") {
			second = i
		}
	}
	if first < 0 || second <= first {
		t.Fatalf("log lines collapsed: %q", all)
	}
	lines[0] = "mutated"
	if renderedOutput("line-01\nline-02\n\x1b[999Cunsafe\ttext", 40)[0] == "mutated" {
		t.Fatal("caller mutated cache")
	}
}
