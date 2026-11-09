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
func TestToolActivityIsDistinctFromAssistantText(t *testing.T) {
	input := "# Plan\n\nChecks next.\n\n[running] bash: go test ./...\n[finished] bash: go test ./...\n[failed] bash: go vet ./...\n\nAll good.\n"
	lines := renderedOutput(input, 80)
	all := strings.Join(lines, "\n")
	plain := ansi.Strip(all)
	for _, want := range []string{"Plan", "Checks next", "All good", "▎", "✓ finished", "bash", "go test ./...", "✗ failed", "go vet ./..."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	for _, unwanted := range []string{"[running]", "[finished]", "[failed]", "⠿"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("raw or stale marker %q in %q", unwanted, plain)
		}
	}
	if got := strings.Count(plain, "go test ./..."); got != 1 {
		t.Fatalf("paired tool call rendered %d times: %q", got, plain)
	}
	if !strings.Contains(all, "\x1b[") {
		t.Fatal("tool activity was not styled")
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("line exceeds width: %d %q", width, line)
		}
	}
}

func TestToolActivityRunningRowStaysVisible(t *testing.T) {
	plain := ansi.Strip(strings.Join(renderedOutput("work\n[running] read: main.go\n", 60), "\n"))
	for _, want := range []string{"⠿ running", "read", "main.go"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
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
