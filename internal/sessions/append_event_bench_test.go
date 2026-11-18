package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var benchPreexistingCounts = []int{0, 1000, 5000}

func BenchmarkAppendEventSessionLog(b *testing.B) {
	for _, initialEvents := range benchPreexistingCounts {
		b.Run(fmt.Sprintf("events_%d", initialEvents), func(b *testing.B) {
			runAppendEventBenchmark(b, initialEvents, func(store *Store, sessionID string, input AppendEventInput) error {
				_, err := store.AppendEvent(sessionID, input)
				return err
			})
		})
	}
}

func BenchmarkAppendEventSessionLogNoFsync(b *testing.B) {
	noopSync := func(*os.File) error { return nil }
	for _, initialEvents := range benchPreexistingCounts {
		b.Run(fmt.Sprintf("events_%d", initialEvents), func(b *testing.B) {
			runAppendEventBenchmark(b, initialEvents, func(store *Store, sessionID string, input AppendEventInput) error {
				_, err := appendEventWithSyncForBench(store, sessionID, input, noopSync)
				return err
			})
		})
	}
}

func runAppendEventBenchmark(
	b *testing.B,
	initialEvents int,
	appendFn func(store *Store, sessionID string, input AppendEventInput) error,
) {
	b.Helper()

	root := realDiskBenchDir(b)
	home := filepath.Join(root, "home")
	b.Setenv("HOME", home)
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	b.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	b.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	storeDir := filepath.Join(root, "sessions")
	store := NewStore(StoreOptions{RootDir: storeDir})

	session, err := store.Create(CreateInput{
		SessionID: fmt.Sprintf("qg_bench_%d", initialEvents),
		Title:     "Investigar latência de fsync na gravação da sessão",
		Cwd:       "/home/user/workspace/project",
		ModelID:   "claude-sonnet-4-6",
		Provider:  "anthropic",
	})
	if err != nil {
		b.Fatalf("Create session: %v", err)
	}

	cycle := realisticTurnEventsForBench()
	if initialEvents > 0 {
		seed := make([]AppendEventInput, initialEvents)
		for i := range seed {
			seed[i] = cycle[i%len(cycle)]
		}
		if _, err := store.AppendEvents(session.SessionID, seed); err != nil {
			b.Fatalf("seed %d events: %v", initialEvents, err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := appendFn(store, session.SessionID, cycle[i%len(cycle)]); err != nil {
			b.Fatalf("append event %d: %v", i, err)
		}
	}
}

func realisticTurnEventsForBench() []AppendEventInput {
	toolOutput := realisticToolOutput2KB()
	return []AppendEventInput{
		{
			Type: EventMessage,
			Payload: map[string]any{
				"role":    "user",
				"content": "Analise internal/sessions/store.go para confirmar quantos fsyncs ocorrem por evento gravado e verifique se uma sessão longa sofre degradação perceptível.",
			},
		},
		{
			Type: EventToolCall,
			Payload: map[string]any{
				"id":        "call_read_store_01",
				"name":      "read_file",
				"arguments": `{"path":"internal/sessions/store.go","start_line":650,"end_line":725}`,
			},
		},
		{
			Type: EventToolResult,
			Payload: map[string]any{
				"toolCallId": "call_read_store_01",
				"name":       "read_file",
				"status":     "ok",
				"output":     toolOutput,
				"meta": map[string]string{
					"path":  "internal/sessions/store.go",
					"lines": "75",
				},
			},
		},
		{
			Type: EventMessage,
			Payload: map[string]any{
				"role":    "assistant",
				"content": "Confirmei no código que cada AppendEvent realiza três chamadas Sync: uma em events.jsonl logo após o append, uma no arquivo temporário de metadata.json antes do rename atômico e uma no diretório da sessão em syncDir.",
			},
		},
	}
}

func realisticToolOutput2KB() string {
	const targetBytes = 2048
	var builder strings.Builder
	builder.Grow(targetBytes + 128)
	for line := 650; builder.Len() < targetBytes; line++ {
		fmt.Fprintf(&builder, "%4d: \t// trecho realista de leitura de arquivo retornado por read_file (%02d)\n", line, line%100)
	}
	return builder.String()[:targetBytes]
}

// appendEventWithSyncForBench mirrors Store.AppendEvent step for step while
// routing the single (*os.File).Sync of the normal path through syncFn so the
// benchmark can isolate fsync cost without modifying production code.
// (metadata.json is no longer rewritten on append: its derived counters are
// reconciled from the log on read.)
func appendEventWithSyncForBench(
	store *Store,
	sessionID string,
	input AppendEventInput,
	syncFn func(*os.File) error,
) (Event, error) {
	if !ValidSessionID(sessionID) {
		return Event{}, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	prepared, err := prepareAppendEventInputs([]AppendEventInput{input})
	if err != nil {
		return Event{}, err
	}
	unlock, err := store.lockSession(sessionID)
	if err != nil {
		return Event{}, err
	}
	defer unlock()

	session, err := store.readMetadata(sessionID)
	if err != nil {
		return Event{}, err
	}
	sequence := session.EventCount + 1
	if logSeq, err := store.lastEventSequence(sessionID); err == nil && logSeq >= sequence {
		sequence = logSeq + 1
	}

	var batch bytes.Buffer
	timestamp := store.timestamp()
	event := Event{
		ID:        fmt.Sprintf("%s:%d", sessionID, sequence),
		SessionID: sessionID,
		Sequence:  sequence,
		Type:      prepared[0].Type,
		CreatedAt: timestamp,
		Payload:   prepared[0].Payload,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("encode qg session event: %w", err)
	}
	batch.Write(data)
	batch.WriteByte('\n')

	file, err := os.OpenFile(store.eventsPath(sessionID), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return Event{}, fmt.Errorf("append qg session event: %w", err)
	}
	if _, err := file.Write(batch.Bytes()); err != nil {
		_ = file.Close()
		return Event{}, fmt.Errorf("append qg session event: %w", err)
	}
	if err := syncFn(file); err != nil {
		_ = file.Close()
		return Event{}, fmt.Errorf("sync qg session event: %w", err)
	}
	if err := file.Close(); err != nil {
		return Event{}, fmt.Errorf("close qg session event file: %w", err)
	}
	return event, nil
}

// realDiskBenchDir returns a temporary directory backed by a real disk
// filesystem rather than tmpfs/ramfs so fsync exercises storage durability.
func realDiskBenchDir(b *testing.B) string {
	b.Helper()

	if override := strings.TrimSpace(os.Getenv("QG_BENCH_SESSION_DIR")); override != "" {
		dir, err := os.MkdirTemp(override, "qg-session-bench-*")
		if err != nil {
			b.Fatalf("create temp dir in QG_BENCH_SESSION_DIR=%q: %v", override, err)
		}
		b.Cleanup(func() { _ = os.RemoveAll(dir) })
		return verifyNonTmpfsBenchDir(b, dir, true)
	}

	dir := b.TempDir()
	if checked := verifyNonTmpfsBenchDir(b, dir, false); checked != "" {
		return checked
	}

	candidates := []string{"/var/tmp"}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		candidates = append(candidates, wd)
	}
	for _, base := range candidates {
		alt, err := os.MkdirTemp(base, "qg-session-bench-*")
		if err != nil {
			continue
		}
		if checked := verifyNonTmpfsBenchDir(b, alt, false); checked != "" {
			b.Cleanup(func() { _ = os.RemoveAll(alt) })
			return checked
		}
		_ = os.RemoveAll(alt)
	}
	b.Fatalf("could not find a non-tmpfs directory for session fsync benchmark (b.TempDir()=%s)", dir)
	return ""
}

func verifyNonTmpfsBenchDir(b *testing.B, dir string, fatalOnTmpfs bool) string {
	b.Helper()
	resolved := dir
	if eval, err := filepath.EvalSymlinks(dir); err == nil {
		resolved = eval
	}
	if runtime.GOOS != "linux" {
		return resolved
	}
	mountPoint, fsType, ok := linuxMountFSType(resolved)
	if !ok {
		return resolved
	}
	if fsType == "tmpfs" || fsType == "ramfs" {
		if fatalOnTmpfs {
			b.Fatalf("benchmark directory %s (mount %s) is on %s; real filesystem required", resolved, mountPoint, fsType)
		}
		return ""
	}
	return resolved
}

func linuxMountFSType(target string) (mountPoint string, fsType string, ok bool) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "", "", false
	}
	cleanTarget := filepath.Clean(target)
	bestLen := -1
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mp := unescapeMountPath(fields[1])
		ft := fields[2]
		if mp == "rootfs" || ft == "rootfs" || ft == "autofs" {
			continue
		}
		if cleanTarget == mp || mp == "/" || strings.HasPrefix(cleanTarget, mp+"/") {
			if len(mp) >= bestLen {
				bestLen = len(mp)
				mountPoint = mp
				fsType = ft
				ok = true
			}
		}
	}
	return mountPoint, fsType, ok
}

func unescapeMountPath(raw string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return filepath.Clean(replacer.Replace(raw))
}
