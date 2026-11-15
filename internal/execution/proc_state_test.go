package execution

import (
	"context"
	"os/exec"
	"testing"
)

func TestProcNextTabelaEstadoEvento(t *testing.T) {
	states := []procState{procRunning, procReaped, procDone}
	events := []procEvent{procEvWaitReturned, procEvPublished}
	legal := map[procState]map[procEvent]procState{
		procRunning: {procEvWaitReturned: procReaped},
		procReaped:  {procEvPublished: procDone},
	}
	for _, st := range states {
		for _, ev := range events {
			next, ok := procNext(st, ev)
			wantNext, wantOK := legal[st][ev]
			if ok != wantOK {
				t.Fatalf("procNext(%v,%v) ok=%v, want %v", st, ev, ok, wantOK)
			}
			if ok && next != wantNext {
				t.Fatalf("procNext(%v,%v) = %v, want %v", st, ev, next, wantNext)
			}
			if !ok && next != st {
				t.Fatalf("procNext ilegal (%v,%v) mudou estado para %v", st, ev, next)
			}
		}
	}
}

func TestProcStateString(t *testing.T) {
	if procRunning.String() != "running" || procReaped.String() != "reaped" || procDone.String() != "done" {
		t.Fatalf("String = %q %q %q", procRunning, procReaped, procDone)
	}
	if procState(99).String() != "unknown" {
		t.Fatalf("estado desconhecido deveria ser unknown")
	}
	if procEvWaitReturned.String() != "waitReturned" || procEvPublished.String() != "published" {
		t.Fatalf("evento String errado")
	}
}

func newSm6Proc() *managedProcess {
	return &managedProcess{
		command: exec.Command("true"),
		output:  newProcessOutputBuffer(),
		reaped:  make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func TestProcTransitionEscreveGemeosJuntos(t *testing.T) {
	process := newSm6Proc()
	if !process.transition(procEvWaitReturned, nil) {
		t.Fatalf("running--wait-->reaped recusada")
	}
	if !process.reapedClosed() || process.doneClosed() {
		t.Fatalf("apos reap: reaped fechado e done aberto")
	}
	if process.snapshot().Status != "running" {
		t.Fatalf("snapshot em reaped = %q, want running", process.snapshot().Status)
	}
	payload := &procDonePayload{waitErr: nil, exitCode: 3}
	if !process.transition(procEvPublished, payload) {
		t.Fatalf("reaped--publish-->done recusada")
	}
	if !process.doneClosed() || !process.reapedClosed() {
		t.Fatalf("apos publish: done e reaped fechados")
	}
	snap := process.snapshot()
	if snap.Status != "exited" || snap.ExitCode == nil || *snap.ExitCode != 3 {
		t.Fatalf("snapshot done = %#v, want exited exit 3", snap)
	}
	res := process.collectResult(context.TODO(), 0, false)
	if !res.Exited || res.ExitCode != 3 {
		t.Fatalf("collectResult done = %#v, want exited 3", res)
	}
	// Terminal sem saida e sem panic.
	if process.transition(procEvWaitReturned, nil) || process.transition(procEvPublished, payload) {
		t.Fatalf("transicao saindo de done deveria ser ilegal")
	}
}

func TestProcTransitionIlegalNaoEscreveNada(t *testing.T) {
	process := newSm6Proc()
	payload := &procDonePayload{exitCode: 7}
	// running --publish--> ilegal: nada muda, canais abertos, sem exit.
	if process.transition(procEvPublished, payload) {
		t.Fatalf("running--publish deveria ser ilegal")
	}
	if process.doneClosed() || process.reapedClosed() {
		t.Fatalf("transicao ilegal fechou canal")
	}
	if got := process.collectResult(context.TODO(), 0, false); got.Exited {
		t.Fatalf("transicao ilegal publicou exit")
	}
	// Payload nil nunca publica nem da panic.
	if process.transition(procEvWaitReturned, nil) != true {
		t.Fatalf("reap legal falhou")
	}
	if process.transition(procEvPublished, nil) {
		t.Fatalf("publish com payload nil deveria recusar")
	}
	if process.doneClosed() {
		t.Fatalf("payload nil nao deveria fechar done")
	}
	// Reap duplo ilegal e sem panic (segundo retorna false, sem fechar de novo).
	dup := newSm6Proc()
	if !dup.transition(procEvWaitReturned, nil) {
		t.Fatalf("primeiro reap falhou")
	}
	if dup.transition(procEvWaitReturned, nil) {
		t.Fatalf("reap duplo deveria ser ilegal")
	}
}
