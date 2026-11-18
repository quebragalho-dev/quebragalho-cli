package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Real crash proof: a child process appends events in a loop and is killed with
// SIGKILL at random moments. Afterwards the session is opened by a fresh Store
// and verified: no acknowledged event is lost, no file ends up corrupted or
// unreadable, and resuming works. Must also pass under -race.

// TestSessionCrashChild is the child process: it appends events non-stop and
// reports each acknowledgement (AppendEvent return) as one "ACK <seq>" line on
// stdout, using a single write per line so pipe reads stay atomic.
func TestSessionCrashChild(t *testing.T) {
	if os.Getenv("QG_SESSION_CRASH_CHILD") != "1" {
		t.Skip("processo auxiliar do teste de queda")
	}
	root := os.Getenv("QG_SESSION_CRASH_ROOT")
	sessionID := os.Getenv("QG_SESSION_CRASH_SID")
	if root == "" || sessionID == "" {
		fmt.Fprintln(os.Stderr, "QG_SESSION_CRASH_ROOT/SID ausentes")
		os.Exit(2)
	}
	store := NewStore(StoreOptions{RootDir: root})
	payloads := []string{"a", "conteúdo médio do turno com algum texto", strings.Repeat("z", 2048)}
	for i := 0; ; i++ {
		event, err := store.AppendEvent(sessionID, AppendEventInput{
			Type:    EventMessage,
			Payload: map[string]any{"content": payloads[i%len(payloads)], "turno": i},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "append:", err)
			os.Exit(1)
		}
		if _, err := os.Stdout.WriteString("ACK " + strconv.Itoa(event.Sequence) + "\n"); err != nil {
			os.Exit(1)
		}
	}
}

// TestSessionSurvivesSigkillDuringAppends kills the child with SIGKILL at
// random moments (at least 50 rounds) and verifies recovery on every round.
func TestSessionSurvivesSigkillDuringAppends(t *testing.T) {
	const rounds = 50
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for round := 0; round < rounds; round++ {
		t.Run(fmt.Sprintf("queda_%02d", round), func(t *testing.T) {
			surviveOneSigkillRound(t, rng)
		})
	}
}

// ackLog collects child-process ACKs across goroutines.
type ackLog struct {
	mu   sync.Mutex
	seqs []int
}

func (l *ackLog) add(seq int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seqs = append(l.seqs, seq)
}

func (l *ackLog) snapshot() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int{}, l.seqs...)
}

func surviveOneSigkillRound(t *testing.T, rng *rand.Rand) {
	t.Helper()
	root := t.TempDir()
	const sessionID = "qg_crash"
	parent := NewStore(StoreOptions{RootDir: root})
	session, err := parent.Create(CreateInput{SessionID: sessionID, Title: "queda", Cwd: "/repo", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}

	childBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(childBinary, "-test.run=TestSessionCrashChild")
	child.Env = append(os.Environ(),
		"QG_SESSION_CRASH_CHILD=1",
		"QG_SESSION_CRASH_ROOT="+root,
		"QG_SESSION_CRASH_SID="+session.SessionID,
	)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = nil
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	acks := &ackLog{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "ACK ") {
				continue
			}
			seq, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "ACK ")))
			if err != nil || seq <= 0 {
				continue
			}
			acks.add(seq)
		}
	}()

	// Wait for a handful of acknowledgements, then kill at a random instant:
	// sometimes mid-append (torn-write window), sometimes between events.
	deadline := time.Now().Add(10 * time.Second)
	for len(acks.snapshot()) < 5+rng.Intn(20) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(time.Duration(rng.Intn(30)) * time.Millisecond)
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL no filho: %v", err)
	}
	_ = child.Wait()
	<-done

	acked := acks.snapshot()
	if len(acked) == 0 {
		t.Fatal("filho morreu antes de confirmar qualquer evento")
	}

	// Open the session with a fresh Store, like a new process after the crash.
	reopened := NewStore(StoreOptions{RootDir: root})
	loaded, err := reopened.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Get após SIGKILL: %v", err)
	}
	if loaded == nil {
		t.Fatal("sessão sumiu após SIGKILL")
	}
	events, present, err := reopened.ReadEventsWithPresence(session.SessionID)
	if err != nil {
		t.Fatalf("ReadEvents após SIGKILL: %v", err)
	}
	if !present {
		t.Fatal("events.jsonl sumiu após SIGKILL")
	}
	bySeq := map[int]Event{}
	for _, event := range events {
		if _, dup := bySeq[event.Sequence]; dup {
			t.Fatalf("sequência duplicada %d após SIGKILL", event.Sequence)
		}
		bySeq[event.Sequence] = event
	}
	for _, seq := range acked {
		event, ok := bySeq[seq]
		if !ok {
			t.Fatalf("evento confirmado %d perdido após SIGKILL", seq)
		}
		if event.SessionID != session.SessionID || event.Sequence != seq || event.Type == "" || event.CreatedAt == "" {
			t.Fatalf("evento confirmado %d ilegível após SIGKILL: %+v", seq, event)
		}
	}
	maxAcked := acked[len(acked)-1]

	// metadata.json must stay valid.
	rawMeta, err := os.ReadFile(filepath.Join(root, session.SessionID, MetadataFile))
	if err != nil {
		t.Fatalf("metadata.json ilegível após SIGKILL: %v", err)
	}
	var metaMap map[string]any
	if err := json.Unmarshal(rawMeta, &metaMap); err != nil {
		t.Fatalf("metadata.json corrompido após SIGKILL: %v", err)
	}
	// The derived counter must reflect the log (tolerating at most the
	// unacknowledged suffix SIGKILL may have interrupted mid-write).
	maxSeq := 0
	for seq := range bySeq {
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	if loaded.EventCount != maxSeq {
		t.Fatalf("EventCount derivado=%d, último do log=%d", loaded.EventCount, maxSeq)
	}
	if maxSeq < maxAcked {
		t.Fatalf("log terminou em %d, mas o evento %d havia sido confirmado", maxSeq, maxAcked)
	}

	// No atomic-write leftovers.
	entries, err := os.ReadDir(filepath.Join(root, session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("resto de tmp após SIGKILL: %s", entry.Name())
		}
	}

	// Resuming must work: the next event advances without reusing an ID.
	resumed, err := reopened.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"content": "depois da queda"}})
	if err != nil {
		t.Fatalf("retomada após SIGKILL: %v", err)
	}
	if resumed.Sequence != maxSeq+1 {
		t.Fatalf("retomada reutilizou sequência: obteve %d, esperava %d", resumed.Sequence, maxSeq+1)
	}
}
