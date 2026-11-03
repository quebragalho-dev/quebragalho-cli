package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// paletteLiveModel returns the batch fixture with a fully described live
// model: window, capabilities, bonus and BRL prices.
func paletteLiveModel(t *testing.T) model {
	t.Helper()
	m := batchThreeLiveModel(t)
	info := m.modelPickerLiveByProvider["qg"][0].APIModel
	window := int64(200000)
	info.ContextLength = &window
	info.Capabilities = qgaccount.ModelCapabilities{Tools: true, Vision: true, Reasoning: true}
	info.AcceptsBonus = true
	return m
}

func paletteKey(m model, msg tea.KeyPressMsg) model {
	next, _ := m.Update(msg)
	return next.(model)
}

func TestPaletteImmediateSearchFiltersWithoutApplying(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.picker = m.newModelPicker()
	m = paletteKey(m, testKeyText("z"))
	m = paletteKey(m, testKeyText("z"))
	m = paletteKey(m, testKeyText("z"))
	if m.picker == nil {
		t.Fatal("typing must not apply the selection")
	}
	if m.picker.query != "zzz" {
		t.Fatalf("query = %q, want zzz", m.picker.query)
	}
	got := plainRender(t, m.pickerOverlay(120))
	assertContains(t, got, "Nenhum modelo correspondente")
	m = paletteKey(m, testKey(tea.KeyBackspace))
	m = paletteKey(m, testKey(tea.KeyBackspace))
	m = paletteKey(m, testKey(tea.KeyBackspace))
	if len(m.picker.items) == 0 {
		t.Fatal("backspace must restore the filtered rows")
	}
}

func TestPaletteLetterDoesNotToggleFavorite(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.picker = m.newModelPicker()
	next, _ := m.Update(testKeyAltText("f"))
	m = next.(model)
	if !m.favoriteModels[m.modelName] {
		t.Fatal("Alt+f must favorite the highlighted model")
	}
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "* ")
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "Favoritos")
	next, _ = m.Update(testKeyAltText("f"))
	m = next.(model)
	if m.favoriteModels[m.modelName] {
		t.Fatal("Alt+f again must remove the favorite")
	}
	m = paletteKey(m, testKeyText("f"))
	if m.picker.query != "f" {
		t.Fatalf("query = %q, want f: letters must filter, not shortcut", m.picker.query)
	}
	if m.favoriteModels[m.modelName] {
		t.Fatal("typing f must not favorite the model")
	}
}

func TestPaletteTabOpensMarkedLadderAndEscReturns(t *testing.T) {
	m := batchThreeLiveModel(t)
	markedInfo := &qgaccount.ModelInfo{ID: "marked-low"}
	m.modelPickerLiveByProvider["qg"] = append(m.modelPickerLiveByProvider["qg"], providermodeldiscovery.Model{
		ID:               "marked-low",
		Source:           "live",
		APIModel:         markedInfo,
		ReasoningEfforts: []string{"low", "medium"},
	})
	m.picker = m.newModelPicker()
	m = paletteKey(m, testKeyText("marked"))
	if len(m.picker.items) == 0 {
		t.Fatal("search must find the marked model")
	}
	found := -1
	for i, item := range m.picker.items {
		if item.Value == "marked-low" {
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("marked model missing from %+v", pickerValues(m.picker.items))
	}
	m.picker.selected = found
	marked := m.picker.items[found].Value
	m = paletteKey(m, testKey(tea.KeyTab))
	if m.picker == nil || m.picker.kind != pickerEffort {
		t.Fatal("Tab must open the effort ladder")
	}
	if m.picker.effortModel != marked || m.picker.resume == nil {
		t.Fatalf("ladder must target the marked model, got %+v", m.picker)
	}
	got := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"baixo", "médio", "o modelo decide quanto pensar"} {
		assertContains(t, got, want)
	}
	for _, unwanted := range []string{"alto", "muito alto", "máximo"} {
		assertNotContains(t, got, unwanted)
	}
	m = paletteKey(m, testKey(tea.KeyEsc))
	if m.picker == nil || m.picker.kind != pickerModel {
		t.Fatal("Esc must return to the palette")
	}
	if m.picker.query != "marked" {
		t.Fatalf("Esc must keep the query, got %q", m.picker.query)
	}
	if item, ok := m.picker.current(); !ok || item.Value != marked {
		t.Fatal("Esc must keep the marked selection")
	}
}

func TestPaletteSelectedRowShowsCapabilitiesAndExitPrice(t *testing.T) {
	m := paletteLiveModel(t)
	m.height = 40
	m.modelPickerLiveByProvider["qg"] = append(m.modelPickerLiveByProvider["qg"], providermodeldiscovery.Model{
		ID: "extra-model", Source: "live", APIModel: &qgaccount.ModelInfo{ID: "extra-model"},
	})
	m.picker = m.newModelPicker()
	got := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"ferramentas", "imagens", "raciocínio", "bônus", "saída R$ 5,00", "200k", "R$ 1,65", "Recentes", "Catálogo"} {
		assertContains(t, got, want)
	}
	for _, unwanted := range []string{"tools", "vision", "reasoning"} {
		assertNotContains(t, got, unwanted)
	}
}

func TestLadderShowsAcceptedLevelsAndCurrent(t *testing.T) {
	m := paletteLiveModel(t)
	m.modelPickerLiveByProvider["qg"][0].ReasoningEfforts = []string{"low", "medium"}
	m.modelPickerLiveByProvider["qg"][0].Reasoning = true
	m.reasoningEffort = "medium"
	m = batchSixOpen(m, "/effort")
	got := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"automático", "baixo", "médio", "atual", "aceita de baixo a médio"} {
		assertContains(t, got, want)
	}
	for _, unwanted := range []string{"alto", "muito alto", "máximo", "nenhum", "mínimo"} {
		assertNotContains(t, got, unwanted)
	}
}

var styledSGR = regexp.MustCompile("\x1b\\[([0-9;]*)m")

// styledCellBGs records the background of every printable cell: "none" or
// "r,g,b". Resets (including bare \x1b[m) clear it, mirroring the terminal.
func styledCellBGs(line string) []string {
	bgs := []string{}
	cur := "none"
	rest := line
	for len(rest) > 0 {
		loc := styledSGR.FindStringSubmatchIndex(rest)
		if loc == nil {
			for range rest {
				bgs = append(bgs, cur)
			}
			break
		}
		for range rest[:loc[0]] {
			bgs = append(bgs, cur)
		}
		raw := rest[loc[2]:loc[3]]
		if strings.Trim(raw, ";") == "" {
			cur = "none"
		} else {
			parts := strings.Split(raw, ";")
			for i := 0; i < len(parts); i++ {
				switch parts[i] {
				case "0":
					cur = "none"
				case "48":
					if i+1 < len(parts) && parts[i+1] == "2" && i+4 < len(parts) {
						cur = parts[i+2] + "," + parts[i+3] + "," + parts[i+4]
						i += 4
					} else if i+1 < len(parts) && parts[i+1] == "5" {
						i++
					}
				case "49":
					cur = "none"
				case "38":
					if i+1 < len(parts) && parts[i+1] == "2" {
						i += 4
					} else if i+1 < len(parts) && parts[i+1] == "5" {
						i++
					}
				}
			}
		}
		rest = rest[loc[1]:]
	}
	return bgs
}

// assertSelectionBand checks that every inner cell of the given overlay rows
// carries the selection background: no holes between segments, no unpainted
// detail line. offset is the centering pad before the frame.
func assertSelectionBand(t *testing.T, overlay string, rows []int, offset, inner int, want string) {
	t.Helper()
	lines := strings.Split(overlay, "\n")
	for _, idx := range rows {
		if idx < 0 || idx >= len(lines) {
			t.Fatalf("selected row %d outside %d rendered lines", idx, len(lines))
		}
		cells := styledCellBGs(lines[idx])
		if len(cells) < offset+2+inner {
			t.Fatalf("row %d has %d cells, want at least %d", idx, len(cells), offset+2+inner)
		}
		for _, bg := range cells[offset+2 : offset+2+inner] {
			if bg != want {
				t.Fatalf("row %d has a cell without the selection background: %q", idx, lines[idx])
			}
		}
	}
}

func TestSelectedRowsCarrySelectionBackground(t *testing.T) {
	old := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	defer func() { lipgloss.Writer.Profile = old }()
	r, g, b, _ := qgTheme.bgSel.RGBA()
	want := fmt.Sprintf("%d,%d,%d", r>>8, g>>8, b>>8)
	m := paletteLiveModel(t)
	m.height = 40
	m.picker = m.newModelPicker()
	width := 120
	overlayWidth := paletteFrameWidth(width, modelPaletteMaxWidth)
	inner := overlayWidth - 4
	offset := (width - overlayWidth) / 2
	lines := strings.Split(m.pickerOverlay(width), "\n")
	sel := -1
	for i, line := range lines {
		if strings.Contains(line, "▌") {
			sel = i
			break
		}
	}
	if sel < 0 {
		t.Fatal("no selected palette row")
	}
	item, ok := m.picker.current()
	if !ok {
		t.Fatal("no current palette item")
	}
	rows := []int{sel}
	for k := range renderPaletteDetail(inner, item, true) {
		rows = append(rows, sel+1+k)
	}
	if len(rows) < 2 {
		t.Fatal("fixture must expand the selected row")
	}
	assertSelectionBand(t, m.pickerOverlay(width), rows, offset, inner, want)
	m.picker = m.newEffortPickerFor(strings.TrimSpace(m.modelName), m.effortDisplay(), nil)
	overlayWidth = paletteFrameWidth(width, effortLadderMaxWidth)
	inner = overlayWidth - 4
	offset = (width - overlayWidth) / 2
	lines = strings.Split(m.pickerOverlay(width), "\n")
	sel = -1
	for i, line := range lines {
		if strings.Contains(line, "›") {
			sel = i
			break
		}
	}
	if sel < 0 {
		t.Fatal("no selected ladder row")
	}
	assertSelectionBand(t, m.pickerOverlay(width), []int{sel}, offset, inner, want)
}

func TestLadderCurrentIsEffectiveForMarkedModel(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider["qg"] = append(m.modelPickerLiveByProvider["qg"], providermodeldiscovery.Model{
		ID: "marked-low", Source: "live",
		ReasoningEfforts: []string{"low", "medium"},
		APIModel:         &qgaccount.ModelInfo{ID: "marked-low"},
	})
	m.reasoningEffort = "high"
	m.picker = m.newModelPicker()
	// The palette title shows the session level for the active model.
	assertContains(t, plainRender(t, m.pickerOverlay(120)), "nível alto")
	for i, item := range m.picker.items {
		if item.Value == "marked-low" {
			m.picker.selected = i
		}
	}
	m = paletteKey(m, testKey(tea.KeyTab))
	got := plainRender(t, m.pickerOverlay(120))
	// High is not accepted by the marked model, so "atual" falls back to the
	// level that effectively applies to it instead of pointing nowhere.
	auto := false
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "atual") {
			if !strings.Contains(line, "automático") {
				t.Fatalf("atual must sit on automático, got %q", line)
			}
			auto = true
		}
	}
	if !auto {
		t.Fatal("ladder must tag the effective level atual")
	}
}

func TestPaletteAndLadderFitRequiredWidths(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	m := paletteLiveModel(t)
	m.height = 40
	m.picker = m.newModelPicker()
	for _, width := range []int{50, 80, 120} {
		for name, overlay := range map[string]string{"palette": m.pickerOverlay(width), "ladder": m.effortLadderForTest(width)} {
			rendered := plainRender(t, overlay)
			for _, line := range strings.Split(rendered, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("%s line is %d cells, want <=%d: %q", name, got, width, line)
				}
			}
			// Chrome must survive intact: overflow truncates the row end, so a
			// right-aligned hint is the first victim of a too-wide frame.
			for _, want := range map[string][]string{
				"palette": {"modelo", "nível automático · Tab", "busque por nome", "Enter usar", "Alt+f favoritar", "Esc fechar"},
				"ladder":  {"Nível", "aceita de", "usar sempre", "Esc fechar"},
			}[name] {
				assertContains(t, rendered, want)
			}
		}
	}
	got50 := plainRender(t, m.pickerOverlay(50))
	// The 50-column palette keeps window and entry price and cuts the name.
	assertContains(t, got50, "200k · R$ 1,65")
	assertContains(t, got50, "muse-spark-1.3-contri…")
	assertNotContains(t, got50, "muse-spark-1.3-contributor")
	// The detail breaks into capabilities + exit price instead of clipping
	// the capability list mid-separator.
	assertContains(t, got50, "raciocínio · bônus")
	assertContains(t, got50, "saída R$ 5,00")
	assertNotContains(t, got50, "imagens ·…")
	got120 := plainRender(t, m.pickerOverlay(120))
	assertContains(t, got120, "200k · R$ 1,65")
	assertContains(t, got120, "muse-spark-1.3-contributor")
	// The 50-column ladder drops the hint column; dots and "atual" stay.
	ladder50 := plainRender(t, m.effortLadderForTest(50))
	assertNotContains(t, ladder50, "respostas")
	assertNotContains(t, ladder50, "pensa com")
	assertContains(t, ladder50, "●")
	assertContains(t, ladder50, "atual")
	ladder80 := plainRender(t, m.effortLadderForTest(80))
	assertContains(t, ladder80, "respostas rápidas")
}

// effortLadderForTest renders the ladder for the active model the way Tab
// would, without leaving the palette open.
func (m model) effortLadderForTest(width int) string {
	kept := m.picker
	m.picker = m.newEffortPickerFor(strings.TrimSpace(m.modelName), m.effortDisplay(), nil)
	defer func() { m.picker = kept }()
	return m.pickerOverlay(width)
}

func TestPaletteLightThemeRenders(t *testing.T) {
	previous := qgTheme
	_, qgTheme = themeForMode(themeLight, false)
	defer func() { qgTheme = previous }()
	m := paletteLiveModel(t)
	m.height = 40
	m.picker = m.newModelPicker()
	for _, overlay := range []string{m.pickerOverlay(80), m.effortLadderForTest(80)} {
		for _, line := range strings.Split(plainRender(t, overlay), "\n") {
			if got := lipgloss.Width(line); got > 80 {
				t.Fatalf("light theme line is %d cells, want <=80: %q", got, line)
			}
		}
	}
	assertContains(t, plainRender(t, m.pickerOverlay(80)), "busque por nome")
}

func TestFormatPalettePriceBRL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1.648", "R$ 1,65"},
		{"31.93", "R$ 31,93"},
		{"0.004", "< R$ 0,01"},
		{"0", ""},
		{"-1.5", ""},
		{"", ""},
	} {
		if got := formatPalettePriceBRL(tc.in); got != tc.want {
			t.Errorf("formatPalettePriceBRL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTabLadderSessionAndDefaultApply(t *testing.T) {
	for _, session := range []bool{true, false} {
		t.Run(map[bool]string{true: "session", false: "default"}[session], func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.providerProfile.Name = "qg"
			m.providerProfile.ProviderKind = config.ProviderKindOpenAICompatible
			m.providerProfile.Model = m.modelName
			m.providerProfile.APIKey = "fixture"
			m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
			if _, err := config.UpsertProvider(m.userConfigPath, m.providerProfile, true); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(m.userConfigPath)
			info := qgaccount.ModelInfo{ID: "ladder-live-model", Capabilities: qgaccount.ModelCapabilities{Reasoning: true}}
			m.modelPickerLiveByProvider["qg"] = append(m.modelPickerLiveByProvider["qg"], providermodeldiscovery.Model{ID: info.ID, Source: "live", Reasoning: true, APIModel: &info})
			m.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) { return &fakeProvider{}, nil }
			m = batchSixOpen(m, "/model")
			m.picker.selected = 1
			marked := m.picker.items[1].Value
			m = paletteKey(m, testKey(tea.KeyTab))
			if m.picker == nil || m.picker.kind != pickerEffort || m.picker.effortModel != marked {
				t.Fatal("Tab must open the marked model ladder")
			}
			m = paletteKey(m, testKey(tea.KeyDown))
			if session {
				next, _ := m.Update(testKeyAltText("s"))
				m = next.(model)
			} else {
				m = paletteKey(m, testKey(tea.KeyEnter))
			}
			if m.picker != nil {
				t.Fatal("ladder apply must close the picker")
			}
			if m.modelName != marked || m.reasoningEffort != "low" {
				t.Fatalf("ladder apply failed: %s %s", m.modelName, m.reasoningEffort)
			}
			after, _ := os.ReadFile(m.userConfigPath)
			if session && string(after) != string(before) {
				t.Fatal("session ladder apply wrote defaults")
			}
			if !session && !strings.Contains(string(after), marked) {
				t.Fatalf("default ladder apply did not save the model: %s", after)
			}
		})
	}
}
