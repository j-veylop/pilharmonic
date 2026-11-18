package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
)

// renderTestOutput renders a transcript through the same cache the view uses.
// Thinking stays hidden and the running glyph is the default unless a test
// asks for otherwise.
func renderTestOutput(text string, width int) []string {
	return (&markdownCache{}).render(text, width, defaultRunningGlyph, false)
}

func renderTestOutputWithGlyph(text string, width int, glyph string) []string {
	return (&markdownCache{}).render(text, width, glyph, false)
}

func renderTestOutputWithThinking(text string, width int, glyph string, showThinking bool) []string {
	return (&markdownCache{}).render(text, width, glyph, showThinking)
}

func TestMarkdownRenderingAndBounds(t *testing.T) {
	input := "# Heading\n\n**Bold text**\n\n- item\n\n```go\n" + strings.Repeat("verylongtoken", 20) + "\n```\n"
	for _, width := range []int{8, 20, 50} {
		lines := renderTestOutput(input, width)
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
func TestCodeKeepsTheCanvasBackground(t *testing.T) {
	input := "text `inline` here\n\n```go\npackage main\n\nfunc main() {}\n```\n"
	all := strings.Join(renderTestOutput(input, 40), "\n")
	if strings.Contains(all, "48;5;") || strings.Contains(all, "48;2;") {
		t.Fatalf("code painted a background over the canvas: %q", all)
	}
	if !strings.Contains(ansi.Strip(all), "package main") {
		t.Fatalf("code block content missing: %q", all)
	}
}

func TestToolActivityIsDistinctFromAssistantText(t *testing.T) {
	input := "# Plan\n\nChecks next.\n\n[running] bash: go test ./...\n[finished] bash: go test ./...\n[failed] bash: go vet ./...\n\nAll good.\n"
	lines := renderTestOutput(input, 80)
	all := strings.Join(lines, "\n")
	plain := ansi.Strip(all)
	for _, want := range []string{"Plan", "Checks next", "All good", "▎", "✓  bash  go test ./...", "✗  bash  go vet ./..."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	for _, unwanted := range []string{"[running]", "[finished]", "[failed]", "⠿", "running", "finished", "failed"} {
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

func TestRunningToolGlyphAdvancesWithSpinner(t *testing.T) {
	m := model{runningFrame: 1}
	glyph := ansi.Strip(m.runningGlyph())
	if glyph != spinner.MiniDot.Frames[1] {
		t.Fatalf("spinner did not advance: %q", glyph)
	}
	plain := ansi.Strip(strings.Join(renderTestOutputWithGlyph("[running] read: animated.go\n", 60, glyph), "\n"))
	if !strings.Contains(plain, glyph+"  read  animated.go") {
		t.Fatalf("transcript did not use current frame %q: %q", glyph, plain)
	}
}

func TestThinkingBlocksAreHiddenByDefaultAndToggleable(t *testing.T) {
	input := "Before\n[thinking]\nprivate reasoning\nsecond line\n[/thinking]\nAfter\n"
	hidden := ansi.Strip(strings.Join(renderTestOutput(input, 80), "\n"))
	if strings.Contains(hidden, "private reasoning") || strings.Contains(hidden, "second line") {
		t.Fatalf("hidden thinking leaked text: %q", hidden)
	}
	for _, want := range []string{"Before", "After", "thinking hidden"} {
		if !strings.Contains(hidden, want) {
			t.Fatalf("missing %q in %q", want, hidden)
		}
	}
	shown := ansi.Strip(strings.Join(renderTestOutputWithThinking(input, 80, defaultRunningGlyph, true), "\n"))
	for _, want := range []string{"THINKING", "private reasoning", "second line", "Before", "After"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("missing %q in %q", want, shown)
		}
	}
	if strings.Contains(shown, "[thinking]") || strings.Contains(shown, "[/thinking]") {
		t.Fatalf("raw marker leaked: %q", shown)
	}
}

func TestThinkingBlocksAcceptClosingMarkerVariantsAndTruncatedTail(t *testing.T) {
	for _, input := range []string{
		"start\n[thinking]\nsecret\n[thinking end]\nend\n",
		"start\n[thinking_start]\nsecret\n[thinking_end]\nend\n",
		"start\n[thinking] secret\n[thinking] second secret\nend\n",
	} {
		hidden := ansi.Strip(strings.Join(renderTestOutput(input, 60), "\n"))
		if strings.Contains(hidden, "secret") || !strings.Contains(hidden, "start") || !strings.Contains(hidden, "end") {
			t.Fatalf("marker variant failed: %q", hidden)
		}
		shown := ansi.Strip(strings.Join(renderTestOutputWithThinking(input, 60, defaultRunningGlyph, true), "\n"))
		if !strings.Contains(shown, "secret") || !strings.Contains(shown, "THINKING") {
			t.Fatalf("shown marker variant failed: %q", shown)
		}
	}
	truncated := ansi.Strip(strings.Join(renderTestOutput("old tail\n[/thinking]\nvisible\n", 60), "\n"))
	if strings.Contains(truncated, "[/thinking]") || !strings.Contains(truncated, "visible") {
		t.Fatalf("truncated tail failed: %q", truncated)
	}
}

func TestMarkdownSanitizesControlsAndPreservesLogs(t *testing.T) {
	lines := renderTestOutput("line-01\nline-02\n\x1b[999Cunsafe\ttext", 40)
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
	if renderTestOutput("line-01\nline-02\n\x1b[999Cunsafe\ttext", 40)[0] == "mutated" {
		t.Fatal("caller mutated cache")
	}
}

// Each tool call renders as one row whose glyph changes from running to its
// result; completions pair with their start only when the pairing is certain.
func TestToolCallsCollapseIntoOneRowPerCall(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
		count map[string]int
	}{
		{"running row", "work\n[running] read: main.go\n", []string{"⠋  read  main.go"}, nil},
		{"completion across blank lines", "work\n\n[running] read: main.go\n\n[finished] read: main.go\n", []string{"✓  read  main.go"}, map[string]int{"read": 1, "⠋": 0}},
		{"completion keeps start arguments", "work\n[running] read: {\"path\":\"note.txt\"}\n\n[finished] read\n", []string{"✓  read", "note.txt"}, map[string]int{"read": 1, "⠋": 0}},
		{"failure explanation replaces arguments", "[running] bash: go test ./...\n\n[failed] bash: exit status 1\n", []string{"✗  bash  exit status 1"}, map[string]int{"bash": 1, "⠋": 0}},
		{"unnamed failure keeps the tool", "[running] bash: go test ./...\n\n[failed] exit status 1\n", []string{"✗  bash  exit status 1"}, map[string]int{"bash": 1, "⠋": 0}},
		{"unnamed failure with several running is not guessed", "[running] read: a.go\n[running] bash: go test ./...\n\n[failed] timed out\n", []string{"⠋  read  a.go", "⠋  bash  go test ./...", "✗  timed out"}, nil},
		{"unmatched completion keeps its row", "[running] bash: go test ./...\n\n[finished] read\n", []string{"⠋  bash  go test ./...", "✓  read"}, nil},
		{"named failure without start is not misattributed", "[running] read: a.go\n\n[failed] bash: exit status 1\n", []string{"⠋  read  a.go", "✗  bash  exit status 1"}, nil},
		{"empty completion folds", "[running] read: {\"path\":\"note.txt\"}\n[finished]\n", []string{"✓  read", "note.txt"}, map[string]int{"✓": 1, "⠋": 0}},
		{"status is the icon only", "[running] read: a.go\n[finished] read: a.go\n[running] bash: go vet ./...\n[failed] bash: exit status 1\n[running] ls: .\n[interrupted] ls: .\n", []string{"✓", "✗", "⊘"}, map[string]int{"running": 0, "finished": 0, "failed": 0, "interrupted": 0}},
		{"sequential calls of one tool", "[running] read: a.go\n[finished] read\n\n[running] read: b.go\n[finished] read\n", []string{"a.go", "b.go"}, map[string]int{"read": 2, "⠋": 0}},
		{"interleaved calls", "[running] read: a.go\n[running] bash: go test ./...\n[finished] read: a.go\n[failed] bash: go test ./...\n", []string{"✓  read  a.go", "✗  bash  go test ./..."}, map[string]int{"read": 1, "bash": 1, "⠋": 0}},
	}
	for _, tc := range cases {
		plain := ansi.Strip(strings.Join(renderTestOutput(tc.input, 80), "\n"))
		for _, want := range tc.want {
			if !strings.Contains(plain, want) {
				t.Fatalf("%s: missing %q in %q", tc.name, want, plain)
			}
		}
		for text, n := range tc.count {
			if got := strings.Count(plain, text); got != n {
				t.Fatalf("%s: %q appears %d times, want %d: %q", tc.name, text, got, n, plain)
			}
		}
	}
}
