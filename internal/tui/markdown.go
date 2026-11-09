package tui

import (
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
)

// Cache only the most recent output so repeated layout/scroll calculations do
// not rebuild the Markdown renderer, without retaining prior task transcripts.
var markdownCache struct {
	sync.Mutex
	text  string
	width int
	lines []string
}

// toolMarkerPattern matches the inline tool markers written by the Pi adapter.
// It is a display hint only: agent text that happens to look like a marker is
// still untrusted data, and the structured activity view remains authoritative.
var toolMarkerPattern = regexp.MustCompile(`^\[(running|finished|failed|interrupted)\] ?(.*)$`)

type transcriptToolCall struct {
	status  string
	summary string
}

func parseToolMarker(line string) (transcriptToolCall, bool) {
	match := toolMarkerPattern.FindStringSubmatch(strings.TrimRight(line, "\r"))
	if match == nil {
		return transcriptToolCall{}, false
	}
	return transcriptToolCall{status: match[1], summary: strings.TrimSpace(terminalText(match[2]))}, true
}

func renderedOutput(text string, width int) []string {
	if width < 1 {
		return nil
	}
	markdownCache.Lock()
	defer markdownCache.Unlock()
	if markdownCache.lines != nil && markdownCache.text == text && markdownCache.width == width {
		return append([]string(nil), markdownCache.lines...)
	}
	lines := renderTranscript(text, width)
	markdownCache.text, markdownCache.width, markdownCache.lines = text, width, lines
	return append([]string(nil), lines...)
}

// renderTranscript keeps assistant prose and tool activity as separate visual
// blocks: prose goes through Markdown, tool markers become compact status rows.
func renderTranscript(text string, width int) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(raw); {
		if _, ok := parseToolMarker(raw[i]); !ok {
			start := i
			for i < len(raw) {
				if _, ok := parseToolMarker(raw[i]); ok {
					break
				}
				i++
			}
			chunk := strings.Trim(terminalText(strings.Join(raw[start:i], "\n")), "\n")
			if strings.TrimSpace(chunk) != "" {
				out = append(out, renderMarkdown(chunk, width)...)
			}
			continue
		}
		var calls []transcriptToolCall
		for i < len(raw) {
			call, ok := parseToolMarker(raw[i])
			if !ok {
				break
			}
			calls = append(calls, call)
			i++
		}
		out = appendToolCalls(out, collapseToolCalls(calls), width)
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

func renderMarkdown(safe string, width int) []string {
	rendered := safe
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(width), glamour.WithTableWrap(true), glamour.WithPreservedNewLines())
	if err == nil {
		if result, renderErr := renderer.Render(safe); renderErr == nil {
			rendered = strings.Trim(result, "\n")
		}
	}
	// Code fences and long unbroken tokens can exceed Glamour's word-wrap width.
	// Hardwrap the final styled text so every viewport row respects terminal cells.
	return strings.Split(ansi.Hardwrap(rendered, width, true), "\n")
}

// collapseToolCalls folds a directly paired start/finish of the same call into
// one row, so the transcript shows each call once instead of twice.
func collapseToolCalls(calls []transcriptToolCall) []transcriptToolCall {
	result := make([]transcriptToolCall, 0, len(calls))
	for _, call := range calls {
		if n := len(result) - 1; n >= 0 && result[n].status == "running" && call.status != "running" && call.summary == result[n].summary {
			result[n] = call
			continue
		}
		result = append(result, call)
	}
	return result
}

func appendToolCalls(out []string, calls []transcriptToolCall, width int) []string {
	if len(calls) == 0 {
		return out
	}
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	for _, call := range calls {
		out = append(out, toolCallLines(call, width)...)
	}
	return append(out, "")
}

// toolCallLines renders one tool call as a status-coloured rail and glyph,
// followed by the tool name and a muted summary. The rail and indentation keep
// tool activity distinct from assistant prose at a glance.
func toolCallLines(call transcriptToolCall, width int) []string {
	style, glyph := toolStatusStyle(call.status)
	body := style.Bold(true).Render(glyph + " " + call.status)
	name, args := splitToolSummary(call.summary)
	if name != "" {
		body += "  " + titleStyle.Render(name)
	}
	if args != "" {
		body += "  " + mutedStyle.Render(args)
	}
	wrapped := strings.Split(ansi.Hardwrap(body, max(1, width-2), true), "\n")
	rail := style.Render("▎")
	for i := range wrapped {
		if i == 0 {
			wrapped[i] = rail + " " + wrapped[i]
		} else {
			wrapped[i] = "  " + wrapped[i]
		}
	}
	return wrapped
}

func splitToolSummary(summary string) (string, string) {
	if name, args, ok := strings.Cut(summary, ": "); ok {
		return name, args
	}
	return summary, ""
}

func styledToolSummary(summary string) string {
	name, args := splitToolSummary(terminalText(summary))
	if name == "" {
		return ""
	}
	if args == "" {
		return titleStyle.Render(name)
	}
	return titleStyle.Render(name) + mutedStyle.Render("  "+args)
}
