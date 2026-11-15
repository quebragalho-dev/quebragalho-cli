package execution

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func sm6Root(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("caracterizacao usa /bin/sh (POSIX)")
	}
	return t.TempDir()
}

func sm6StartSleep(t *testing.T, manager *ProcessManager, root string) ProcessResult {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	started, err := manager.Start(context.Background(), ProcessStart{
		Prepared: PreparedCommand{Command: cmd},
		Request:  processManagerRequest(root, cmd),
	}, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Start sleep: %v", err)
	}
	if started.Exited {
		t.Fatalf("sleep deveria continuar rodando, obteve %#v", started)
	}
	return started
}

func sm6WaitDone(t *testing.T, manager *ProcessManager, id int) *managedProcess {
	t.Helper()
	proc, ok := manager.get(id)
	if !ok {
		t.Fatalf("processo %d sumiu do mapa antes do done", id)
	}
	select {
	case <-proc.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("processo %d nao publicou done em 10s", id)
	}
	return proc
}

// Snapshot/List/Len em cada fase, incluindo a janela reaped-sem-done.
// A divergencia Len x List (Len conta retidos, List filtra) e comportamento atual.
func TestSm6SnapshotListLenPorFase(t *testing.T) {
	root := sm6Root(t)
	manager := NewProcessManager(ProcessManagerOptions{CompletedRetention: time.Hour})

	// Fase running.
	running := sm6StartSleep(t, manager, root)
	snap, ok := manager.Snapshot(running.ProcessID)
	if !ok || snap.Status != "running" || snap.ExitCode != nil {
		t.Fatalf("snapshot running = %#v ok=%v, want running sem exit", snap, ok)
	}
	if snap.ID != running.ProcessID || snap.ID < 1000 {
		t.Fatalf("snapshot ID = %d, want >=1000 estavel", snap.ID)
	}
	if got := len(manager.List()); got != 1 {
		t.Fatalf("List em running = %d, want 1", got)
	}
	if got := manager.Len(); got != 1 {
		t.Fatalf("Len em running = %d, want 1", got)
	}
	manager.Stop(running.ProcessID)
	manager.Remove(running.ProcessID)

	// Janela reaped-sem-done via AfterWait deterministico.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once int32
	manager2 := NewProcessManager(ProcessManagerOptions{CompletedRetention: time.Hour})
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	type startOut struct {
		result ProcessResult
		err    error
	}
	doneCh := make(chan startOut, 1)
	go func() {
		res, err := manager2.Start(context.Background(), ProcessStart{
			Prepared: PreparedCommand{Command: cmd},
			Request:  processManagerRequest(root, cmd),
			AfterWait: func() []byte {
				if atomic.CompareAndSwapInt32(&once, 0, 1) {
					close(entered)
				}
				<-release
				return nil
			},
		}, 50*time.Millisecond)
		doneCh <- startOut{res, err}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("AfterWait nunca entrou (janela reaped nao alcancada)")
	}
	// Acha o id retido (so ha um).
	list := manager2.List()
	if len(list) != 1 {
		t.Fatalf("List na janela reaped = %d, want 1 (inclui)", len(list))
	}
	id := list[0].ID
	snap, ok = manager2.Snapshot(id)
	if !ok {
		t.Fatalf("Snapshot na janela reaped sumiu")
	}
	if snap.Status != "running" {
		t.Fatalf("snapshot na janela reaped = %q, want running (exitCode ainda nil)", snap.Status)
	}
	if snap.ExitCode != nil {
		t.Fatalf("snapshot na janela reaped tem exit, want nil")
	}
	proc, ok := manager2.get(id)
	if !ok {
		t.Fatalf("get na janela reaped falhou")
	}
	if !proc.reapedClosed() {
		t.Fatalf("reaped deveria estar fechado na janela")
	}
	if proc.doneClosed() {
		t.Fatalf("done deveria estar aberto na janela")
	}
	if got := manager2.Len(); got != 1 {
		t.Fatalf("Len na janela reaped = %d, want 1", got)
	}
	// Stop na janela nao deve sinalizar (reaped ja fechado) mas retorna true.
	var kills int32
	proc.kill = func(int) error { atomic.AddInt32(&kills, 1); return nil }
	if !manager2.Stop(id) {
		t.Fatalf("Stop na janela reaped retornou false, want true (achou)")
	}
	if got := atomic.LoadInt32(&kills); got != 0 {
		t.Fatalf("Stop na janela reaped sinalizou %d vezes, want 0", got)
	}
	// Start ainda esta no collect (50ms) com AfterWait bloqueado: deve
	// retornar running ANTES do release, senao o markDone contaminaria o resultado.
	var out startOut
	select {
	case out = <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("Start nao retornou com AfterWait bloqueado")
	}
	close(release)
	if out.err != nil {
		t.Fatalf("Start: %v", out.err)
	}
	if out.result.Exited {
		t.Fatalf("Start com AfterWait bloqueado deveria retornar running, obteve exited")
	}
	sm6WaitDone(t, manager2, id)
	snap, ok = manager2.Snapshot(id)
	if !ok || snap.Status != "exited" || snap.ExitCode == nil {
		t.Fatalf("snapshot done = %#v ok=%v, want exited com exit", snap, ok)
	}
	// Divergencia documentada: List filtra concluido retido, Len conta.
	if got := len(manager2.List()); got != 0 {
		t.Fatalf("List em done retido = %d, want 0 (filtra)", got)
	}
	if got := manager2.Len(); got != 1 {
		t.Fatalf("Len em done retido = %d, want 1 (conta retido)", got)
	}
	manager2.Remove(id)
}

// Stop em cada fase: 1 kill em running, 0 depois de reaped e em done.
func TestSm6StopKillsPorFase(t *testing.T) {
	root := sm6Root(t)
	manager := NewProcessManager(ProcessManagerOptions{CompletedRetention: time.Hour})

	// Running: 1 chamada.
	running := sm6StartSleep(t, manager, root)
	proc, _ := manager.get(running.ProcessID)
	var killsRun int32
	proc.kill = func(int) error { atomic.AddInt32(&killsRun, 1); return nil }
	if !manager.Stop(running.ProcessID) {
		t.Fatalf("Stop em running = false, want true")
	}
	if got := atomic.LoadInt32(&killsRun); got != 1 {
		t.Fatalf("Stop em running sinalizou %d vezes, want 1", got)
	}
	manager.Remove(running.ProcessID)

	// Reaped (AfterWait bloqueado): 0 chamadas.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once int32
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	started := make(chan ProcessResult, 1)
	go func() {
		res, err := manager.Start(context.Background(), ProcessStart{
			Prepared: PreparedCommand{Command: cmd},
			Request:  processManagerRequest(root, cmd),
			AfterWait: func() []byte {
				if atomic.CompareAndSwapInt32(&once, 0, 1) {
					close(entered)
				}
				<-release
				return nil
			},
		}, 50*time.Millisecond)
		if err == nil {
			started <- res
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("AfterWait nao entrou")
	}
	id := manager.List()[0].ID
	proc, _ = manager.get(id)
	var killsReaped int32
	proc.kill = func(int) error { atomic.AddInt32(&killsReaped, 1); return nil }
	manager.Stop(id)
	if got := atomic.LoadInt32(&killsReaped); got != 0 {
		t.Fatalf("Stop em reaped sinalizou %d vezes, want 0", got)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatalf("Start nao retornou com AfterWait bloqueado")
	}
	close(release)
	sm6WaitDone(t, manager, id)

	// Done retido: 0 chamadas.
	proc, _ = manager.get(id)
	var killsDone int32
	proc.kill = func(int) error { atomic.AddInt32(&killsDone, 1); return nil }
	if !manager.Stop(id) {
		t.Fatalf("Stop em done = false, want true (ainda no mapa)")
	}
	if got := atomic.LoadInt32(&killsDone); got != 0 {
		t.Fatalf("Stop em done sinalizou %d vezes, want 0", got)
	}
	manager.Remove(id)
	if manager.Stop(id) {
		t.Fatalf("Stop apos Remove = true, want false")
	}
}

// Continue/WriteInput/ResizeInput em processo concluido (pipe, sem PTY).
func TestSm6InputOpsEmConcluido(t *testing.T) {
	root := sm6Root(t)
	manager := NewProcessManager(ProcessManagerOptions{CompletedRetention: time.Hour})
	cmd := exec.Command("/bin/sh", "-c", "exit 3")
	started, err := manager.Start(context.Background(), ProcessStart{
		Prepared: PreparedCommand{Command: cmd},
		Request:  processManagerRequest(root, cmd),
	}, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	id := started.ProcessID
	// Se o Start rapido ja colheu o exit, ele remove na hora; nesse caso
	// o comportamento atual e ErrProcessNotFound nas ops seguintes.
	// Se ainda estava rodando, espera o done retido e fixa o comportamento retido.
	if started.Exited {
		if _, err := manager.Continue(context.Background(), ProcessContinue{ProcessID: id}); !errors.Is(err, ErrProcessNotFound) {
			t.Fatalf("Continue apos Start exited imediato = %v, want ErrProcessNotFound (Remove imediato)", err)
		}
		if err := manager.WriteInput(id, []byte("x")); !errors.Is(err, ErrProcessNotFound) {
			t.Fatalf("WriteInput apos removido = %v, want ErrProcessNotFound", err)
		}
		if err := manager.ResizeInput(id, 100, 40); !errors.Is(err, ErrProcessNotFound) {
			t.Fatalf("ResizeInput apos removido = %v, want ErrProcessNotFound", err)
		}
		return
	}
	sm6WaitDone(t, manager, id)
	snap, ok := manager.Snapshot(id)
	if !ok || snap.Status != "exited" {
		t.Fatalf("snapshot done = %#v ok=%v, want exited", snap, ok)
	}
	// Continue em concluido retorna Exited e remove.
	cont, err := manager.Continue(context.Background(), ProcessContinue{ProcessID: id, Wait: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("Continue em concluido: %v", err)
	}
	if !cont.Exited {
		t.Fatalf("Continue em concluido Exited=false, want true")
	}
	if _, ok := manager.Snapshot(id); ok {
		t.Fatalf("processo deveria ter sido removido apos Continue com Exited")
	}
	// Reconstroi um segundo concluido retido para Write/Resize (pipe nao-TTY).
	cmd2 := exec.Command("/bin/sh", "-c", "sleep 0.05")
	r2, err := manager.Start(context.Background(), ProcessStart{
		Prepared: PreparedCommand{Command: cmd2},
		Request:  processManagerRequest(root, cmd2),
	}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Start2: %v", err)
	}
	if r2.Exited {
		t.Skip("Start2 colheu exit imediato; comportamento ja coberto acima")
	}
	sm6WaitDone(t, manager, r2.ProcessID)
	if err := manager.WriteInput(r2.ProcessID, []byte("x")); !errors.Is(err, ErrProcessStdinDisabled) {
		t.Fatalf("WriteInput em pipe concluido = %v, want ErrProcessStdinDisabled", err)
	}
	if err := manager.WriteInput(r2.ProcessID, nil); err != nil {
		t.Fatalf("WriteInput vazio em concluido = %v, want nil", err)
	}
	if err := manager.ResizeInput(r2.ProcessID, 100, 40); !errors.Is(err, ErrProcessStdinDisabled) {
		t.Fatalf("ResizeInput em pipe concluido = %v, want ErrProcessStdinDisabled", err)
	}
	if err := manager.ResizeInput(r2.ProcessID, 0, 0); err != nil {
		t.Fatalf("ResizeInput 0x0 = %v, want nil (no-op)", err)
	}
}

// Remove em cada fase.
func TestSm6RemovePorFase(t *testing.T) {
	root := sm6Root(t)
	manager := NewProcessManager(ProcessManagerOptions{CompletedRetention: time.Hour})

	running := sm6StartSleep(t, manager, root)
	manager.Remove(running.ProcessID)
	if _, ok := manager.Snapshot(running.ProcessID); ok {
		t.Fatalf("Snapshot apos Remove em running ainda acha")
	}
	if got := manager.Len(); got != 0 {
		t.Fatalf("Len apos Remove em running = %d, want 0", got)
	}
	manager.Stop(running.ProcessID) // nao deve panic nem sinalizar nada visivel

	entered := make(chan struct{})
	release := make(chan struct{})
	var once int32
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = manager.Start(context.Background(), ProcessStart{
			Prepared: PreparedCommand{Command: cmd},
			Request:  processManagerRequest(root, cmd),
			AfterWait: func() []byte {
				if atomic.CompareAndSwapInt32(&once, 0, 1) {
					close(entered)
				}
				<-release
				return nil
			},
		}, 50*time.Millisecond)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("AfterWait nao entrou")
	}
	id := manager.List()[0].ID
	manager.Remove(id)
	if _, ok := manager.Snapshot(id); ok {
		t.Fatalf("Snapshot apos Remove em reaped ainda acha")
	}
	if got := manager.Len(); got != 0 {
		t.Fatalf("Len apos Remove em reaped = %d, want 0", got)
	}
	close(release)
	<-finished // goroutine termina markDone fora do mapa sem panic

	cmd3 := exec.Command("/bin/sh", "-c", "sleep 0.05")
	r3, err := manager.Start(context.Background(), ProcessStart{
		Prepared: PreparedCommand{Command: cmd3},
		Request:  processManagerRequest(root, cmd3),
	}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Start3: %v", err)
	}
	if !r3.Exited {
		sm6WaitDone(t, manager, r3.ProcessID)
		manager.Remove(r3.ProcessID)
		if _, ok := manager.Snapshot(r3.ProcessID); ok {
			t.Fatalf("Snapshot apos Remove em done ainda acha")
		}
	}
}

// Capacidade cheia com concluidos retidos: Len conta retidos, prune deleta o mais antigo.
func TestSm6CapacidadeComRetidos(t *testing.T) {
	root := sm6Root(t)
	manager := NewProcessManager(ProcessManagerOptions{CompletedRetention: time.Hour, MaxProcesses: 2})

	mkDoneRetained := func() int {
		cmd := exec.Command("/bin/sh", "-c", "sleep 0.05")
		res, err := manager.Start(context.Background(), ProcessStart{
			Prepared: PreparedCommand{Command: cmd},
			Request:  processManagerRequest(root, cmd),
		}, 10*time.Millisecond)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if res.Exited {
			t.Fatalf("Start colheu exit imediato; teste precisa de retido (aumente o sleep ou reduza o wait)")
		}
		sm6WaitDone(t, manager, res.ProcessID)
		return res.ProcessID
	}

	id1 := mkDoneRetained()
	id2 := mkDoneRetained()
	if got := manager.Len(); got != 2 {
		t.Fatalf("Len com 2 retidos = %d, want 2", got)
	}
	if got := len(manager.List()); got != 0 {
		t.Fatalf("List com 2 retidos = %d, want 0 (filtra done)", got)
	}
	// Terceiro Start deve podar o concluido mais antigo e manter Len == max.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	r3, err := manager.Start(context.Background(), ProcessStart{
		Prepared: PreparedCommand{Command: cmd},
		Request:  processManagerRequest(root, cmd),
	}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Start3: %v", err)
	}
	if got := manager.Len(); got != 2 {
		t.Fatalf("Len apos prune com retidos = %d, want 2 (max)", got)
	}
	if _, ok := manager.Snapshot(id1); ok {
		t.Fatalf("id1 (%d) deveria ter sido podado (mais antigo)", id1)
	}
	if _, ok := manager.Snapshot(id2); !ok {
		// id2 e done retido: Snapshot ainda acha (mapa), mesmo filtrado do List.
		t.Fatalf("id2 (%d) deveria permanecer retido", id2)
	}
	manager.Stop(r3.ProcessID)
	manager.Remove(r3.ProcessID)
	manager.Remove(id2)
}
