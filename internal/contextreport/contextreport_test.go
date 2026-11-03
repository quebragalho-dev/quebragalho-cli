package contextreport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestBuildCountsProjectGuidelinesAndFreeBudget(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "AGENTS.md", strings.Repeat("project rules\n", 200))

	registry := tools.NewRegistry()
	for _, tool := range tools.CoreToolsScoped(root, nil) {
		registry.Register(tool)
	}

	report, err := Build(Options{
		WorkspaceRoot: root,
		Provider: config.ProviderProfile{
			Name:         "openai",
			ProviderKind: config.ProviderKindOpenAI,
			Model:        "gpt-4.1",
		},
		Registry:      registry,
		ContextWindow: 20_000,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if report.Contract != ContractV1 {
		t.Fatalf("Contract = %q, want %q", report.Contract, ContractV1)
	}
	if report.Runtime != RuntimeGo {
		t.Fatalf("Runtime = %q, want %q", report.Runtime, RuntimeGo)
	}
	if report.Root != root {
		t.Fatalf("Root = %q, want %q", report.Root, root)
	}
	if report.ProviderName != "openai" || report.ModelID != "gpt-4.1" || report.APIModel != "gpt-4.1" {
		t.Fatalf("provider metadata = %#v", report)
	}
	if report.ContextWindow != 20_000 {
		t.Fatalf("ContextWindow = %d, want 20000", report.ContextWindow)
	}
	if report.ProjectGuidelineFile != "AGENTS.md" {
		t.Fatalf("ProjectGuidelineFile = %q, want AGENTS.md", report.ProjectGuidelineFile)
	}
	if report.ToolCount == 0 {
		t.Fatal("ToolCount = 0, want core tools counted")
	}
	if report.UsedTokens <= 0 {
		t.Fatalf("UsedTokens = %d, want > 0", report.UsedTokens)
	}
	if report.FreeTokens <= 0 {
		t.Fatalf("FreeTokens = %d, UsedTokens = %d, ContextWindow = %d, want > 0", report.FreeTokens, report.UsedTokens, report.ContextWindow)
	}
	if report.FreeTokens+report.UsedTokens != report.ContextWindow {
		t.Fatalf("free + used = %d, want %d", report.FreeTokens+report.UsedTokens, report.ContextWindow)
	}
	if !hasCategory(report, CategoryProjectGuidelines) {
		t.Fatalf("missing project guideline category: %#v", report.Categories)
	}
	if !hasCategory(report, CategoryTools) {
		t.Fatalf("missing tools category: %#v", report.Categories)
	}
	if !hasCategory(report, CategoryFree) {
		t.Fatalf("missing free category: %#v", report.Categories)
	}
}

func TestBuildHasStableJSONContractAndCategoryMath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "QG.md", strings.Repeat("qg rules\n", 16))

	registry := tools.NewRegistry()
	for _, tool := range tools.CoreToolsScoped(root, nil) {
		registry.Register(tool)
	}

	report, err := Build(Options{
		WorkspaceRoot:       root,
		Registry:            registry,
		ContextWindow:       50_000,
		ProjectContextFiles: []string{"AGENTS.md", "QG.md"},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	keys := categoryKeys(report)
	for _, want := range []string{CategorySystemPrompt, CategoryProjectGuidelines, CategoryTools, CategoryFree} {
		if !contains(keys, want) {
			t.Fatalf("missing category %q in %v", want, keys)
		}
	}
	if report.ProjectGuidelineFile != "QG.md" {
		t.Fatalf("ProjectGuidelineFile = %q, want QG.md", report.ProjectGuidelineFile)
	}
	if report.FreeTokens+report.UsedTokens != report.ContextWindow {
		t.Fatalf("free + used = %d, want %d", report.FreeTokens+report.UsedTokens, report.ContextWindow)
	}
	if report.UsedFraction != float64(report.UsedTokens)/float64(report.ContextWindow) {
		t.Fatalf("UsedFraction = %v, want used/context ratio", report.UsedFraction)
	}

	var decoded struct {
		Contract   string     `json:"contract"`
		Runtime    string     `json:"runtime"`
		Categories []Category `json:"categories"`
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.Contract != ContractV1 || decoded.Runtime != RuntimeGo {
		t.Fatalf("decoded identity = %q/%q", decoded.Contract, decoded.Runtime)
	}
	if len(decoded.Categories) != len(report.Categories) {
		t.Fatalf("decoded %d categories, want %d", len(decoded.Categories), len(report.Categories))
	}
}

func TestBuildReportsInitialDeferredToolSurface(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreToolsScoped(root, nil) {
		registry.Register(tool)
	}

	eager, err := Build(Options{WorkspaceRoot: root, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := Build(Options{WorkspaceRoot: root, Registry: registry, DeferThreshold: 3})
	if err != nil {
		t.Fatal(err)
	}
	if deferred.DeferredToolCount < 3 {
		t.Fatalf("DeferredToolCount = %d, want at least 3", deferred.DeferredToolCount)
	}
	if deferred.AvailableToolCount != eager.ToolCount {
		t.Fatalf("available tools = %d, want eager total %d", deferred.AvailableToolCount, eager.ToolCount)
	}
	if deferred.ToolCount >= deferred.AvailableToolCount {
		t.Fatalf("initial exposed tools = %d, available = %d; expected a smaller initial surface", deferred.ToolCount, deferred.AvailableToolCount)
	}
	if categoryByKey(deferred, CategoryTools).Tokens >= categoryByKey(eager, CategoryTools).Tokens {
		t.Fatalf("deferred tool schemas did not reduce the initial context")
	}
}

func TestBuildClampsFreeBudgetWhenOverContextWindow(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "AGENTS.md", strings.Repeat("very large project rule\n", 1000))

	report, err := Build(Options{
		WorkspaceRoot: root,
		ContextWindow: 10,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if report.UsedTokens <= report.ContextWindow {
		t.Fatalf("UsedTokens = %d, want over tiny context window %d", report.UsedTokens, report.ContextWindow)
	}
	if report.FreeTokens != 0 {
		t.Fatalf("FreeTokens = %d, want clamp to 0", report.FreeTokens)
	}
	free := categoryByKey(report, CategoryFree)
	if free == nil {
		t.Fatalf("missing free category: %#v", report.Categories)
	}
	if free.Tokens != 0 {
		t.Fatalf("free category tokens = %d, want 0", free.Tokens)
	}
}

func TestBuildAccountsForWorkspaceMapContext(t *testing.T) {
	root := t.TempDir()

	baseline, err := Build(Options{
		WorkspaceRoot: root,
		ContextWindow: 10_000,
	})
	if err != nil {
		t.Fatalf("Build baseline returned error: %v", err)
	}
	baselineCat := categoryByKey(baseline, CategoryWorkspaceMap)
	if baselineCat == nil {
		t.Fatalf("baseline missing workspace map category: %#v", baseline.Categories)
	}

	writeTestFile(t, root, "go.mod", "module example.test/repo\n")
	writeTestFile(t, root, "cmd/qg/main.go", "package main\n")
	writeTestFile(t, root, "README.md", "# Example\n")

	report, err := Build(Options{
		WorkspaceRoot: root,
		ContextWindow: 10_000,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	cat := categoryByKey(report, CategoryWorkspaceMap)
	if cat == nil {
		t.Fatalf("missing workspace map category: %#v", report.Categories)
	}
	if cat.Tokens <= 0 {
		t.Fatalf("workspace map tokens = %d, want > 0", cat.Tokens)
	}
	workspaceDelta := cat.Tokens - baselineCat.Tokens
	if workspaceDelta <= 0 {
		t.Fatalf("workspace map token delta = %d, want > 0 (baseline=%d final=%d)", workspaceDelta, baselineCat.Tokens, cat.Tokens)
	}
	if usedDelta := report.UsedTokens - baseline.UsedTokens; usedDelta != workspaceDelta {
		t.Fatalf("used token delta = %d, want workspace map delta %d", usedDelta, workspaceDelta)
	}
	if freeDelta := baseline.FreeTokens - report.FreeTokens; freeDelta != workspaceDelta {
		t.Fatalf("free token delta = %d, want workspace map delta %d", freeDelta, workspaceDelta)
	}
	formatted := Format(report)
	if !strings.Contains(formatted, "Mapa da pasta de trabalho") {
		t.Fatalf("Format missing workspace map category:\n%s", formatted)
	}
	if strings.Contains(formatted, "Workspace map") {
		t.Fatalf("Format kept English workspace map category:\n%s", formatted)
	}
}

func TestBuildShowsWorkspaceMapScanErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")

	report, err := Build(Options{
		WorkspaceRoot: root,
		ContextWindow: 10_000,
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	cat := categoryByKey(report, CategoryWorkspaceMap)
	if cat == nil {
		t.Fatalf("missing workspace map error category: %#v", report.Categories)
	}
	if cat.Tokens != 0 {
		t.Fatalf("workspace map error tokens = %d, want 0", cat.Tokens)
	}
	if !strings.Contains(cat.Name, "Workspace map (error:") {
		t.Fatalf("workspace map category name = %q, want visible error", cat.Name)
	}
	if formatted := Format(report); !strings.Contains(formatted, "Mapa da pasta de trabalho (erro: ") {
		t.Fatalf("Format missing workspace map error:\n%s", formatted)
	} else if strings.Contains(formatted, "Workspace map (error:") {
		t.Fatalf("Format kept English workspace map error:\n%s", formatted)
	}
}

func TestBuildWithoutProviderStillReturnsReport(t *testing.T) {
	root := t.TempDir()

	report, err := Build(Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	if report.Root != root {
		t.Fatalf("Root = %q, want %q", report.Root, root)
	}
	if report.ProviderName != "" || report.ModelID != "" || report.APIModel != "" {
		t.Fatalf("provider fields = %q/%q/%q, want empty", report.ProviderName, report.ModelID, report.APIModel)
	}
	if report.ContextWindow != 0 || report.UsedFraction != 0 || report.FreeTokens != 0 {
		t.Fatalf("unknown budget fields should be zero, got %#v", report)
	}
	if !hasCategory(report, CategorySystemPrompt) {
		t.Fatalf("missing system prompt category: %#v", report.Categories)
	}
	if !hasCategory(report, CategoryFree) {
		t.Fatalf("missing free category: %#v", report.Categories)
	}
}

func TestFormatIncludesRootModelAndCategories(t *testing.T) {
	report := Report{
		Contract:      ContractV1,
		Runtime:       RuntimeGo,
		Root:          "D:/repo",
		ProviderName:  "openai",
		ModelID:       "gpt-4.1",
		APIModel:      "gpt-4.1",
		ContextWindow: 1000,
		UsedTokens:    250,
		FreeTokens:    750,
		UsedFraction:  0.25,
		ToolCount:     2,
		Categories:    []Category{{Key: CategorySystemPrompt, Name: "System prompt", Tokens: 100, Percent: 10}, {Key: CategoryFree, Name: "Free", Tokens: 750, Percent: 75}},
	}

	formatted := Format(report)

	for _, want := range []string{"Relatório de contexto do QG", "pasta: D:/repo", "provedor: openai", "modelo: gpt-4.1", "modelo da API: gpt-4.1", "Prompt do sistema", "Livres"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("Format missing %q:\n%s", want, formatted)
		}
	}
	for _, banned := range []string{"QG context report", "root: D:/repo", "provider: openai", "\nmodel: gpt-4.1", "api_model:", "System prompt", "\nFree:"} {
		if strings.Contains(formatted, banned) {
			t.Fatalf("Format kept English %q:\n%s", banned, formatted)
		}
	}
}

func TestFormatHandlesUnknownContextWindow(t *testing.T) {
	report := Report{
		Contract:     ContractV1,
		Runtime:      RuntimeGo,
		Root:         "D:/repo",
		ProviderName: "openai",
		ModelID:      "gpt-4.1",
		APIModel:     "gpt-4.1",
		UsedTokens:   250,
		ToolCount:    2,
		Categories:   []Category{{Key: CategorySystemPrompt, Name: "System prompt", Tokens: 100}, {Key: CategoryFree, Name: "Free"}},
	}

	formatted := Format(report)

	for _, want := range []string{
		"Relatório de contexto do QG",
		"pasta: D:/repo",
		"provedor: openai",
		"modelo: gpt-4.1",
		"modelo da API: gpt-4.1",
		"uso: 250 tokens (janela de contexto desconhecida)",
		"Prompt do sistema: 100 tokens",
		"Livres: 0 tokens",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("Format missing %q:\n%s", want, formatted)
		}
	}
	for _, banned := range []string{"QG context report", "root: D:/repo", "usage: 250 tokens", "(context window unknown)", "System prompt: 100 tokens"} {
		if strings.Contains(formatted, banned) {
			t.Fatalf("Format kept English %q:\n%s", banned, formatted)
		}
	}
}

func hasCategory(report Report, key string) bool {
	return categoryByKey(report, key) != nil
}

func categoryByKey(report Report, key string) *Category {
	for _, category := range report.Categories {
		if category.Key == key {
			return &category
		}
	}
	return nil
}

func categoryKeys(report Report) []string {
	keys := make([]string, 0, len(report.Categories))
	for _, category := range report.Categories {
		keys = append(keys, category.Key)
	}
	return keys
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeTestFile(t *testing.T, root string, name string, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
}

func TestFormatToolLineCountsPortuguese(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		exposed, available, deferred int
		want                         []string
		banned                       []string
	}{
		{"plural", 16, 29, 14,
			[]string{"ferramentas: 16 expostas, 29 disponíveis, 14 adiadas"},
			[]string{"tools:", "exposed", "available", "deferred", "1 exposta", "1 disponível", "1 adiada"}},
		{"singular", 1, 1, 1,
			[]string{"ferramenta: 1 exposta"},
			[]string{"ferramentas: 1", "expostas", "exposed"}},
		{"diferida singular", 1, 2, 1,
			[]string{"ferramenta: 1 exposta, 2 disponíveis, 1 adiada"},
			[]string{"exposed", "available", "deferred", "1 adiadas"}},
		{"sem diferidas", 2, 0, 0,
			[]string{"ferramentas: 2 expostas"},
			[]string{"disponíveis", "adiadas", "available"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := Report{Contract: ContractV1, Runtime: RuntimeGo, Root: "D:/repo",
				ContextWindow: 1000, UsedTokens: 10, FreeTokens: 990, UsedFraction: 0.01,
				ToolCount: tc.exposed, AvailableToolCount: tc.available, DeferredToolCount: tc.deferred,
				Categories: []Category{{Key: CategoryFree, Name: "Free", Tokens: 990, Percent: 99}}}
			got := Format(report)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("Format %s missing %q:\n%s", tc.name, want, got)
				}
			}
			for _, banned := range tc.banned {
				if strings.Contains(got, banned) {
					t.Fatalf("Format %s kept %q:\n%s", tc.name, banned, got)
				}
			}
		})
	}
	// "1 disponível" não sai pelo Format: o sufixo diferido exige
	// available > exposed >= 1, então available == 1 nunca o alcança.
	// O singular do helper fica coberto direto.
	for _, tc := range []struct {
		n        int
		singular string
		plural   string
		want     string
	}{
		{1, "disponível", "disponíveis", "1 disponível"},
		{2, "disponível", "disponíveis", "2 disponíveis"},
		{1, "adiada", "adiadas", "1 adiada"},
	} {
		if got := contextCountNoun(tc.n, tc.singular, tc.plural); got != tc.want {
			t.Fatalf("contextCountNoun(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
