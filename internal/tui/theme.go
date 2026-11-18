package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/j-veylop/pilharmonic/internal/tui/editor"
)

// The interface uses one compact, adaptive palette. Keep colour decisions here
// so status, selection, badges and transcript styling stay in the same visual
// language.
const (
	colorInk        = "#ECEBF0" // Sash, the base foreground
	colorMuted      = "#858392" // Squid, secondary metadata
	colorFaint      = "#605F6B" // Oyster, the quietest text
	colorAccent     = "#6B50FF" // Charple, the focus colour
	colorAccentDeep = "#5B40EC" // Darple, selection background
	colorCyan       = "#68FFD6" // Bok, keywords
	colorBlue       = "#00A4FF" // Malibu, running state
	colorGreen      = "#12C78F" // Guac, success
	colorAmber      = "#F5EF34" // Mustard, waiting state
	colorRed        = "#EB4268" // Sriracha, failure
	colorPink       = "#FF60FF" // Dolly, the brand's second stop
	colorViolet     = "#8B75FF" // Hazy, the brand's middle stop
	colorBorder     = "#3A3943" // Char, separators and quiet borders
	colorSurface    = "#2D2C36" // BBQ, raised surfaces
	colorOnAccent   = "#FFFAF1" // Butter, text on an accent fill
	colorCanvas     = "#272433" // soft plum canvas behind every pane
	colorPastel     = "#3F3A5C" // muted lavender selection fill
)

// The palette adapts to the terminal background: the light variants read as
// the same hierarchy on a white surface without changing any call site.
var (
	ink    = lipgloss.AdaptiveColor{Light: "#201F26", Dark: colorInk}
	muted  = lipgloss.AdaptiveColor{Light: "#605F6B", Dark: colorMuted}
	accent = lipgloss.AdaptiveColor{Light: "#5B40EC", Dark: colorAccent}
	cyan   = lipgloss.AdaptiveColor{Light: "#0E8C7C", Dark: colorCyan}
	border = lipgloss.AdaptiveColor{Light: "#D6D3DC", Dark: colorBorder}
	blue   = lipgloss.AdaptiveColor{Light: "#007AB8", Dark: colorBlue}
	green  = lipgloss.AdaptiveColor{Light: "#00A475", Dark: colorGreen}
	amber  = lipgloss.AdaptiveColor{Light: "#A66A0A", Dark: colorAmber}
	red    = lipgloss.AdaptiveColor{Light: "#AB2454", Dark: colorRed}
	faint  = lipgloss.AdaptiveColor{Light: "#858392", Dark: colorFaint}
	pink   = lipgloss.AdaptiveColor{Light: "#E940B0", Dark: colorPink}

	surface       = lipgloss.AdaptiveColor{Light: "#ECEBF0", Dark: colorSurface}
	pillOnSurface = lipgloss.AdaptiveColor{Light: "#201F26", Dark: colorInk}
	canvas        = lipgloss.AdaptiveColor{Light: "#F7F3FC", Dark: colorCanvas}
	pastelFill    = lipgloss.AdaptiveColor{Light: "#DFD6F5", Dark: colorPastel}
)

var accentDeep = lipgloss.AdaptiveColor{Light: "#5B40EC", Dark: colorAccentDeep}

// spinnerColors resolves the closed blue, pink and violet loop the output
// loading bar travels through before caching its pre-rendered frames.
func spinnerColors() []lipgloss.TerminalColor {
	if lipgloss.HasDarkBackground() {
		return []lipgloss.TerminalColor{lipgloss.Color("#00A4FF"), lipgloss.Color("#FF60FF"), lipgloss.Color("#8B75FF")}
	}
	return []lipgloss.TerminalColor{lipgloss.Color("#007AB8"), lipgloss.Color("#E940B0"), lipgloss.Color("#6B50FF")}
}

// editorCursorStyle is Crush's block cursor: Dolly pink, bold, one cell.
var editorCursorStyle = lipgloss.NewStyle().Background(pink).Foreground(lipgloss.Color("#201F26"))

func init() { editor.CursorStyle = editorCursorStyle }

// cursorGlyph is the visible caret at the draft's cursor position.
func cursorGlyph() string { return editorCursorStyle.Render("█") }

// canvasEscape paints the application background. The terminal's own
// background is replaced so tinted panes sit on one pastel surface.
func canvasEscape() string {
	var hex string
	if lipgloss.HasDarkBackground() {
		hex = colorCanvas
	} else {
		hex = "#F7F3FC"
	}
	var r, g, b int
	fmt.Sscanf(hex, "#%02X%02X%02X", &r, &g, &b)
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

func blueStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(blue) }
func amberStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(amber) }
func pinkStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(pink) }
func cyanStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(cyan) }

// Diff lines carry a tinted background so additions and removals stay distinct
// even in a monochrome terminal.
var (
	diffAddedStyle = lipgloss.NewStyle().
			Foreground(green).
			Background(lipgloss.AdaptiveColor{Light: "#DDF7EC", Dark: "#18463D"})
	diffRemovedStyle = lipgloss.NewStyle().
				Foreground(red).
				Background(lipgloss.AdaptiveColor{Light: "#FCE4E8", Dark: "#582238"})
	diffHunkStyle = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true)
)

// Semantic status colours keep process state, merge state and tool activity
// consistent. They are intentionally separate from the accent used for focus.
var (
	statusRunning   = blue
	statusCompleted = green
	statusWaiting   = amber
	statusFailed    = red
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(ink)
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(muted)
	mutedStyle   = lipgloss.NewStyle().Foreground(muted)
	faintStyle   = lipgloss.NewStyle().Foreground(faint)
	accentStyle  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	warningStyle = lipgloss.NewStyle().Foreground(statusWaiting).Bold(true)
	successStyle = lipgloss.NewStyle().Foreground(statusCompleted).Bold(true)
	panelStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)

	selectionRailStyle = lipgloss.NewStyle().
				Foreground(accent).
				Bold(true)
	selectionBodyStyle = lipgloss.NewStyle().
				Foreground(pillOnSurface).
				Background(pastelFill).
				Bold(true)
	selectionHighlightStyle = lipgloss.NewStyle().
				Background(pastelFill).
				Foreground(pillOnSurface)
	keyStyle = lipgloss.NewStyle().
			Foreground(muted)
)

// pillStyle and cardStyle carry the Crush-like chrome: compact badges and
// bordered blocks in the transcript.
var (
	pillStyle = lipgloss.NewStyle().
			Background(accent).
			Foreground(lipgloss.Color(colorOnAccent)).
			Bold(true).
			Padding(0, 1)
	pillMutedStyle = lipgloss.NewStyle().
			Background(surface).
			Foreground(muted).
			Padding(0, 1)
	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 1)
)

// The help overlay is prose plus a two-column shortcut table, so it needs a
// heading, a description and a scrollbar style of its own. They reuse the
// palette above and never set a background colour.
var (
	helpHeadingStyle = lipgloss.NewStyle().
				Foreground(accent).
				Bold(true)
	helpDescStyle = lipgloss.NewStyle().
			Foreground(ink)
	helpThumbStyle = lipgloss.NewStyle().
			Foreground(accent)
	helpTrackStyle = lipgloss.NewStyle().
			Foreground(faint)
)

// ApplyTheme pins the background the adaptive palette resolves against. Auto
// follows the terminal; the mode is returned for display and persistence.
func ApplyTheme(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "dark":
		lipgloss.SetHasDarkBackground(true)
		return "dark"
	case "light":
		lipgloss.SetHasDarkBackground(false)
		return "light"
	}
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(os.Stdout))
	return "auto"
}

// wordmark is the product identity as one compact logo, painted through the
// same pink-to-violet ramp Crush uses for its own wordmark.
func wordmark() string {
	return accentStyle.Render("◈ ") + gradientText("PILHARMONIC")
}

// gradientText paints a wordmark through pink and violet without changing its
// visible text or width. ANSI styling is applied per rune, so callers can
// continue to use normal cell-width helpers for layout.
func gradientText(text string) string { return gradientRunes(text, 0) }

func gradientRunes(text string, phase int) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range runes {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(gradientColor(i, len(runes), phase))).Bold(true).Render(string(r)))
	}
	return b.String()
}

// gradientStops is the brand ramp: Dolly pink into Charple violet, matching
// the Crush wordmark's two-stop gradient.
var gradientStops = [][3]float64{
	{255, 96, 255},
	{139, 117, 255},
	{107, 80, 255},
}

// gradientColor interpolates the wordmark through the brand stops, shifting
// with the animation phase.
func gradientColor(i, n, phase int) string {
	t := 0.0
	if span := n + 8; span > 1 {
		t = float64((i+phase)%span) / float64(span-1)
		if t > 1 {
			t = 2 - t
		}
	}
	pos := t * float64(len(gradientStops)-1)
	index := min(int(pos), len(gradientStops)-2)
	local := pos - float64(index)
	start, end := gradientStops[index], gradientStops[index+1]
	return fmt.Sprintf("#%02X%02X%02X",
		int(start[0]+(end[0]-start[0])*local),
		int(start[1]+(end[1]-start[1])*local),
		int(start[2]+(end[2]-start[2])*local),
	)
}
