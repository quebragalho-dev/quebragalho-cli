package providermodelcatalog

import (
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

type Model struct {
	ID                     string
	Description            string
	ContextWindow          int
	ToolCall               bool
	Reasoning              bool
	ReasoningEfforts       []string
	DefaultReasoningEffort string
	ServiceTiers           []string
	DefaultServiceTier     string
	InputModalities        []string
	OutputModalities       []string
	InputCost              float64
	OutputCost             float64
	Tags                   []string
	Source                 string
}

// qgCuratedModels is the fixed offline/pre-key fallback for the single QG
// provider: the model ids served by https://api.quebragalho.dev/v1 (observed
// via GET /v1/models). The live discovery probe replaces this whenever a key
// is present; these are the picker's default order when offline.
var qgCuratedModels = []Model{
	{ID: "muse-spark-1.3-contributor", Description: "catalog default (QG default coding model)"},
	{ID: "deepseek-v4-pro", Description: "QG model"},
	{ID: "deepseek-v4.1-flash", Description: "QG model"},
	{ID: "glm-5.3", Description: "QG model"},
	{ID: "glm-5.3-flash", Description: "QG model"},
	{ID: "gpt-5.6-luna", Description: "QG model"},
	{ID: "gpt-5.6-sol", Description: "QG model"},
	{ID: "gpt-6-astra", Description: "QG model"},
	{ID: "gpt-6-luna", Description: "QG model"},
	{ID: "gpt-6-sol", Description: "QG model"},
	{ID: "gpt-6.1-sol", Description: "QG model"},
	{ID: "grok-4.7", Description: "QG model"},
	{ID: "hy4", Description: "QG model"},
	{ID: "kimi-k3", Description: "QG model"},
	{ID: "mimo-v2.6-flash", Description: "QG model"},
	{ID: "mimo-v2.6-pro", Description: "QG model"},
	{ID: "mimo-v2.6-pro-ultraspeed", Description: "QG model"},
	{ID: "qwen3.8-max", Description: "QG model"},
	{ID: "qwen3.8-omni-flash", Description: "QG model"},
}

var curatedModels = map[string][]Model{
	"qg": qgCuratedModels,
}

func Models(provider providercatalog.Descriptor) []Model {
	return dedupeModels(provider.DefaultModel, curatedModels[provider.ID])
}

func dedupeModels(defaultModel string, models []Model) []Model {
	result := []Model{}
	seen := map[string]bool{}
	add := func(model Model) {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || seen[model.ID] {
			return
		}
		if model.Description == "" {
			model.Description = "catalog model"
		}
		model.InputModalities = append([]string{}, model.InputModalities...)
		model.OutputModalities = append([]string{}, model.OutputModalities...)
		model.ReasoningEfforts = append([]string{}, model.ReasoningEfforts...)
		model.ServiceTiers = append([]string{}, model.ServiceTiers...)
		model.Tags = append([]string{}, model.Tags...)
		seen[model.ID] = true
		result = append(result, model)
	}
	defaultEntry := Model{ID: defaultModel, Description: "catalog default"}
	for _, model := range models {
		if strings.TrimSpace(model.ID) == strings.TrimSpace(defaultModel) {
			defaultEntry = model
			defaultEntry.Description = "catalog default"
			break
		}
	}
	add(defaultEntry)
	for _, model := range models {
		add(model)
	}
	return result
}
