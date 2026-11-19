package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// O Windows Terminal não entrega bracketed paste: um caminho copiado no
// Explorer chega como teclas digitadas, uma por caractere, então routePaste e
// droppedAttachmentPath nunca rodam e o caminho ficava como texto no composer
// até o usuário apertar Enter. Uma rajada de teclas (vários caracteres em
// poucos ms) que deixa o composer inteiro igual a um caminho anexável agora
// anexa como o Ctrl+V/arrastar faz.
func TestTypedPathBurstAttachesImage(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "Captura de Tela teste.png")
	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})

	next, cmd := typeBurst(m, img, time.Unix(100, 0))
	// A checagem é adiada depois da última tecla: nada é convertido antes dela.
	if len(next.pendingImages) != 0 {
		t.Fatalf("anexou no meio da rajada: %d pendentes", len(next.pendingImages))
	}
	if next.composerValue() != img {
		t.Fatalf("o texto da rajada deveria estar no composer, veio %q", next.composerValue())
	}
	check := typedPathCheckOf(t, cmd)

	checked, _ := next.Update(check)
	attached := checked.(model)
	if len(attached.pendingImages) != 1 {
		t.Fatalf("rajada com caminho de PNG deveria anexar, pendentes = %d", len(attached.pendingImages))
	}
	if attached.composerValue() != "" {
		t.Fatalf("o composer deveria ficar limpo depois do anexo, veio %q", attached.composerValue())
	}
	if chips := renderAttachmentChips(attached.pendingImageLabels, attached.pendingDocuments, attached.pendingVideoInfos); !strings.Contains(chips, "[Image #1]") {
		t.Fatalf("sem chip de prévia: %q", chips)
	}
}

// Digitar o caminho devagar é digitação humana, não colagem: continua texto no
// composer, e o Enter anexa como hoje.
func TestTypedPathWithHumanPaceStaysText(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "Captura de Tela teste.png")
	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})

	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	var cmd tea.Cmd
	for _, r := range img {
		var next tea.Model
		next, cmd = m.Update(testKeyText(string(r)))
		m = next.(model)
		now = now.Add(120 * time.Millisecond) // mais que a janela de rajada
	}
	if _, ok := typedPathCheckOfOK(cmd); ok {
		t.Fatal("digitação lenta não deveria agendar a checagem de caminho")
	}
	if len(m.pendingImages) != 0 {
		t.Fatalf("digitação lenta virou anexo: %d pendentes", len(m.pendingImages))
	}
	if m.composerValue() != img {
		t.Fatalf("composer = %q, want o caminho digitado", m.composerValue())
	}
}

// Rajada de texto comum (uma frase) não muda nada: fica texto, mesmo que a
// checagem adiada rode.
func TestPlainTextBurstStaysText(t *testing.T) {
	dir := t.TempDir()
	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})

	next, cmd := typeBurst(m, "corrige o bug do login", time.Unix(100, 0))
	if check, ok := typedPathCheckOfOK(cmd); ok {
		checked, _ := next.Update(check)
		next = checked.(model)
	}
	if next.composerValue() != "corrige o bug do login" {
		t.Fatalf("composer = %q", next.composerValue())
	}
	if len(next.pendingImages) != 0 || len(next.pendingDocuments) != 0 {
		t.Fatal("texto comum virou anexo")
	}
}

// Rajada de um caminho que não existe fica como texto: o usuário pode estar
// colando um caminho para a conversa.
func TestTypedPathBurstWithoutExistingFileStaysText(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nao existe.png")
	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})

	next, cmd := typeBurst(m, missing, time.Unix(100, 0))
	if check, ok := typedPathCheckOfOK(cmd); ok {
		checked, _ := next.Update(check)
		next = checked.(model)
	}
	if next.composerValue() != missing {
		t.Fatalf("composer = %q, want o caminho colado", next.composerValue())
	}
	if len(next.pendingImages) != 0 {
		t.Fatal("caminho inexistente virou anexo")
	}
}

// PDF colado como rajada segue o mesmo caminho do handleImageCommand.
func TestTypedPathBurstAttachesPDF(t *testing.T) {
	dir := t.TempDir()
	doc := writeTestPDF(t, dir, "relatorio.pdf", "conteúdo do relatório")
	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})

	next, cmd := typeBurst(m, doc, time.Unix(100, 0))
	check := typedPathCheckOf(t, cmd)
	checked, _ := next.Update(check)
	attached := checked.(model)
	if len(attached.pendingDocuments) != 1 {
		t.Fatalf("PDF colado como rajada deveria anexar, documentos = %d", len(attached.pendingDocuments))
	}
	if attached.composerValue() != "" {
		t.Fatalf("composer deveria ficar limpo, veio %q", attached.composerValue())
	}
}

// A checagem adiada chega depois do atraso real, quando o relógio já passou da
// janela de rajada: ela ainda precisa anexar. Este é o caminho que o teste de
// rajada acima não cobre, porque lá o relógio fica parado na última tecla.
func TestTypedPathCheckFiresAfterTheDelay(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "print.png")
	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})

	next, cmd := typeBurst(m, img, time.Unix(100, 0))
	check := typedPathCheckOf(t, cmd)

	// O tea.Tick entrega o msg só depois de typedPathCheckDelay: quando a
	// checagem roda, o relógio já passou da janela de rajada contada da última
	// tecla. O avanço é grande o suficiente para não depender do tempo que o
	// teste levou até aqui.
	later := next.typedPathLastAt.Add(typedPathCheckDelay + 5*time.Second)
	next.now = func() time.Time { return later }

	checked, _ := next.Update(check)
	attached := checked.(model)
	if len(attached.pendingImages) != 1 {
		t.Fatalf("a checagem adiada deveria anexar mesmo rodando depois do atraso, pendentes = %d", len(attached.pendingImages))
	}
	if attached.composerValue() != "" {
		t.Fatalf("composer deveria ficar limpo, veio %q", attached.composerValue())
	}
}

// Uma tecla depois da rajada cancela a checagem pendente: o caminho deixou de
// ser o composer inteiro, então nada é convertido.
func TestTypedPathCheckCancelledByLaterKey(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "print.png")
	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})

	next, cmd := typeBurst(m, img, time.Unix(100, 0))
	check := typedPathCheckOf(t, cmd)

	// O usuário continua digitando (dentro da janela de rajada) antes do tick.
	now := time.Unix(100, 0).Add(5 * time.Millisecond)
	next.now = func() time.Time { return now }
	typed, _ := next.Update(testKeyText("x"))
	next = typed.(model)

	checked, _ := next.Update(check)
	after := checked.(model)
	if len(after.pendingImages) != 0 {
		t.Fatal("tecla posterior à rajada deveria cancelar a checagem")
	}
	if after.composerValue() != img+"x" {
		t.Fatalf("composer = %q, want o caminho mais o caractere", after.composerValue())
	}
}

// typeBurst entrega o texto como teclas individuais dentro da janela de rajada
// e devolve o modelo e o comando da última tecla. O relógio é injetado, sem
// sleeps.
func typeBurst(m model, text string, at time.Time) (model, tea.Cmd) {
	now := at
	m.now = func() time.Time { return now }
	var cmd tea.Cmd
	for i, r := range text {
		if i > 0 {
			now = now.Add(2 * time.Millisecond)
		}
		var next tea.Model
		next, cmd = m.Update(testKeyText(string(r)))
		m = next.(model)
	}
	return m, cmd
}

// typedPathCheckOfOK runs the command (unwrapping a batch) and reports the
// deferred path check, if the command scheduled one.
func typedPathCheckOfOK(cmd tea.Cmd) (typedPathCheckMsg, bool) {
	if cmd == nil {
		return typedPathCheckMsg{}, false
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if check, ok := typedPathCheckOfOK(sub); ok {
				return check, true
			}
		}
		return typedPathCheckMsg{}, false
	}
	check, ok := msg.(typedPathCheckMsg)
	return check, ok
}
func typedPathCheckOf(t *testing.T, cmd tea.Cmd) typedPathCheckMsg {
	t.Helper()
	check, ok := typedPathCheckOfOK(cmd)
	if !ok {
		t.Fatal("a rajada de um caminho deveria agendar a checagem adiada")
	}
	return check
}
