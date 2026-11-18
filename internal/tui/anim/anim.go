// Package anim ports Crush's animated spinner: scrambled cycling characters
// with a gradient, a staggered entrance, an animated ellipsis and an optional
// suffix such as elapsed time. Frames are pre-rendered and cached by settings,
// so Render stays cheap and identical settings render identically in every
// process.
package anim

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const (
	fps               = 20
	initialChar       = '.'
	ellipsisSpeed     = 8
	maxBirthSteps     = 20
	prerenderedFrames = 10

	defaultSize = 10
)

var (
	scrambleRunes = []rune("0123456789abcdefABCDEF~!@#$£€%^&*()+=_")
	ellipsisText  = []string{".", "..", "...", ""}
)

// FrameInterval is the shared animation clock, matching Crush's 20 fps.
func FrameInterval() time.Duration { return time.Second / fps }

// Settings configures one spinner.
type Settings struct {
	ID          string
	Size        int
	Label       string
	LabelColor  lipgloss.TerminalColor
	GradColorA  lipgloss.TerminalColor
	GradColorB  lipgloss.TerminalColor
	CycleColors bool
	NoScramble  bool
	// Suffix returns a dynamic suffix such as elapsed time. It hides the
	// ellipsis while non-empty, matching Crush.
	Suffix      func() string
	SuffixColor lipgloss.TerminalColor
}

var (
	lastID atomic.Int64
)

// Anim is a single spinner driven by an external clock.
type Anim struct {
	mu        sync.RWMutex
	settings  Settings
	cycling   int
	label     []string
	labelWide int
	width     int
	framesN   int
	birth     []int
	cache     *frameCache
	step      atomic.Int64
	ticks     atomic.Int64
	ellipsisI atomic.Int64
	born      atomic.Bool
	suffix    atomic.Value // string
}

// New builds and pre-renders a spinner. Identical settings share cached frames.
func New(settings Settings) *Anim {
	if settings.Size < 1 && !settings.NoScramble {
		settings.Size = defaultSize
	}
	if settings.GradColorA == nil {
		settings.GradColorA = lipgloss.Color("#FF0000")
	}
	if settings.GradColorB == nil {
		settings.GradColorB = lipgloss.Color("#0000FF")
	}
	if settings.SuffixColor == nil {
		settings.SuffixColor = settings.LabelColor
	}
	a := &Anim{settings: settings}
	a.cycling = settings.Size
	if settings.NoScramble {
		a.cycling = 0
		a.born.Store(true)
	}
	a.suffix.Store("")
	a.build()
	a.birth = birthSchedule(a.id(), settingsHash(settings), a.width)
	return a
}

func (a *Anim) id() string {
	if a.settings.ID != "" {
		return a.settings.ID
	}
	return fmt.Sprintf("%d", lastID.Add(1))
}

func (a *Anim) build() {
	key := settingsHash(a.settings)
	cached := loadCache(key)
	if cached == nil {
		cached = renderFrames(a.settings, a.cycling)
		storeCache(key, cached)
	}
	a.mu.Lock()
	a.cache = cached
	a.label = cached.label
	a.labelWide = cached.labelWide
	a.width = cached.width
	a.framesN = cached.framesN
	a.mu.Unlock()
}

// SetLabel changes the label and its animated ellipsis without restarting the
// frames or entrance schedule.
func (a *Anim) SetLabel(label string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.Label = label
	a.labelWide = lipgloss.Width(label)
	a.width = a.cycling
	if label != "" {
		if a.cycling > 0 {
			a.width++
		}
		a.width += a.labelWide
	}
	a.label = renderLabel(label, a.settings.LabelColor)
	a.cache = a.cache.withLabel(a.label, a.labelWide, a.width)
}

// SetColors rebuilds the gradient, label and suffix colours while keeping the
// current step and entrance schedule, so a theme swap does not restart flight.
func (a *Anim) SetColors(labelColor, gradA, gradB, suffixColor lipgloss.TerminalColor) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.LabelColor = labelColor
	a.settings.GradColorA = gradA
	a.settings.GradColorB = gradB
	a.settings.SuffixColor = suffixColor
	key := settingsHash(a.settings)
	cached := loadCache(key)
	if cached == nil {
		cached = renderFrames(a.settings, a.cycling)
		storeCache(key, cached)
	}
	a.cache = cached
	a.label = cached.label
	a.labelWide = cached.labelWide
	a.width = cached.width
	a.framesN = cached.framesN
}

// SetSuffixText updates the dynamic suffix. It hides the ellipsis while set.
func (a *Anim) SetSuffixText(text string) { a.suffix.Store(text) }

// Advance moves one frame forward. The caller owns the clock.
func (a *Anim) Advance() bool {
	step := a.step.Add(1)
	a.mu.RLock()
	frames := a.cache.framesN
	a.mu.RUnlock()
	if int(step) >= frames {
		a.step.Store(0)
	}
	ticks := a.ticks.Add(1)
	a.mu.RLock()
	labelWide := a.labelWide
	a.mu.RUnlock()
	if a.born.Load() && labelWide > 0 {
		next := a.ellipsisI.Add(1)
		if int(next) >= ellipsisSpeed*len(ellipsisText) {
			a.ellipsisI.Store(0)
		}
	} else if !a.born.Load() && int(ticks) >= maxBirthSteps {
		a.born.Store(true)
	}
	return true
}

// Render returns the current frame, ellipsis and suffix.
func (a *Anim) Render() string {
	a.mu.RLock()
	cache := a.cache
	cycling := a.cycling
	width := a.width
	labelWide := a.labelWide
	suffixColor := a.settings.SuffixColor
	a.mu.RUnlock()
	step := int(a.step.Load()) % max(1, cache.framesN)
	ticks := int(a.ticks.Load())
	var b strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case !a.born.Load() && i < len(a.birth) && ticks < a.birth[i]:
			b.WriteString(cache.initial[step][i])
		case i < cycling:
			b.WriteString(cache.frames[step][i])
		case i == cycling && cycling > 0:
			b.WriteString(" ")
		default:
			offset := cycling
			if cycling > 0 {
				offset++
			}
			if index := i - offset; index >= 0 && index < len(cache.label) {
				b.WriteString(cache.label[index])
			}
		}
	}
	suffix, _ := a.suffix.Load().(string)
	if a.born.Load() && labelWide > 0 && suffix == "" {
		index := int(a.ellipsisI.Load()) / ellipsisSpeed
		b.WriteString(cache.ellipsis[index%len(cache.ellipsis)])
	}
	if suffix != "" {
		b.WriteString(" ")
		b.WriteString(lipgloss.NewStyle().Foreground(suffixColor).Render(suffix))
	}
	return b.String()
}

// Width is the rendered cell width, including the widest ellipsis frame and the
// current suffix.
func (a *Anim) Width() int {
	a.mu.RLock()
	w := a.width
	labelWide := a.labelWide
	a.mu.RUnlock()
	if labelWide > 0 {
		w += 3
	}
	if suffix, _ := a.suffix.Load().(string); suffix != "" {
		w += 1 + lipgloss.Width(suffix)
	}
	return w
}

type frameCache struct {
	initial   [][]string
	frames    [][]string
	label     []string
	ellipsis  []string
	width     int
	labelWide int
	framesN   int
}

func (c *frameCache) withLabel(label []string, labelWide, width int) *frameCache {
	return &frameCache{initial: c.initial, frames: c.frames, label: label, ellipsis: c.ellipsis, width: width, labelWide: labelWide, framesN: c.framesN}
}

var frameCacheMap sync.Map

func settingsHash(s Settings) string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d-%s-%v-%v-%v-%t", s.Size, s.Label, s.LabelColor, s.GradColorA, s.GradColorB, s.CycleColors)
	return fmt.Sprintf("%x", h.Sum64())
}

func loadCache(key string) *frameCache {
	if value, ok := frameCacheMap.Load(key); ok {
		return value.(*frameCache)
	}
	return nil
}

func storeCache(key string, cache *frameCache) { frameCacheMap.Store(key, cache) }

func renderFrames(settings Settings, cycling int) *frameCache {
	width := cycling
	if settings.Label != "" {
		if cycling > 0 {
			width++
		}
		width += lipgloss.Width(settings.Label)
	}
	framesN := prerenderedFrames
	rampSize := max(1, width*3)
	if settings.CycleColors {
		framesN = max(2, width*2)
		rampSize = framesN/2 + 1
	}
	ramp := gradientRamp(rampSize, settings.GradColorA, settings.GradColorB)
	seed := seedOf(settingsHash(settings))
	rng := rand.New(rand.NewPCG(seed, ^seed))
	cache := &frameCache{
		initial:   make([][]string, framesN),
		frames:    make([][]string, framesN),
		label:     renderLabel(settings.Label, settings.LabelColor),
		ellipsis:  renderEllipsis(settings.LabelColor),
		width:     width,
		labelWide: lipgloss.Width(settings.Label),
		framesN:   framesN,
	}
	for f := range cache.initial {
		cache.initial[f] = make([]string, max(0, width))
		cache.frames[f] = make([]string, max(0, width))
		for i := range cache.frames[f] {
			index := i % len(ramp)
			if settings.CycleColors {
				// Mirror the ramp so the frame wrap takes one ordinary color step.
				phase := (i + f) % framesN
				index = min(phase, framesN-phase)
			}
			c := ramp[index]
			cache.initial[f][i] = lipgloss.NewStyle().Foreground(c).Render(string(initialChar))
			if i < cycling {
				cache.frames[f][i] = lipgloss.NewStyle().Foreground(c).Render(string(scrambleRunes[rng.IntN(len(scrambleRunes))]))
			}
		}
	}
	return cache
}

func renderLabel(label string, color lipgloss.TerminalColor) []string {
	out := make([]string, 0, len(label))
	for _, r := range label {
		out = append(out, lipgloss.NewStyle().Foreground(color).Render(string(r)))
	}
	return out
}

func renderEllipsis(color lipgloss.TerminalColor) []string {
	out := make([]string, len(ellipsisText))
	for i, frame := range ellipsisText {
		out[i] = lipgloss.NewStyle().Foreground(color).Render(frame)
	}
	return out
}

func birthSchedule(id, hash string, width int) []int {
	seed := seedOf(id + "|" + hash)
	rng := rand.New(rand.NewPCG(seed, ^seed))
	steps := make([]int, max(1, width))
	for i := range steps {
		steps[i] = rng.IntN(maxBirthSteps)
	}
	return steps
}

func seedOf(text string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return h.Sum64()
}

func gradientRamp(size int, a, b lipgloss.TerminalColor) []lipgloss.TerminalColor {
	if size < 2 {
		return []lipgloss.TerminalColor{a, b}
	}
	ramp := make([]lipgloss.TerminalColor, size)
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	for i := range ramp {
		t := float64(i) / float64(size-1)
		ramp[i] = lipgloss.Color(fmt.Sprintf("#%02x%02x%02x",
			uint8(float64(ar>>8)*(1-t)+float64(br>>8)*t),
			uint8(float64(ag>>8)*(1-t)+float64(bg>>8)*t),
			uint8(float64(ab>>8)*(1-t)+float64(bb>>8)*t)))
	}
	return ramp
}
