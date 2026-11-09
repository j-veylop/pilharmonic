package tui

import (
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

func renderedOutput(text string, width int) []string {
	if width < 1 {
		return nil
	}
	markdownCache.Lock()
	defer markdownCache.Unlock()
	if markdownCache.lines != nil && markdownCache.text == text && markdownCache.width == width {
		return append([]string(nil), markdownCache.lines...)
	}
	safe := terminalText(text)
	rendered := safe
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(width), glamour.WithTableWrap(true), glamour.WithPreservedNewLines())
	if err == nil {
		if result, renderErr := renderer.Render(safe); renderErr == nil {
			rendered = strings.Trim(result, "\n")
		}
	}
	// Code fences and long unbroken tokens can exceed Glamour's word-wrap width.
	// Hardwrap the final styled text so every viewport row respects terminal cells.
	lines := strings.Split(ansi.Hardwrap(rendered, width, true), "\n")
	markdownCache.text, markdownCache.width, markdownCache.lines = text, width, lines
	return append([]string(nil), lines...)
}
