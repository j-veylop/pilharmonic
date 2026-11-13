package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"
)

// markdownCache keeps only the most recent output so repeated layout/scroll
// calculations do not rebuild the Markdown renderer, without retaining prior
// task transcripts. It belongs to the model so no process-wide state is shared
// between terminals or tests.
type markdownCache struct {
	text     string
	width    int
	thinking bool
	lines    []string
}

// toolMarkerPattern matches the inline tool markers written by the Pi adapter.
// It is a display hint only: agent text that happens to look like a marker is
// still untrusted data, and the structured activity retained on the task
// remains authoritative.
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

// parseThinkingMarker recognizes the display-only boundaries written by the Pi
// adapter. Like tool markers, these are hints: agent text that happens to look
// like a marker is still untrusted data.
func parseThinkingMarker(line string) (open, closing bool) {
	switch strings.TrimSpace(strings.TrimRight(line, "\r")) {
	case "[thinking]", "[thinking start]", "[thinking_start]":
		return true, false
	case "[/thinking]", "[thinking end]", "[thinking_end]":
		return false, true
	}
	return false, false
}

// parseInlineThinking accepts the compact "[thinking] text" form as well as the
// line-delimited block form. It is a convenience for adapters that annotate each
// delta instead of writing explicit start and end boundaries.
func parseInlineThinking(line string) (string, bool) {
	trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
	if !strings.HasPrefix(trimmed, "[thinking]") {
		return "", false
	}
	content := strings.TrimSpace(strings.TrimPrefix(trimmed, "[thinking]"))
	return content, content != ""
}

func appendThinking(out []string, block string, width int, showThinking bool) []string {
	if !showThinking {
		out = append(out, strings.Split(ansi.Hardwrap(mutedStyle.Render("▌ thinking hidden · T to show"), width, true), "\n")...)
		return append(out, "")
	}
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	out = append(out, strings.Split(ansi.Hardwrap(mutedStyle.Render("▌ THINKING"), width, true), "\n")...)
	if strings.TrimSpace(block) == "" {
		out = append(out, mutedStyle.Render("(no thinking text)"))
	} else {
		out = append(out, renderMarkdown(block, width)...)
	}
	return append(out, "")
}

// renderedOutputFor renders through the model's cache so a view pass reuses the
// last transcript instead of re-running Glamour for every geometry query.
func (m model) renderedOutputFor(text string, width int, glyph string, showThinking bool) []string {
	cache := m.markdown
	if cache == nil {
		cache = &markdownCache{}
	}
	return cache.render(text, width, glyph, showThinking)
}

func (c *markdownCache) render(text string, width int, glyph string, showThinking bool) []string {
	if width < 1 {
		return nil
	}
	if c.lines == nil || c.text != text || c.width != width || c.thinking != showThinking {
		c.lines = renderTranscriptWithThinking(text, width, showThinking)
		c.text, c.width, c.thinking = text, width, showThinking
	}
	result := append([]string(nil), c.lines...)
	if glyph == "" {
		glyph = defaultRunningGlyph
	}
	for i := range result {
		result[i] = strings.ReplaceAll(result[i], runningGlyphPlaceholder, glyph)
	}
	return result
}

// renderTranscriptWithThinking keeps assistant prose, thinking and tool
// activity as separate visual blocks: prose goes through Markdown, tool markers
// become compact status rows, and thinking is either collapsed or shown.
func renderTranscriptWithThinking(text string, width int, showThinking bool) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(raw); {
		if open, closing := parseThinkingMarker(raw[i]); open || closing {
			if closing {
				// A retained tail can start after the opening marker was trimmed.
				i++
				continue
			}
			i++
			start := i
			for i < len(raw) {
				if _, closing := parseThinkingMarker(raw[i]); closing {
					break
				}
				i++
			}
			block := strings.Trim(terminalText(strings.Join(raw[start:i], "\n")), "\n")
			if i < len(raw) {
				i++
			}
			out = appendThinking(out, block, width, showThinking)
			continue
		}
		if _, ok := parseInlineThinking(raw[i]); ok {
			var parts []string
			for i < len(raw) {
				part, ok := parseInlineThinking(raw[i])
				if !ok {
					break
				}
				parts = append(parts, part)
				i++
			}
			out = appendThinking(out, terminalText(strings.Join(parts, "\n")), width, showThinking)
			continue
		}
		if _, ok := parseToolMarker(raw[i]); !ok {
			start := i
			for i < len(raw) {
				if _, ok := parseToolMarker(raw[i]); ok {
					break
				}
				if open, closing := parseThinkingMarker(raw[i]); open || closing {
					break
				}
				if _, ok := parseInlineThinking(raw[i]); ok {
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
			if ok {
				calls = append(calls, call)
				i++
				continue
			}
			// The Pi adapter writes a newline before each marker, which can
			// leave an empty line between a start and its completion. Keep the
			// markers in one block so the completion updates the running row.
			if strings.TrimSpace(raw[i]) == "" {
				j := i
				for j < len(raw) && strings.TrimSpace(raw[j]) == "" {
					j++
				}
				if j < len(raw) {
					if _, next := parseToolMarker(raw[j]); next {
						i = j
						continue
					}
				}
			}
			break
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

// transcriptStyle keeps assistant Markdown in the same palette as the task
// board. Headings use a slim coloured rail; text stays on the terminal's own
// background instead of the brighter default Glamour theme.
func transcriptStyle() glamouransi.StyleConfig {
	stringPtr := func(value string) *string { return &value }
	boolPtr := func(value bool) *bool { return &value }
	uintPtr := func(value uint) *uint { return &value }

	style := glamourstyles.DarkStyleConfig
	style.Document.Color = stringPtr(colorInk)
	style.Document.Margin = uintPtr(0)
	style.BlockQuote.Color = stringPtr(colorMuted)
	style.BlockQuote.Indent = uintPtr(1)
	style.BlockQuote.IndentToken = stringPtr("▌ ")
	style.Heading.Color = stringPtr(colorAccent)
	style.Heading.Bold = boolPtr(true)
	style.H1 = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
		BlockSuffix: "\n", Prefix: "▌ ", Color: stringPtr(colorAccent), Bold: boolPtr(true),
	}}
	style.H2 = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
		BlockSuffix: "\n", Prefix: "▍ ", Color: stringPtr(colorCyan), Bold: boolPtr(true),
	}}
	style.H3 = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
		BlockSuffix: "\n", Prefix: "› ", Color: stringPtr(colorAmber), Bold: boolPtr(true),
	}}
	style.Text.Color = stringPtr(colorInk)
	style.Strong.Color = stringPtr(colorInk)
	style.Strong.Bold = boolPtr(true)
	style.Emph.Color = stringPtr(colorCyan)
	style.Emph.Italic = boolPtr(true)
	style.HorizontalRule.Color = stringPtr(colorFaint)
	style.HorizontalRule.Format = "\n  ┄┄┄\n"
	style.Link.Color = stringPtr(colorCyan)
	style.Link.Underline = boolPtr(true)
	style.LinkText.Color = stringPtr(colorAccentDeep)
	style.LinkText.Bold = boolPtr(true)
	style.Code.Color = stringPtr(colorAmber)
	style.Code.Prefix = " "
	style.Code.Suffix = " "
	style.CodeBlock.Color = stringPtr(colorInk)
	style.CodeBlock.Margin = uintPtr(0)
	style.Table.Color = stringPtr(colorMuted)
	style.Table.ColumnSeparator = stringPtr(" │ ")
	style.Table.CenterSeparator = stringPtr("─┼─")
	style.Table.RowSeparator = stringPtr("─")
	style.Item.Color = stringPtr(colorAccent)
	style.Task.Ticked = "[✓] "
	style.Task.Unticked = "[ ] "
	return style
}

func renderMarkdown(safe string, width int) []string {
	rendered := safe
	renderer, err := glamour.NewTermRenderer(glamour.WithStyles(transcriptStyle()), glamour.WithWordWrap(width), glamour.WithTableWrap(true), glamour.WithPreservedNewLines())
	if err == nil {
		if result, renderErr := renderer.Render(safe); renderErr == nil {
			rendered = strings.Trim(result, "\n")
		}
	}
	// Code fences and long unbroken tokens can exceed Glamour's word-wrap width.
	// Hardwrap the final styled text so every viewport row respects terminal cells.
	return strings.Split(ansi.Hardwrap(rendered, width, true), "\n")
}

// collapseToolCalls folds each completion into the matching running row, even
// when another tool started in between. A call is rendered once, with its
// glyph changing from the running spinner to the final status icon. Successful
// completions carry only the tool name, so they are paired with the earliest
// unfinished call of the same tool; the running summary is kept rather than
// dropping the arguments the start marker already displayed. A completion that
// carries no tool name of its own, such as a bare failure message, is paired
// with the only unfinished call, so its row keeps the tool and the message
// instead of showing the message alone. A completion that does name a tool the
// block has no call for keeps its own row: it already shows that tool, and
// folding it into another call would mislabel the failure.
func collapseToolCalls(calls []transcriptToolCall) []transcriptToolCall {
	result := make([]transcriptToolCall, 0, len(calls))
	for _, call := range calls {
		matched := false
		if call.status != "running" {
			name, detail := splitToolSummary(call.summary)
			index := lastRunningWithSummary(result, call.summary)
			if index < 0 {
				index = firstRunningNamed(result, name)
			}
			if index < 0 && !namesTool(result, name) {
				switch {
				case (call.status == "failed" || call.status == "interrupted") && detail == "":
					// The completion is a message, not a tool call of its
					// own, so it belongs to the call that was running.
					index = onlyUnfinishedCall(result)
				case call.status == "finished" && name == "":
					// An empty completion carries nothing to show on its own.
					index = onlyUnfinishedCall(result)
				}
			}
			if index >= 0 {
				call.summary = mergeToolSummary(result[index].summary, call.summary)
				result[index] = call
				matched = true
			}
		}
		if !matched {
			result = append(result, call)
		}
	}
	return result
}

// lastRunningWithSummary finds the most recent unfinished call with exactly this
// summary, which is the usual repeat of a start marker's arguments.
func lastRunningWithSummary(calls []transcriptToolCall, summary string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].status == "running" && calls[i].summary == summary {
			return i
		}
	}
	return -1
}

// firstRunningNamed finds the earliest unfinished call of the named tool.
func firstRunningNamed(calls []transcriptToolCall, name string) int {
	if name == "" {
		return -1
	}
	for i := range calls {
		if runningName, _ := splitToolSummary(calls[i].summary); runningName == name && calls[i].status == "running" {
			return i
		}
	}
	return -1
}

// namesTool reports whether the block already shows a call of the named tool, so
// a completion that repeats a tool name the block recorded keeps that meaning
// instead of being read as a message about another call.
func namesTool(calls []transcriptToolCall, name string) bool {
	if name == "" {
		return false
	}
	for i := range calls {
		if toolName, _ := splitToolSummary(calls[i].summary); toolName == name {
			return true
		}
	}
	return false
}

// onlyUnfinishedCall returns the index of the single running call, or -1 when
// none or several are running. The adapter runs tools one at a time, so an
// unnamed failure belongs to that call; with several candidates the pairing
// would be a guess and is left to the named matches above.
func onlyUnfinishedCall(calls []transcriptToolCall) int {
	index := -1
	for i := range calls {
		if calls[i].status != "running" {
			continue
		}
		if index >= 0 {
			return -1
		}
		index = i
	}
	return index
}

// mergeToolSummary keeps the arguments established by a running marker when a
// completion only repeats the tool name. A completion that carries its own
// detail, such as a failure explanation, replaces the arguments but never
// hides the tool: a completion that names no tool keeps the started name in
// front of its message.
func mergeToolSummary(started, completed string) string {
	startedName, _ := splitToolSummary(started)
	name, detail := splitToolSummary(completed)
	switch {
	case completed == "":
		return started
	case detail == "" && name == startedName:
		// The completion only repeats the tool name.
		return started
	case detail != "" && name == startedName:
		return completed
	case startedName != "":
		return startedName + ": " + completed
	default:
		return completed
	}
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
// followed by the tool name and a muted summary. The glyph alone carries the
// status, so the row stays compact; the rail and indentation keep tool activity
// distinct from assistant prose at a glance. A failure keeps the tool name in
// front of its message, so the row never shows the explanation alone.
func toolCallLines(call transcriptToolCall, width int) []string {
	style, glyph := toolStatusStyle(call.status)
	body := style.Bold(true).Render(glyph)
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
