package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	zsearch "github.com/quebragalho-dev/quebragalho-cli/internal/search"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// /search responde em português nos três ramos (uso, vazio, acerto).
func TestPTBRSearchCommand(t *testing.T) {
	m := newModel(context.Background(), Options{})

	empty := m.searchText("")
	for _, want := range []string{"Busca", "uso: /search <termo>"} {
		if !strings.Contains(empty, want) {
			t.Fatalf("search usage missing %q:\n%s", want, empty)
		}
	}
	for _, unwanted := range []string{"Search", "usage:", "<query>"} {
		if strings.Contains(empty, unwanted) {
			t.Fatalf("search usage leaked English %q:\n%s", unwanted, empty)
		}
	}

	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "ptbr-busca", Title: "Busca", Cwd: "/repo", ModelID: "gpt-4.1", Provider: "qg"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(session.SessionID, sessions.AppendEventInput{Type: sessions.EventMessage, Payload: map[string]string{"content": "agulha aparece aqui"}}); err != nil {
		t.Fatal(err)
	}
	withStore := newModel(context.Background(), Options{SessionStore: store})

	hit := withStore.searchText("agulha")
	for _, want := range []string{"Encontrado 1 evento de sessão local", "agulha aparece aqui", "modelo: gpt-4.1", "provedor: qg"} {
		if !strings.Contains(hit, want) {
			t.Fatalf("search hit missing %q:\n%s", want, hit)
		}
	}
	for _, unwanted := range []string{"Found ", "local session event", "model: gpt-4.1", "provider: qg", "updated:"} {
		if strings.Contains(hit, unwanted) {
			t.Fatalf("search hit leaked English %q:\n%s", unwanted, hit)
		}
	}

	miss := withStore.searchText("termo que não existe")
	for _, want := range []string{"Nenhum evento de sessão local corresponde a", "sessão pesquisada"} {
		if !strings.Contains(miss, want) {
			t.Fatalf("search miss missing %q:\n%s", want, miss)
		}
	}
	for _, unwanted := range []string{"No local session", "Searched ", "session"} {
		if strings.Contains(miss, unwanted) {
			t.Fatalf("search miss leaked English %q:\n%s", unwanted, miss)
		}
	}

	// O FormatResult pluraliza em português nos dois ramos.
	multi := zsearch.FormatResult(zsearch.Result{Query: "x", SearchedSessions: 2})
	if !strings.Contains(multi, "2 sessões pesquisadas") || strings.Contains(multi, "Searched") {
		t.Fatalf("empty result plural wrong:\n%s", multi)
	}
	single := zsearch.FormatResult(zsearch.Result{Query: "x", SearchedSessions: 1})
	if !strings.Contains(single, "1 sessão pesquisada") {
		t.Fatalf("empty result singular wrong:\n%s", single)
	}
}

// O erro de leitura da busca usa o prefixo em português; a causa original segue intacta.
func TestPTBRSearchCommandError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block session root creation"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{SessionStore: sessions.NewStore(sessions.StoreOptions{RootDir: blocker})})
	got := m.searchText("agulha")
	if !strings.Contains(got, "Busca\n") {
		t.Fatalf("search error missing Portuguese title:\n%s", got)
	}
	if !strings.Contains(got, "erro: ") {
		t.Fatalf("search error missing Portuguese prefix:\n%s", got)
	}
	if strings.Contains(got, "Search\n") || strings.Contains(got, "error: ") {
		t.Fatalf("search error leaked English:\n%s", got)
	}
}
