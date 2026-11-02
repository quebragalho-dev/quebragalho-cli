package tui

import (
	"fmt"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestBatchNineContextStatusSimple(t *testing.T) {
	for _, tc := range []struct{ id, want string }{{"muse-spark-1.3-contributor", "ctx 1.000.000"}, {"grok-4.7", "ctx 500.000"}} {
		t.Run(tc.id, func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.modelName = tc.id
			m.modelPickerLiveByProvider["qg"][0].ID = tc.id
			for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
				got := plainRender(t, m.statusLine(width))
				assertNotContains(t, got, "fallback/docs")
				if width >= 80 {
					assertContains(t, got, tc.want)
				}
				if lipgloss.Width(got) > width {
					t.Fatalf("width %d overflow: %q", width, got)
				}
			}
			n := int64(131072)
			m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &n
			assertContains(t, plainRender(t, m.statusLine(120)), "ctx 131.072")
		})
	}
}

func TestBatchNineProviderNotDiscoverable(t *testing.T) {
	assertNotContains(t, formatGroupedCommandHelp(), "/provider")
	for _, name := range listCommandNames() {
		if strings.HasPrefix(name, "/provider") {
			t.Errorf("provider in command names: %s", name)
		}
	}
	for _, suggestion := range matchCommandSuggestions("/pro") {
		if strings.HasPrefix(suggestion.Name, "/provider") {
			t.Errorf("provider suggested: %+v", suggestion)
		}
	}
	if _, ok := leaderCommandByKey['p']; ok {
		t.Error("provider leader shortcut remains")
	}
}

func TestBatchNineProviderInputsGuideQGLogin(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, input := range []string{"/provider", "/provider add", "/provider status", "/provider edit old", "/provider delete old", "/provider activate old", "/providers", "/PROVIDER add"} {
			t.Run(fmt.Sprintf("%s/pending=%v", input, pending), func(t *testing.T) {
				m := batchThreeLiveModel(t)
				m.pending = pending
				before := m.providerProfile
				saved := append(m.savedProviders[:0:0], m.savedProviders...)
				m.input.SetValue(input)
				updated, cmd := m.Update(testKey(tea.KeyEnter))
				next := updated.(model)
				if cmd != nil {
					t.Error("provider input returned executable work")
				}
				if next.picker != nil {
					t.Error("provider input opened management UI")
				}
				assertNotContains(t, plainRender(t, next.View()), "Provider setup")
				got := transcriptText(next.transcript)
				assertContains(t, got, "qg login")
				assertContains(t, got, "QG_API_KEY")
				assertContains(t, got, "QUEBRAGALHO_API_KEY")
				for _, unwanted := range []string{"Provider setup", "browse providers", "qg providers", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
					assertNotContains(t, got, unwanted)
				}
				if !reflect.DeepEqual(next.providerProfile, before) || !reflect.DeepEqual(next.savedProviders, saved) {
					t.Error("provider input mutated configuration")
				}
			})
		}
	}
}

func TestBatchNineDoctorFixGuidesLogin(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.providerProfile.Name = ""
	m.providerName = ""
	m.provider = nil
	m.input.SetValue("/doctor fix")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Error("doctor opened provider setup")
	}
	assertContains(t, transcriptText(next.transcript), "qg login")
}

func TestBatchNineModelPickerDoesNotOfferOtherProviders(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider = nil
	m.providerProfile.Name = "qg"
	m.savedProviders = []config.ProviderProfile{m.providerProfile, {Name: "legacy", Model: "foreign-model", APIKey: "fixture"}}
	m.recentModels = []config.RecentModelEntry{{Provider: "legacy", Model: "foreign-model"}}
	p := m.newModelPicker()
	if p == nil {
		t.Fatal("expected QG model picker")
	}
	for _, item := range p.items {
		if item.OwnerProvider == "legacy" || item.Value == "foreign-model" {
			t.Errorf("legacy provider offered: %+v", item)
		}
	}
}

func TestBatchNineStaleModelChoiceCannotActivateProvider(t *testing.T) {
	for _, title := range []string{"Choose a model", "Selecionar modelo"} {
		t.Run(title, func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.savedProviders = []config.ProviderProfile{{Name: "legacy", CatalogID: "qg", Model: "foreign-model", APIKey: "fixture"}}
			m.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) {
				t.Error("provider factory called")
				return &fakeProvider{}, nil
			}
			m.picker = &commandPicker{kind: pickerModel, title: title, effort: "invalid-level", items: []pickerItem{{Value: "foreign-model", OwnerProvider: "legacy"}}}
			updated, cmd := m.choosePicker()
			next := updated.(model)
			if cmd != nil || next.providerName != m.providerName || next.modelName != m.modelName {
				t.Error("stale choice changed provider/model")
			}
			assertContains(t, transcriptText(next.transcript)+next.transientNotice.text, "qg login")
		})
	}
}

func TestBatchNineTypedProviderReachesLogin(t *testing.T) {
	for _, input := range []string{"/provider", "/providers", "/provider add", "/provider status"} {
		t.Run(input, func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m = typeRunes(t, m, input)
			updated, cmd := m.Update(testKey(tea.KeyEnter))
			next := updated.(model)
			if cmd != nil {
				t.Error("typed provider returned executable work")
			}
			assertContains(t, transcriptText(next.transcript), "qg login")
			if next.suggestionsActive() {
				t.Error("legacy command kept command palette open")
			}
		})
	}
}
