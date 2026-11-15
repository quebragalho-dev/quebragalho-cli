package swarm

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/testutil"
)

// Caracterização do handoff como ele é hoje (dev intocada): o par
// (Status, handoff) comporta-se como um estado único embora a máquina seja
// implícita (6 ifs repetidos). Estes testes passam antes e depois da
// refatoração explícita sem nenhuma edição: nada visível muda — em
// particular "handing-off" nunca aparece fora do pacote (Get/List/
// Summarize/WaitSettled/status mostram pending/running).

func TestHandoffChar_BeginCommitFromRunning(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("src", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := c.SetStatus("src", StatusRunning); err != nil {
		t.Fatalf("SetStatus running: %v", err)
	}
	if _, err := c.BeginHandoff("src"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	// Por fora a tarefa continua parecendo running.
	if task, _ := c.Get("src"); task.Status != StatusRunning {
		t.Fatalf("durante handoff status = %v, want running", task.Status)
	}
	if err := c.ReserveHandoffSuccessor("src", "dst"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	successor, err := c.CommitHandoff("src", "dst", "a2", "team", "work+note")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if successor.Status != StatusPending || successor.AgentID != "a2" {
		t.Fatalf("sucessor = %+v, want pending/a2", successor)
	}
	if src, _ := c.Get("src"); src.Status != StatusHandedOff {
		t.Fatalf("origem = %v, want handed-off", src.Status)
	}
	s := c.Summarize()
	if s.HandedOff != 1 || s.Pending != 1 || s.Total != 2 {
		t.Fatalf("Summarize = %+v, want handedOff=1 pending=1 total=2", s)
	}
}

func TestHandoffChar_BeginCommitFromPending(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("src", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := c.BeginHandoff("src"); err != nil {
		t.Fatalf("BeginHandoff em pending: %v", err)
	}
	if task, _ := c.Get("src"); task.Status != StatusPending {
		t.Fatalf("durante handoff status = %v, want pending", task.Status)
	}
	if err := c.ReserveHandoffSuccessor("src", "dst"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := c.CommitHandoff("src", "dst", "a2", "team", "cont"); err != nil {
		t.Fatalf("Commit de pending: %v", err)
	}
	if src, _ := c.Get("src"); src.Status != StatusHandedOff {
		t.Fatalf("origem = %v, want handed-off", src.Status)
	}
}

func TestHandoffChar_AbortFromPendingRestoresPending(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("t", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := c.BeginHandoff("t"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	c.AbortHandoff("t")
	if task, _ := c.Get("t"); task.Status != StatusPending {
		t.Fatalf("após abort status = %v, want pending (origem)", task.Status)
	}
	if err := c.Complete("t", "ok"); err != nil {
		t.Fatalf("Complete após abort: %v", err)
	}
	if task, _ := c.Get("t"); task.Status != StatusDone {
		t.Fatalf("status = %v, want done", task.Status)
	}
}

func TestHandoffChar_AbortFromRunningRestoresRunning(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("t", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := c.SetStatus("t", StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := c.BeginHandoff("t"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	c.AbortHandoff("t")
	if task, _ := c.Get("t"); task.Status != StatusRunning {
		t.Fatalf("após abort status = %v, want running (origem)", task.Status)
	}
	if err := c.Complete("t", "ok"); err != nil {
		t.Fatalf("Complete após abort: %v", err)
	}
}

func TestHandoffChar_FailDuringHandoffRefused(t *testing.T) {
	for _, origin := range []TaskStatus{StatusPending, StatusRunning} {
		c := NewCoordinator()
		if _, err := c.Register("t", "a1", "team", "work"); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if origin == StatusRunning {
			if err := c.SetStatus("t", StatusRunning); err != nil {
				t.Fatalf("SetStatus: %v", err)
			}
		}
		if _, err := c.BeginHandoff("t"); err != nil {
			t.Fatalf("BeginHandoff: %v", err)
		}
		if err := c.Fail("t", "boom"); err == nil {
			t.Fatalf("origem %v: Fail durante handoff deve ser recusado", origin)
		}
		if task, _ := c.Get("t"); task.Status != origin {
			t.Fatalf("origem %v: status após Fail recusado = %v, want %v", origin, task.Status, origin)
		}
		// Após abort, falhar funciona.
		c.AbortHandoff("t")
		if err := c.Fail("t", "boom"); err != nil {
			t.Fatalf("origem %v: Fail após abort: %v", origin, err)
		}
	}
}

func TestHandoffChar_SetStatusDuringHandoffRefused(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("t", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := c.SetStatus("t", StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := c.BeginHandoff("t"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	for _, target := range []TaskStatus{StatusPending, StatusRunning, StatusDone, StatusFailed} {
		if err := c.SetStatus("t", target); err == nil {
			t.Fatalf("SetStatus(%v) durante handoff deve ser recusado", target)
		}
	}
	if task, _ := c.Get("t"); task.Status != StatusRunning {
		t.Fatalf("status = %v, want running (inalterado)", task.Status)
	}
}

func TestHandoffChar_ReassignDuringHandoffRefused(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("t", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := c.SetStatus("t", StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := c.BeginHandoff("t"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	if err := c.Reassign("t", "a2"); err == nil {
		t.Fatal("Reassign durante handoff deve ser recusado")
	}
	if task, _ := c.Get("t"); task.AgentID != "a1" || task.Status != StatusRunning {
		t.Fatalf("tarefa mudou durante handoff: %+v", task)
	}
	c.AbortHandoff("t")
	if err := c.Reassign("t", "a2"); err != nil {
		t.Fatalf("Reassign após abort: %v", err)
	}
	if task, _ := c.Get("t"); task.AgentID != "a2" || task.Status != StatusPending {
		t.Fatalf("após Reassign: %+v, want a2/pending", task)
	}
}

func TestHandoffChar_SecondBeginRefused(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("t", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := c.BeginHandoff("t"); err != nil {
		t.Fatalf("primeiro BeginHandoff: %v", err)
	}
	if _, err := c.BeginHandoff("t"); err == nil {
		t.Fatal("segundo BeginHandoff deve ser recusado")
	}
}

func TestHandoffChar_CommitRequiresReserveAndClaim(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("src", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Sem Begin: Commit recusado.
	if _, err := c.CommitHandoff("src", "dst", "a2", "team", "x"); err == nil {
		t.Fatal("Commit sem Begin deve ser recusado")
	}
	if _, err := c.BeginHandoff("src"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	// Com Begin mas sem Reserve: Commit recusado.
	if _, err := c.CommitHandoff("src", "dst", "a2", "team", "x"); err == nil {
		t.Fatal("Commit sem Reserve deve ser recusado")
	}
	// Reserve de outro id não autoriza.
	if err := c.ReserveHandoffSuccessor("src", "other"); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := c.CommitHandoff("src", "dst", "a2", "team", "x"); err == nil {
		t.Fatal("Commit com id não reservado deve ser recusado")
	}
}

func TestHandoffChar_BeginOnTerminalRefused(t *testing.T) {
	for _, fin := range []func(*Coordinator, string) error{
		func(c *Coordinator, id string) error { return c.Complete(id, "ok") },
		func(c *Coordinator, id string) error { return c.Fail(id, "boom") },
	} {
		c := NewCoordinator()
		if _, err := c.Register("t", "a1", "team", "work"); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := fin(c, "t"); err != nil {
			t.Fatalf("finish: %v", err)
		}
		if _, err := c.BeginHandoff("t"); err == nil {
			t.Fatal("BeginHandoff em tarefa terminal deve ser recusado")
		}
	}
}

func TestHandoffChar_AdoptOrphansSkipsHandoff(t *testing.T) {
	l := newLauncher(okFor)
	sw := newSwarmFor(t, l)
	coord := sw.Coordinator()
	if _, err := coord.Register("orphan", "ghost", "team", "stranded"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := coord.BeginHandoff("orphan"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	adopted, err := sw.AdoptOrphans(Policy{Model: "m"}, "team", "subagent")
	if err != nil {
		t.Fatalf("AdoptOrphans: %v", err)
	}
	for _, id := range adopted {
		if id == "orphan" {
			t.Fatal("AdoptOrphans não deve adotar tarefa em handoff")
		}
	}
	if task, _ := coord.Get("orphan"); task.Status != StatusPending {
		t.Fatalf("tarefa em handoff mudou: %+v", task)
	}
	// Sem o claim, a adoção acontece.
	coord.AbortHandoff("orphan")
	adopted, err = sw.AdoptOrphans(Policy{Model: "m"}, "team", "subagent")
	if err != nil {
		t.Fatalf("AdoptOrphans: %v", err)
	}
	if len(adopted) != 1 || adopted[0] != "orphan" {
		t.Fatalf("adopted = %v, want [orphan]", adopted)
	}
	testutil.WaitFor(t, "órfã adotada conclui", func() bool {
		task, ok := coord.Get("orphan")
		return ok && task.Status == StatusDone
	})
}

func TestHandoffChar_WaitSettledTreatsHandoffAsNonTerminal(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("t", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := c.BeginHandoff("t"); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	// Contexto já cancelado: retorna de imediato com o estado parcial,
	// que ainda contém a tarefa não terminal (handoff não assenta).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := c.WaitSettled(ctx, "team")
	if len(got) != 1 || got[0].Status != StatusPending {
		t.Fatalf("WaitSettled parcial = %+v, want 1x pending", got)
	}
	// Após abort+complete, assenta.
	c.AbortHandoff("t")
	if err := c.Complete("t", "ok"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got = c.WaitSettled(context.Background(), "team")
	if len(got) != 1 || got[0].Status != StatusDone {
		t.Fatalf("WaitSettled final = %+v, want 1x done", got)
	}
}

func TestHandoffChar_SummarizeHidesHandoffBucket(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("p", "a", "team", ""); err != nil {
		t.Fatalf("Register p: %v", err)
	}
	if _, err := c.Register("r", "b", "team", ""); err != nil {
		t.Fatalf("Register r: %v", err)
	}
	if err := c.SetStatus("r", StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := c.BeginHandoff("p"); err != nil {
		t.Fatalf("Begin p: %v", err)
	}
	if _, err := c.BeginHandoff("r"); err != nil {
		t.Fatalf("Begin r: %v", err)
	}
	s := c.Summarize()
	// Sem bucket handing-off: p conta como pending, r como running.
	if s.Pending != 1 || s.Running != 1 || s.Total != 2 {
		t.Fatalf("Summarize durante handoff = %+v, want pending=1 running=1", s)
	}
	if s.Done != 0 || s.Failed != 0 || s.HandedOff != 0 {
		t.Fatalf("Summarize durante handoff = %+v, want zeros terminais", s)
	}
}

func TestHandoffChar_StatusOutputHidesHandoff(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("p", "a", "team", "desc p"); err != nil {
		t.Fatalf("Register p: %v", err)
	}
	if _, err := c.Register("r", "b", "team", "desc r"); err != nil {
		t.Fatalf("Register r: %v", err)
	}
	if err := c.SetStatus("r", StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := c.BeginHandoff("p"); err != nil {
		t.Fatalf("Begin p: %v", err)
	}
	if _, err := c.BeginHandoff("r"); err != nil {
		t.Fatalf("Begin r: %v", err)
	}
	out := renderTasks(c, c.List(), "team")
	if strings.Contains(out, "handing-off") {
		t.Fatalf("saída expôs handing-off:\n%s", out)
	}
	if !strings.Contains(out, "[pending]") || !strings.Contains(out, "[running]") {
		t.Fatalf("saída deve mostrar [pending]/[running] durante handoff:\n%s", out)
	}
}
