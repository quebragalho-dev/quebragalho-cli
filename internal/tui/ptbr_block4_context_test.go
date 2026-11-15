package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// /context rende execução, sessão e ferramentas em português, sem vazar o texto antigo.
func TestPTBRContextCommand(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewScopedReadFileTool(".", nil))
	m := newModel(context.Background(), Options{
		Cwd:            "/repo",
		ProviderName:   "qg",
		ModelName:      "muse-spark-1.3-contributor",
		Registry:       registry,
		PermissionMode: agent.PermissionModeAsk,
	})
	m.turnLatencySum = 9 * time.Second
	m.turnLatencyCount = 2

	got := m.contextText()
	for _, want := range []string{
		"Contexto",
		"runtime go | ask permissões | 1 ferramenta",
		"Execução",
		"provedor",
		"modelo",
		"nível",
		"estilo",
		"uso",
		"cache",
		"latência",
		"turnos máximos",
		"Sessão",
		"ativa",
		"raiz",
		"compactação",
		"ociosa",
		"Ferramentas",
		"registradas",
		"4.5s de média (2 turnos)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("/context missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"Context\n", "go runtime", " permissions", "1 tool",
		"\nRuntime", "provider", "model ", "effort", "style", "usage",
		"latency", "max turns", "Session", "active", "root", "compaction",
		"idle", "\nTools", "registered", "avg (", "to first token", "turns)",
		"unknown", "none",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("/context leaked English %q:\n%s", unwanted, got)
		}
	}

	// Latência com TTFT e resumo de uso vazio também falam português.
	m.turnTTFTSum = 3 * time.Second
	m.turnTTFTCount = 2
	if got := m.avgTurnLatencyText(); got != "4.5s de média (1.5s até o primeiro token, 2 turnos)" {
		t.Fatalf("latency with ttft = %q", got)
	}
	if got := m.usageSummaryText(); got != "ainda sem uso" {
		t.Fatalf("empty usage = %q, want ainda sem uso", got)
	}
	if got := (model{}).cacheEfficiencyText(); got != "uso indisponível" {
		t.Fatalf("cache without tracker = %q, want uso indisponível", got)
	}

	// Os estados de compactação conhecidos têm rótulo em português.
	for status, want := range map[string]string{
		"not compacted":            "ociosa",
		"compacting context":       "compactando contexto",
		"compacted manually":       "compactada manualmente",
		"manual compaction failed": "falha na compactação manual",
	} {
		if got := contextCompactionStatus(status); got != want {
			t.Fatalf("contextCompactionStatus(%q) = %q, want %q", status, got, want)
		}
	}
}
