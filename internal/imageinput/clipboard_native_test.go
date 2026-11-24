package imageinput

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// A leitura nativa do macOS roda no processo, sem osascript. Estes testes usam
// um leitor falso e valem em qualquer SO: provam o despacho (nativo primeiro,
// osascript só de reserva), o prazo e a guarda que impede goroutines empilhadas.

// stubNativeClipboard troca a leitura nativa pela resposta dada.
func stubNativeClipboard(t *testing.T, read func(imageWithFiles bool) (ClipboardContent, error)) *int {
	t.Helper()
	calls := new(int)
	prev := clipboardNativeReader
	t.Cleanup(func() { clipboardNativeReader = prev })
	clipboardNativeReader = func(imageWithFiles bool) (ClipboardContent, error) {
		*calls++
		return read(imageWithFiles)
	}
	return calls
}

// Com a leitura nativa disponível, o osascript não sobe: é isso que tira os
// 50–100 ms de cada Ctrl+V.
func TestReadClipboardContentDarwinUsesNativeReaderFirst(t *testing.T) {
	want := ClipboardContent{Files: []string{"/Users/g/a.pdf"}}
	osaCalls, _, _ := stubDarwinContent(t, func(string) (string, error) {
		t.Fatal("osascript não deveria rodar com a leitura nativa disponível")
		return "", nil
	})
	nativeCalls := stubNativeClipboard(t, func(bool) (ClipboardContent, error) { return want, nil })

	got, err := readClipboardContentDarwin(true)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if !sameClipboardContent(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if *nativeCalls != 1 || *osaCalls != 0 {
		t.Fatalf("nativo=%d osascript=%d, want 1 e 0", *nativeCalls, *osaCalls)
	}
}

// Se o AppKit não carrega, o osascript atual segue como reserva, com a mesma
// regra e a mesma resposta.
func TestReadClipboardContentDarwinFallsBackToOsascriptWhenNativeUnavailable(t *testing.T) {
	osaCalls, _, _ := stubDarwinContent(t, reportReply(t, darwinClipboardReport{
		Files: []string{"/Users/g/reserva.png"},
	}, false))
	stubNativeClipboard(t, func(bool) (ClipboardContent, error) {
		return ClipboardContent{}, errNativeClipboardUnavailable
	})

	got, err := readClipboardContentDarwin(false)
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if want := []string{"/Users/g/reserva.png"}; len(got.Files) != 1 || got.Files[0] != want[0] {
		t.Fatalf("Files = %#v, want %#v", got.Files, want)
	}
	if *osaCalls != 1 {
		t.Fatalf("osascript chamado %d vezes, want 1 de reserva", *osaCalls)
	}
}

// Prazo estourado na leitura nativa é erro acionável; não cai no osascript, que
// travaria pelo mesmo motivo e dobraria a espera.
func TestReadClipboardContentDarwinKeepsNativeTimeout(t *testing.T) {
	osaCalls, _, _ := stubDarwinContent(t, reportReply(t, darwinClipboardReport{}, false))
	stubNativeClipboard(t, func(bool) (ClipboardContent, error) {
		return ClipboardContent{}, &ClipboardTimeoutError{Tool: nativeClipboardTool}
	})

	_, err := readClipboardContentDarwin(false)
	if !IsClipboardTimeout(err) {
		t.Fatalf("err = %v, want erro de prazo", err)
	}
	if *osaCalls != 0 {
		t.Fatalf("osascript chamado %d vezes após timeout nativo, want 0", *osaCalls)
	}
}

// Leitura presa: o prazo devolve ClipboardTimeoutError no tempo, e enquanto a
// leitura anterior não terminar, a próxima espera o slot até o prazo e então
// falha, sem criar outra goroutine. Quando a leitura presa termina, a seguinte
// volta a funcionar.
func TestRunClipboardReadDeadlineDoesNotStackReads(t *testing.T) {
	prevTimeout := clipboardTimeout
	clipboardTimeout = 50 * time.Millisecond
	t.Cleanup(func() { clipboardTimeout = prevTimeout })

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStuck := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseStuck)
	started := make(chan struct{}, 4)
	calls := 0
	stuck := func() (ClipboardContent, error) {
		calls++
		started <- struct{}{}
		<-release
		return ClipboardContent{Files: []string{"/liberada.pdf"}}, nil
	}

	start := time.Now()
	_, err := runClipboardRead(stuck)
	if !IsClipboardTimeout(err) {
		t.Fatalf("primeira leitura: err = %v, want erro de prazo", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("prazo levou %s, want perto de %s", elapsed, clipboardTimeout)
	}
	<-started

	// Ainda presa: a segunda não pode abrir outra goroutine nem chamar stuck.
	// Roda fora da goroutine do teste para que, se a guarda falhar, o teste
	// reporte o erro em vez de travar.
	second := make(chan error, 1)
	go func() {
		_, err := runClipboardRead(stuck)
		second <- err
	}()
	select {
	case err := <-second:
		if !IsClipboardTimeout(err) {
			t.Fatalf("segunda leitura enquanto a primeira está presa: err = %v, want erro de prazo", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("segunda leitura ficou presa: a guarda deixou outra chamada entrar")
	}
	if calls != 1 {
		t.Fatalf("leituras empilhadas: calls = %d, want 1", calls)
	}

	releaseStuck()
	deadline := time.Now().Add(2 * time.Second)
	for {
		content, err := runClipboardRead(func() (ClipboardContent, error) {
			return ClipboardContent{Files: []string{"/depois.pdf"}}, nil
		})
		if err == nil {
			if len(content.Files) != 1 || content.Files[0] != "/depois.pdf" {
				t.Fatalf("Files = %#v, want /depois.pdf", content.Files)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a guarda não liberou depois da leitura presa: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Ctrl+V seguido de outro, com a primeira leitura ainda em curso (um TIFF grande
// leva dezenas de milissegundos): a segunda espera o slot dentro do prazo e as
// duas dão certo. Antes, a segunda voltava com erro de prazo na hora.
func TestRunClipboardReadWaitsForSlotWithinDeadline(t *testing.T) {
	prevTimeout := clipboardTimeout
	clipboardTimeout = 30 * time.Second
	t.Cleanup(func() { clipboardTimeout = prevTimeout })

	firstStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := runClipboardRead(func() (ClipboardContent, error) {
			close(firstStarted)
			time.Sleep(100 * time.Millisecond)
			return ClipboardContent{Files: []string{"/primeira.pdf"}}, nil
		})
		firstDone <- err
	}()
	<-firstStarted

	got, err := runClipboardRead(func() (ClipboardContent, error) {
		return ClipboardContent{Files: []string{"/segunda.pdf"}}, nil
	})
	if err != nil {
		t.Fatalf("segunda leitura com a primeira em curso: err = %v, want none", err)
	}
	if len(got.Files) != 1 || got.Files[0] != "/segunda.pdf" {
		t.Fatalf("Files = %#v, want /segunda.pdf", got.Files)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("primeira leitura: err = %v, want none", err)
	}
}

// O resultado da leitura chega ao chamador sem alteração.
func TestRunClipboardReadReturnsResult(t *testing.T) {
	want := ClipboardContent{Image: []byte("\x89PNG\r\n\x1a\nx"), MediaType: "image/png"}
	got, err := runClipboardRead(func() (ClipboardContent, error) { return want, nil })
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if string(got.Image) != string(want.Image) || got.MediaType != want.MediaType {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Um pânico dentro da leitura nativa não derruba a TUI: vira indisponível e o
// osascript assume.
func TestRunClipboardReadRecoversPanicAsUnavailable(t *testing.T) {
	_, err := runClipboardRead(func() (ClipboardContent, error) {
		panic("leitura nativa quebrou")
	})
	if !errors.Is(err, errNativeClipboardUnavailable) {
		t.Fatalf("err = %v, want errNativeClipboardUnavailable", err)
	}
	// O slot foi liberado: a próxima leitura roda.
	if _, err := runClipboardRead(func() (ClipboardContent, error) { return ClipboardContent{}, nil }); err != nil {
		t.Fatalf("leitura seguinte após pânico: err = %v, want none", err)
	}
}

// sameClipboardContent compara o conteúdo sem depender de nil versus vazio.
func sameClipboardContent(a, b ClipboardContent) bool {
	if a.MediaType != b.MediaType || len(a.Image) != len(b.Image) || len(a.Files) != len(b.Files) {
		return false
	}
	for i := range a.Files {
		if a.Files[i] != b.Files[i] {
			return false
		}
	}
	return string(a.Image) == string(b.Image)
}
