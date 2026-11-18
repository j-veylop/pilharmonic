package tui

import (
	"regexp"
	"strings"

	"github.com/j-veylop/pilharmonic/internal/orchestrator"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// markdownCache keeps only the most recent transcript so layout and scroll
// queries do not re-run Glamour. It belongs to the model, not the process.
type markdownCache struct {
	text     string
	width    int
	thinking bool
	lines    []string
}

var toolMarkerStatuses = []string{
	orchestrator.ActivityRunning,
	orchestrator.ActivityFinished,
	orchestrator.ActivityFailed,
	orchestrator.ActivityInterrupted,
}

// toolMarkerPattern matches inline tool markers. Like the thinking markers,
// they are display hints only: the transcript is untrusted and the structured
// activity on the task stays authoritative.
var toolMarkerPattern = regexp.MustCompile(`^\[(` + strings.Join(toolMarkerStatuses, "|") + `)\] ?(.*)$`)

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

// parseThinkingMarker recognizes the block thinking boundaries.
func parseThinkingMarker(line string) (open, closing bool) {
	switch strings.TrimSpace(strings.TrimRight(line, "\r")) {
	case "[thinking]", "[thinking start]", "[thinking_start]":
		return true, false
	case "[/thinking]", "[thinking end]", "[thinking_end]":
		return false, true
	}
	return false, false
}

// parseInlineThinking accepts the per-line "[thinking] text" form.
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
		out = append(out, strings.Split(ansi.Hardwrap(mutedStyle.Render("▌ thinking hidden · t to show"), width, true), "\n")...)
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

// renderedOutputFor renders through the model's cache.
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
	isClosing := func(line string) bool { _, closing := parseThinkingMarker(line); return closing }
	isInline := func(line string) bool { _, ok := parseInlineThinking(line); return ok }
	isTool := func(line string) bool { _, ok := parseToolMarker(line); return ok }
	startsBlock := func(line string) bool {
		open, closing := parseThinkingMarker(line)
		return open || closing || isInline(line) || isTool(line)
	}
	safe := func(lines []string) string { return strings.Trim(terminalText(strings.Join(lines, "\n")), "\n") }
	var out []string
	for i := 0; i < len(raw); {
		open, closing := parseThinkingMarker(raw[i])
		switch {
		case closing:
			// A retained tail can start after the opening marker was trimmed.
			i++
		case open:
			end := indexFrom(raw, i+1, isClosing)
			out = appendThinking(out, safe(raw[i+1:end]), width, showThinking)
			i = min(end+1, len(raw))
		case isInline(raw[i]):
			var parts []string
			for ; i < len(raw) && isInline(raw[i]); i++ {
				part, _ := parseInlineThinking(raw[i])
				parts = append(parts, part)
			}
			out = appendThinking(out, terminalText(strings.Join(parts, "\n")), width, showThinking)
		case isTool(raw[i]):
			var calls []transcriptToolCall
			calls, i = toolBlock(raw, i)
			out = appendToolCalls(out, collapseToolCalls(calls), width)
		default:
			end := indexFrom(raw, i, startsBlock)
			if chunk := safe(raw[i:end]); strings.TrimSpace(chunk) != "" {
				out = append(out, renderMarkdown(chunk, width)...)
			}
			i = end
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// indexFrom is the first index at or after from whose line matches, or len(lines).
func indexFrom(lines []string, from int, match func(string) bool) int {
	for i := from; i < len(lines); i++ {
		if match(lines[i]) {
			return i
		}
	}
	return len(lines)
}

// toolBlock collects consecutive tool markers from i. Blank lines between
// markers stay in the block so a completion still updates its running row.
func toolBlock(raw []string, i int) ([]transcriptToolCall, int) {
	var calls []transcriptToolCall
	for i < len(raw) {
		if call, ok := parseToolMarker(raw[i]); ok {
			calls = append(calls, call)
			i++
			continue
		}
		next := indexFrom(raw, i, func(line string) bool { return strings.TrimSpace(line) != "" })
		if next == i || next == len(raw) {
			break
		}
		if _, ok := parseToolMarker(raw[next]); !ok {
			break
		}
		i = next
	}
	return calls, i
}

// transcriptStyle keeps assistant Markdown in the board's palette.
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
		BlockSuffix: "\n", Prefix: "▍ ", Color: stringPtr(colorAccent), Bold: boolPtr(true),
	}}
	style.H3 = glamouransi.StyleBlock{StylePrimitive: glamouransi.StylePrimitive{
		BlockSuffix: "\n", Prefix: "› ", Color: stringPtr(colorAmber), Bold: boolPtr(true),
	}}
	style.Text.Color = stringPtr(colorInk)
	style.Strong.Color = stringPtr(colorPink)
	style.Strong.Bold = boolPtr(true)
	style.Emph.Color = stringPtr(colorInk)
	style.Emph.Italic = boolPtr(true)
	style.HorizontalRule.Color = stringPtr(colorViolet)
	style.HorizontalRule.Format = "\n  ┄┄┄\n"
	style.Link.Color = stringPtr(colorAccent)
	style.Link.Underline = boolPtr(true)
	style.LinkText.Color = stringPtr(colorAccentDeep)
	style.LinkText.Bold = boolPtr(true)
	style.Code.Color = stringPtr(colorAmber)
	style.Code.Prefix = " "
	style.Code.Suffix = " "
	// Code keeps the canvas: the dark style's grey inline and block fills read
	// as black patches on the pastel surface.
	style.Code.BackgroundColor = nil
	style.CodeBlock.Color = stringPtr(colorInk)
	style.CodeBlock.Margin = uintPtr(0)
	if style.CodeBlock.Chroma != nil {
		style.CodeBlock.Chroma.Background = glamouransi.StylePrimitive{Color: stringPtr(colorInk)}
	}
	style.Table.Color = stringPtr(colorMuted)
	style.Table.ColumnSeparator = stringPtr(" │ ")
	style.Table.CenterSeparator = stringPtr("─┼─")
	style.Table.RowSeparator = stringPtr("─")
	style.Item.Color = stringPtr(colorAccent)
	style.Enumeration.Color = stringPtr(colorPink)
	style.Enumeration.Bold = boolPtr(true)
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

// collapseToolCalls folds each completion into its running row, so a call
// renders once with its glyph changing to the result. A completion pairs with
// the running call repeating its summary, else the earliest running call of
// the same tool; an unnamed or empty completion pairs only when exactly one
// call is running. A completion naming a tool the block has not shown keeps
// its own row rather than mislabel another call.
func collapseToolCalls(calls []transcriptToolCall) []transcriptToolCall {
	result := make([]transcriptToolCall, 0, len(calls))
	for _, call := range calls {
		matched := false
		if call.status != orchestrator.ActivityRunning {
			name, detail := splitToolSummary(call.summary)
			index := lastRunningWithSummary(result, call.summary)
			if index < 0 {
				index = firstRunningNamed(result, name)
			}
			if index < 0 && !namesTool(result, name) {
				switch {
				case (call.status == orchestrator.ActivityFailed || call.status == orchestrator.ActivityInterrupted) && detail == "":
					// The completion is a message, not a tool call of its
					// own, so it belongs to the call that was running.
					index = onlyUnfinishedCall(result)
				case call.status == orchestrator.ActivityFinished && name == "":
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

// lastRunningWithSummary finds the latest running call with this summary.
func lastRunningWithSummary(calls []transcriptToolCall, summary string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].status == orchestrator.ActivityRunning && calls[i].summary == summary {
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
		if runningName, _ := splitToolSummary(calls[i].summary); runningName == name && calls[i].status == orchestrator.ActivityRunning {
			return i
		}
	}
	return -1
}

// namesTool reports whether the block already shows a call of the named tool.
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

// onlyUnfinishedCall is the single running call, or -1 for none or several.
func onlyUnfinishedCall(calls []transcriptToolCall) int {
	index := -1
	for i := range calls {
		if calls[i].status != orchestrator.ActivityRunning {
			continue
		}
		if index >= 0 {
			return -1
		}
		index = i
	}
	return index
}

// mergeToolSummary keeps the start's arguments when a completion only repeats
// the tool name, and keeps the tool name in front of a completion's message.
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

// toolCallLines renders one call as a status-coloured rail and glyph, the tool
// name and a muted summary, indented apart from assistant prose.
func toolCallLines(call transcriptToolCall, width int) []string {
	style, glyph := toolStatusStyle(call.status, runningGlyphPlaceholder)
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
	if label := toolStatusWord(call.status); label != "" && lipgloss.Width(wrapped[0])+len(label)+2 <= width {
		wrapped[0] += strings.Repeat(" ", max(1, width-lipgloss.Width(wrapped[0])-len(label)-1)) + mutedStyle.Render(label)
	}
	return wrapped
}

// toolStatusWord labels a call's result on wide rows, so status is readable
// without relying on the rail colour or glyph alone.
func toolStatusWord(status string) string {
	switch status {
	case orchestrator.ActivityRunning:
		return "RUNNING"
	case orchestrator.ActivityFinished:
		return "DONE"
	case orchestrator.ActivityFailed:
		return "FAILED"
	case orchestrator.ActivityInterrupted:
		return "STOPPED"
	}
	return ""
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
