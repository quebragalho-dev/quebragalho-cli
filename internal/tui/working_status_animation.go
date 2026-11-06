package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

const (
	workingStatusText        = "Trabalhando"
	workingDriveGridWidth    = 3
	workingDriveGridHeight   = 3
	workingDriveStep         = 90 * time.Millisecond
	workingDriveCycle        = 1200 * time.Millisecond
	workingDriveBrightWindow = 80 * time.Millisecond
	workingDriveSoftWindow   = 200 * time.Millisecond
	workingDriveDimWindow    = 430 * time.Millisecond
)

// workingStatusWords are the rotating liveness verbs. Trabalhando stays first
// so the zero-value model (fixtures without beginRun) keeps showing the brand
// word, exactly as before; real turns pick a random start in beginRun.
var workingStatusWords = []string{
	"Trabalhando",
	"Quebrando o Galho",
	"Lapidando",
	"Cozinhando 🔥",
	"Pensando",
	"Dando um jeito",
	"No capricho",
	"Fuçando",
	"Caprichando",
}

// workingWordSwapInterval is how long one word shows before the next. The swap
// derives from the turn's elapsed time, so no new ticker is needed.
const workingWordSwapInterval = 3 * time.Second

const (
	workingFireWord      = "Cozinhando 🔥"
	workingFireWordAscii = "Cozinhando"
)

// workingWordStartIndex draws the turn's first word from the turn-start time
// plus the run ID, both already injected (m.now, runID). No new seam.
func workingWordStartIndex(startedAt time.Time, runID int) int {
	n := len(workingStatusWords)
	if n == 0 {
		return 0
	}
	if startedAt.IsZero() {
		return 0
	}
	seed := startedAt.UnixNano() + int64(runID)
	index := int(seed % int64(n))
	if index < 0 {
		index += n
	}
	return index
}

// workingStatusWord is the label's current word. Reduced motion pins the brand
// word; otherwise (seed + elapsed/3s) mod N rotates without ever repeating the
// previous word. A zero turnStartedAt (fixtures) means slot 0.
func (m model) workingStatusWord() string {
	if m.reducedMotion {
		return workingStatusText
	}
	n := len(workingStatusWords)
	if n == 0 {
		return workingStatusText
	}
	seed := m.workingWordSeed
	if seed < 0 || seed >= n {
		seed = 0
	}
	slot := 0
	if !m.turnStartedAt.IsZero() && m.now != nil {
		elapsed := m.activeTurnElapsed(m.turnStartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		slot = int(elapsed / workingWordSwapInterval)
	}
	word := workingStatusWords[(seed+slot)%n]
	if m.workingWordAscii() && word == workingFireWord {
		return workingFireWordAscii
	}
	return word
}

// workingWordAscii reports whether the fire emoji must be dropped. NO_COLOR
// forces the Ascii profile; TERM=dumb resolves to NoTTY (which already pins
// reduced motion), so only the Ascii path needs the plain fallback.
func (m model) workingWordAscii() bool {
	return m.noColor || lipgloss.Writer.Profile == colorprofile.Ascii
}

// workingLabelCacheKey is the full animated-label identity: wave phase, word,
// and color profile. Theme swaps clear the table in applyTheme.
type workingLabelCacheKey struct {
	phase   int
	word    string
	profile colorprofile.Profile
}

var workingLabelCache = map[workingLabelCacheKey]string{}

func clearWorkingLabelCache() {
	workingLabelCache = map[workingLabelCacheKey]string{}
}

func workingLabelCachePhases() int {
	n := int(workingDriveCycle / activeAnimationFrameInterval)
	if n < 1 {
		return 1
	}
	return n
}

var workingDriveDelays = [...]time.Duration{
	workingDriveStep, 2 * workingDriveStep, 3 * workingDriveStep,
	0, workingDriveStep, 2 * workingDriveStep,
	workingDriveStep, 2 * workingDriveStep, 3 * workingDriveStep,
}

type workingDriveLevel uint8

const (
	workingDriveIdle workingDriveLevel = iota
	workingDriveDim
	workingDriveSoft
	workingDriveBright
)

// workingStatusIndicator renders a compact moving 3×3 chevron. Braille keeps
// the grid in one transcript row while preserving its actual changing shape.
func (m model) workingStatusIndicator() string {
	if m.reducedMotion {
		return ""
	}

	levels := workingDriveLevels(m.spinnerPhase)
	var out strings.Builder
	out.Grow(32)
	for group := 0; group < 2; group++ {
		bits := 0
		level := workingDriveIdle
		for row := 0; row < workingDriveGridHeight; row++ {
			for localColumn := 0; localColumn < 2; localColumn++ {
				column := group*2 + localColumn
				if column >= workingDriveGridWidth {
					continue
				}
				cell := levels[row*workingDriveGridWidth+column]
				if cell == workingDriveIdle {
					continue
				}
				bits |= workingDriveBrailleBit(localColumn, row)
				if cell > level {
					level = cell
				}
			}
		}
		out.WriteString(workingDriveStyle(level).Render(string(rune(0x2800 + bits))))
	}
	return out.String()
}

// workingStatusLabel continues the same wave after the grid. The first letter
// starts immediately after the final grid column, making one animation travel
// cleanly from the loader into the active label. The fire emoji keeps its own
// color (no wave style) while still occupying its 2 columns.
func (m model) workingStatusLabel() string {
	if m.reducedMotion {
		return qgTheme.ink.Render(workingStatusText)
	}

	word := m.workingStatusWord()
	phase := m.spinnerPhase
	if phase < 0 {
		phase = 0
	}
	key := workingLabelCacheKey{phase: phase % workingLabelCachePhases(), word: word, profile: lipgloss.Writer.Profile}
	if cached, ok := workingLabelCache[key]; ok {
		return cached
	}

	var out strings.Builder
	out.Grow(len(word) * 12)
	start := 4 * workingDriveStep
	for index, char := range []rune(word) {
		if char == '🔥' {
			out.WriteString(string(char))
			continue
		}
		delay := start + time.Duration(index)*workingDriveStep
		out.WriteString(workingDriveStyle(workingDriveLevelAt(m.spinnerPhase, delay)).Render(string(char)))
	}
	rendered := out.String()
	workingLabelCache[key] = rendered
	return rendered
}

func workingDriveLevels(phase int) [workingDriveGridWidth * workingDriveGridHeight]workingDriveLevel {
	var levels [workingDriveGridWidth * workingDriveGridHeight]workingDriveLevel
	for index, delay := range workingDriveDelays {
		levels[index] = workingDriveLevelAt(phase, delay)
	}
	return levels
}

func workingDriveBrailleBit(column, row int) int {
	if column == 0 {
		switch row {
		case 0:
			return 1
		case 1:
			return 2
		default:
			return 4
		}
	}
	switch row {
	case 0:
		return 8
	case 1:
		return 16
	default:
		return 32
	}
}

// workingDriveLevelAt computes one shared pulse track. A new grid front starts
// while the final label character still fades, so there is no dead blink between
// loops. The same timing function drives both the 3×3 grid and Working.
func workingDriveLevelAt(phase int, delay time.Duration) workingDriveLevel {
	if phase < 0 {
		phase = 0
	}
	age := time.Duration(phase)*activeAnimationFrameInterval - delay
	age %= workingDriveCycle
	if age < 0 {
		age += workingDriveCycle
	}
	switch {
	case age < workingDriveBrightWindow:
		return workingDriveBright
	case age < workingDriveSoftWindow:
		return workingDriveSoft
	case age < workingDriveDimWindow:
		return workingDriveDim
	default:
		return workingDriveIdle
	}
}

// workingDriveStyle gives the grid and label exactly the same theme-aware
// intensity ramp. The result is legible in ANSI, 256-color, and true-color
// terminals without painting a background behind either surface.
func workingDriveStyle(level workingDriveLevel) lipgloss.Style {
	switch level {
	case workingDriveBright:
		return qgTheme.accent
	case workingDriveSoft:
		return qgTheme.ink
	case workingDriveDim:
		return qgTheme.muted
	default:
		return qgTheme.faint
	}
}
