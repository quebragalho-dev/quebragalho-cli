package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Acceptance tests for "QG reconhece o ambiente/orquestrador sozinho". They
// drive the real `qg exec` / `qg doctor` entry points against a loopback stub
// provider and assert on the exact bytes QG would send to a model. Skill
// fixtures and environment signals use invented names so no real orchestrator
// is ever named here.

var orqSessionSuffixes = []string{"SOCKET", "SESSION", "SESSION_ID", "TERMINAL_ID", "WORKSPACE", "WORKSPACE_ID", "CLI"}

const (
	orqSkillsOpen  = "<available_skills>"
	orqSkillsClose = "</available_skills>"
)

type orqSkill struct {
	name        string
	description string
	environment []string
}

type orqMessage struct {
	role    string
	content string
}

type orqRequest struct {
	raw      string
	system   string
	tools    int
	messages []orqMessage
}

type orqReply struct {
	status int
	body   string
}

type orqHarness struct {
	t       *testing.T
	workdir string

	mu       sync.Mutex
	requests []orqRequest
	reply    func(req orqRequest, mainIndex int) orqReply
	mainSeen int
}

func newOrqHarness(t *testing.T, skills []orqSkill) *orqHarness {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	configHome := filepath.Join(base, "xdg-config")
	dataHome := filepath.Join(base, "xdg-data")
	skillsDir := filepath.Join(base, "skills")
	workdir := filepath.Join(base, "work")
	for _, dir := range []string{home, configHome, dataHome, skillsDir, workdir, filepath.Join(configHome, "qg")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	clearProviderEnv(t)
	clearOrchestratorSignals(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", configHome)
	t.Setenv("LOCALAPPDATA", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "xdg-cache"))
	t.Setenv("QG_CRED_STORAGE", "file")
	t.Setenv("QG_NO_UPDATE_CHECK", "1")
	t.Setenv("QG_SKILLS_DIR", skillsDir)
	writeOrqSkills(t, skillsDir, skills)

	h := &orqHarness{t: t, workdir: workdir}
	h.reply = func(orqRequest, int) orqReply { return orqReply{status: http.StatusOK, body: orqSSEText("ok")} }
	server := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(server.Close)

	config := map[string]any{
		"activeProvider": "orq",
		"providers": []any{map[string]any{
			"name":          "orq",
			"provider_kind": "openai-compatible",
			"base_url":      server.URL,
			"api_key":       "sk-orq-fixture",
			"model":         "orq-model",
		}},
		"mcpServers": map[string]any{"exa": map[string]any{"disabled": true}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "qg", "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

// clearOrchestratorSignals removes every inherited variable that could act as
// a detection signal, so the developer's own terminal (which may itself be an
// orchestrator) never leaks into these tests.
func clearOrchestratorSignals(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "" {
			continue
		}
		signal := key == "TERM_PROGRAM"
		for _, suffix := range orqSessionSuffixes {
			if strings.HasSuffix(key, "_"+suffix) {
				signal = true
			}
		}
		if !signal {
			continue
		}
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func writeOrqSkills(t *testing.T, dir string, skills []orqSkill) {
	t.Helper()
	for _, skill := range skills {
		var b strings.Builder
		b.WriteString("---\n")
		b.WriteString("name: " + skill.name + "\n")
		b.WriteString("description: " + skill.description + "\n")
		if skill.environment != nil {
			b.WriteString("environment: [" + strings.Join(skill.environment, ", ") + "]\n")
		}
		b.WriteString("---\n\n# " + skill.name + "\n\nFixture body for " + skill.name + ".\n")
		skillDir := filepath.Join(dir, skill.name)
		if err := os.MkdirAll(skillDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *orqHarness) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req := decodeOrqRequest(raw)
	h.mu.Lock()
	h.requests = append(h.requests, req)
	mainIndex := -1
	if req.tools > 0 {
		h.mainSeen++
		mainIndex = h.mainSeen
	}
	reply := h.reply
	h.mu.Unlock()

	out := reply(req, mainIndex)
	if out.status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(out.status)
		_, _ = io.WriteString(w, out.body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, out.body)
}

func decodeOrqRequest(raw []byte) orqRequest {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(raw, &body)
	req := orqRequest{raw: string(raw), tools: len(body.Tools)}
	for _, message := range body.Messages {
		content := orqContentText(message.Content)
		req.messages = append(req.messages, orqMessage{role: message.Role, content: content})
		if message.Role == "system" && req.system == "" {
			req.system = content
		}
	}
	return req
}

func orqContentText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return string(raw)
}

func (h *orqHarness) snapshot() []orqRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.requests)
}

// exec runs `qg exec` once and returns the system prompt of the first request it
// sent to the provider.
func (h *orqHarness) exec(extraArgs ...string) string {
	h.t.Helper()
	before := len(h.snapshot())
	args := append([]string{"exec", "--cwd", h.workdir}, extraArgs...)
	args = append(args, "Responda apenas PRONTO.")
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		h.t.Fatalf("qg exec exit=%d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	requests := h.snapshot()
	if len(requests) <= before {
		h.t.Fatalf("qg exec sent no provider request; stderr=%s", stderr.String())
	}
	return requests[before].system
}

func (h *orqHarness) assertNoValueLeak(values ...string) {
	h.t.Helper()
	for index, req := range h.snapshot() {
		for _, value := range values {
			if strings.Contains(req.raw, value) {
				h.t.Errorf("request %d carries the value %q of an environment variable; only variable names may reach the model", index+1, value)
			}
		}
	}
}

func orqSkillList(system string) string {
	start := strings.Index(system, orqSkillsOpen)
	end := strings.Index(system, orqSkillsClose)
	if start < 0 || end < start {
		return ""
	}
	return system[start+len(orqSkillsOpen) : end]
}

func orqOutsideSkillList(system string) string {
	start := strings.Index(system, orqSkillsOpen)
	end := strings.Index(system, orqSkillsClose)
	if start < 0 || end < start {
		return system
	}
	return system[:start] + system[end+len(orqSkillsClose):]
}

// assertOrqDetected checks the system prompt carries an environment block that
// names the skill and the signal that matched it. The block lives outside the
// skills list, so a mention inside <available_skills> does not count.
func assertOrqDetected(t *testing.T, system, skill, signal string) {
	t.Helper()
	outside := orqOutsideSkillList(system)
	if !strings.Contains(outside, signal) {
		t.Errorf("system prompt has no environment block naming signal %q for skill %q; outside the skills list it reads:\n%s", signal, skill, orqTail(outside))
	}
	if !strings.Contains(outside, skill) {
		t.Errorf("system prompt has no environment block naming skill %q (signal %q)", skill, signal)
	}
}

func orqTail(text string) string {
	const max = 1500
	if len(text) <= max {
		return text
	}
	return "…" + text[len(text)-max:]
}

func orqFillers(count int) []orqSkill {
	fillers := make([]orqSkill, 0, count)
	for index := 1; index <= count; index++ {
		id := fmt.Sprintf("%03d", index)
		fillers = append(fillers, orqSkill{
			name:        "filler-skill-" + id,
			description: "Filler " + id + " synthetic description used to fill the skills budget with distinct text for acceptance fixture number " + id + ".",
		})
	}
	return fillers
}

func orqSSE(chunks ...map[string]any) string {
	var b strings.Builder
	for _, chunk := range chunks {
		data, _ := json.Marshal(chunk)
		b.WriteString("data: " + string(data) + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func orqChunk(delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id":      "orq",
		"object":  "chat.completion.chunk",
		"model":   "orq-model",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
}

func orqSSEText(text string) string {
	return orqSSE(
		orqChunk(map[string]any{"role": "assistant", "content": text}, nil),
		orqChunk(map[string]any{}, "stop"),
	)
}

func orqSSEToolCall(id, name, arguments string) string {
	return orqSSE(
		orqChunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": id, "type": "function",
			"function": map[string]any{"name": name, "arguments": arguments},
		}}}, nil),
		orqChunk(map[string]any{}, "tool_calls"),
	)
}

func TestOrchestratorDetectedBySessionSuffixHeuristic(t *testing.T) {
	for _, suffix := range orqSessionSuffixes {
		t.Run(suffix, func(t *testing.T) {
			h := newOrqHarness(t, append(orqFillers(3), orqSkill{name: "quokka-orq", description: "Coordinates quokka terminals."}))
			signal := "QUOKKA_ORQ_" + suffix
			value := "/tmp/orq-secret-" + strings.ToLower(suffix) + "-9f2c"
			t.Setenv(signal, value)

			system := h.exec()

			assertOrqDetected(t, system, "quokka-orq", signal)
			h.assertNoValueLeak(value)
		})
	}
}

func TestOrchestratorDetectedByDeclaredEnvironment(t *testing.T) {
	h := newOrqHarness(t, append(orqFillers(3), orqSkill{
		name:        "atlas-desk",
		description: "Coordinates atlas desks.",
		environment: []string{"ATLAS_ABSENT_SIGNAL", "BAR_X"},
	}))
	t.Setenv("BAR_X", "orq-secret-declared-77ab")

	system := h.exec()

	assertOrqDetected(t, system, "atlas-desk", "BAR_X")
	h.assertNoValueLeak("orq-secret-declared-77ab")
}

func TestOrchestratorDetectedByTermProgram(t *testing.T) {
	h := newOrqHarness(t, append(orqFillers(3), orqSkill{name: "vortexterm", description: "Drives vortex panes."}))
	t.Setenv("TERM_PROGRAM", "VortexTerm")

	system := h.exec()

	assertOrqDetected(t, system, "vortexterm", "TERM_PROGRAM")
}

// Each case pairs a near-miss that must leave the prompt byte-identical to a
// run without the variable with the signal that does match the same skill, so
// the case fails both when nothing is detected and when the guard is loose.
func TestOrchestratorNearMissesDetectNothing(t *testing.T) {
	cases := []struct {
		name      string
		skills    []orqSkill
		nearMiss  map[string]string
		hitSkill  string
		hitSignal string
	}{
		{
			name:      "prefixo git sem sufixo de sessao",
			skills:    []orqSkill{{name: "git", description: "Git helpers."}},
			nearMiss:  map[string]string{"GIT_EDITOR": "vim", "GIT_PAGER": "cat"},
			hitSkill:  "git",
			hitSignal: "GIT_SOCKET",
		},
		{
			name:      "prefixo go sem separador nem sufixo",
			skills:    []orqSkill{{name: "go", description: "Go helpers."}},
			nearMiss:  map[string]string{"GOPATH": "/tmp/orq-gopath", "GOFLAGS": "-count=1"},
			hitSkill:  "go",
			hitSignal: "GO_SESSION",
		},
		{
			name:      "variavel de sessao sem skill instalada",
			skills:    []orqSkill{{name: "heron", description: "Heron helpers."}},
			nearMiss:  map[string]string{"FOO_SOCKET": "/tmp/orq-foo.sock", "FOO_SESSION_ID": "orq-foo-session"},
			hitSkill:  "heron",
			hitSignal: "HERON_TERMINAL_ID",
		},
		{
			name:      "skill declarada sem a variavel declarada ignora a heuristica",
			skills:    []orqSkill{{name: "harbor", description: "Harbor helpers.", environment: []string{"HARBOR_DECLARED"}}},
			nearMiss:  map[string]string{"HARBOR_SOCKET": "/tmp/orq-harbor.sock"},
			hitSkill:  "harbor",
			hitSignal: "HARBOR_DECLARED",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOrqHarness(t, append(orqFillers(3), tc.skills...))
			baseline := h.exec()

			for key, value := range tc.nearMiss {
				t.Setenv(key, value)
			}
			if got := h.exec(); got != baseline {
				t.Errorf("near-miss env %v changed the system prompt; want byte-identical to the run without it.\nwithout:\n%s\nwith:\n%s", tc.nearMiss, orqTail(baseline), orqTail(got))
			}

			t.Setenv(tc.hitSignal, "orq-secret-hit-5d1e")
			assertOrqDetected(t, h.exec(), tc.hitSkill, tc.hitSignal)
			h.assertNoValueLeak("orq-secret-hit-5d1e")
		})
	}
}

// A declaration naming a variable every shell has would match everywhere and
// let any installed skill pin itself. Those names are ignored; a skill that
// also declares its own variable still matches through it.
func TestOrchestratorDeclarationIgnoresUbiquitousVariables(t *testing.T) {
	ubiquitous := []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "TERM_PROGRAM", "COLORTERM",
		"PWD", "OLDPWD", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TZ", "EDITOR", "VISUAL",
		"PAGER", "DISPLAY", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR",
	}
	skills := orqFillers(3)
	for _, variable := range ubiquitous {
		skills = append(skills, orqSkill{
			name:        "ubiq-" + strings.ToLower(strings.ReplaceAll(variable, "_", "-")),
			description: "Declares only " + variable + ".",
			environment: []string{variable},
		})
	}
	skills = append(skills, orqSkill{name: "zebra-hub", description: "Coordinates zebra hubs.", environment: []string{"HOME", "ZEBRA_SOCKET"}})
	h := newOrqHarness(t, skills)
	harmless := map[string]string{
		"USER": "orq", "LOGNAME": "orq", "SHELL": "/bin/sh", "TERM": "xterm-256color", "TERM_PROGRAM": "OrqTerminal",
		"COLORTERM": "truecolor", "PWD": h.workdir, "OLDPWD": h.workdir, "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8",
		"LC_CTYPE": "C.UTF-8", "TMPDIR": os.TempDir(), "TZ": "UTC", "EDITOR": "vi", "VISUAL": "vi", "PAGER": "cat",
		"DISPLAY": ":0", "XDG_RUNTIME_DIR": os.TempDir(),
	}
	for _, variable := range ubiquitous {
		if _, ok := os.LookupEnv(variable); !ok {
			t.Setenv(variable, harmless[variable])
		}
	}
	t.Setenv("ZEBRA_SOCKET", "/tmp/orq-secret-zebra-8e")

	system := h.exec()

	assertOrqDetected(t, system, "zebra-hub", "ZEBRA_SOCKET")
	outside := orqOutsideSkillList(system)
	list := orqSkillList(system)
	firstFiller := strings.Index(list, "filler-skill-")
	for _, skill := range skills {
		if !strings.HasPrefix(skill.name, "ubiq-") {
			continue
		}
		if strings.Contains(outside, skill.name) {
			t.Errorf("skill %s matched through ubiquitous %v; such declarations must be ignored", skill.name, skill.environment)
		}
		if at := strings.Index(list, skill.name); at >= 0 && firstFiller >= 0 && at < firstFiller {
			t.Errorf("skill %s is pinned through ubiquitous %v; such declarations must be ignored", skill.name, skill.environment)
		}
	}
	h.assertNoValueLeak("/tmp/orq-secret-zebra-8e")
}

// KITE_RELAY_SOCKET is "KITE_RELAY_" + SOCKET, so it belongs to kite-relay. For
// kite it would be "KITE_" + "RELAY_SOCKET", which is not a session suffix.
func TestOrchestratorSignalMatchesWholeSkillName(t *testing.T) {
	h := newOrqHarness(t, append(orqFillers(3),
		orqSkill{name: "kite", description: "Kite helpers."},
		orqSkill{name: "kite-relay", description: "Kite relay helpers."},
	))
	t.Setenv("KITE_RELAY_SOCKET", "/tmp/orq-kite-relay.sock")

	system := h.exec()

	assertOrqDetected(t, system, "kite-relay", "KITE_RELAY_SOCKET")
	outside := orqOutsideSkillList(system)
	if strings.Count(outside, "kite") != strings.Count(outside, "kite-relay") {
		t.Errorf("skill kite was reported as detected by KITE_RELAY_SOCKET; outside the skills list:\n%s", orqTail(outside))
	}
}

func TestOrchestratorSkillPinnedAndEveryNameListed(t *testing.T) {
	fillers := orqFillers(253)
	pinnedDescription := strings.Repeat("Quokka orchestration guidance that must reach the model whole. ", 6) + "END-OF-QUOKKA-DESCRIPTION"
	h := newOrqHarness(t, append(fillers, orqSkill{name: "quokka-orq", description: pinnedDescription}))
	t.Setenv("QUOKKA_ORQ_SOCKET", "/tmp/orq-secret-pin-31c0")

	system := h.exec()
	list := orqSkillList(system)
	if list == "" {
		t.Fatalf("system prompt has no %s block:\n%s", orqSkillsOpen, orqTail(system))
	}

	var missing []string
	for _, skill := range fillers {
		if !strings.Contains(list, skill.name) {
			missing = append(missing, skill.name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d of 254 installed skill names are missing from the skills list (first: %s); every name must be listed", len(missing), missing[0])
	}

	pinnedAt := strings.Index(list, "quokka-orq")
	if pinnedAt < 0 {
		t.Fatalf("detected skill quokka-orq is not in the skills list")
	}
	for _, skill := range fillers {
		if at := strings.Index(list, skill.name); at >= 0 && at < pinnedAt {
			t.Errorf("detected skill quokka-orq is listed after %s; it must be pinned at the top", skill.name)
			break
		}
	}
	if !strings.Contains(list, pinnedDescription) {
		t.Errorf("detected skill quokka-orq is listed without its full %d-byte description", len(pinnedDescription))
	}

	descriptionBytes := 0
	for _, skill := range fillers {
		if strings.Contains(list, skill.description) {
			descriptionBytes += len(skill.description)
		}
	}
	if descriptionBytes > 4096 {
		t.Errorf("unpinned skill descriptions use %d bytes; they must stop at the 4 KiB budget", descriptionBytes)
	}
	assertOrqDetected(t, system, "quokka-orq", "QUOKKA_ORQ_SOCKET")
	h.assertNoValueLeak("/tmp/orq-secret-pin-31c0")
}

// Several detected skills plus 249 more must assemble to the same bytes every
// time; the request prefix is cached. Go randomizes map iteration only among a
// few rotations for small maps, so one comparison is not enough to catch an
// unordered pin; twelve assemblies make a lucky pass negligible.
func TestOrchestratorPromptStableAcrossAssemblies(t *testing.T) {
	detected := []struct {
		skill  orqSkill
		signal string
		value  string
	}{
		{orqSkill{name: "quokka-orq", description: "Coordinates quokka terminals."}, "QUOKKA_ORQ_SESSION_ID", "orq-secret-stable-a1"},
		{orqSkill{name: "heron-hub", description: "Coordinates heron hubs."}, "HERON_HUB_CLI", "orq-secret-stable-c3"},
		{orqSkill{name: "atlas-desk", description: "Coordinates atlas desks.", environment: []string{"BAR_X"}}, "BAR_X", "orq-secret-stable-b2"},
		{orqSkill{name: "cobalt-desk", description: "Coordinates cobalt desks.", environment: []string{"COBALT_X"}}, "COBALT_X", "orq-secret-stable-d4"},
		{orqSkill{name: "vortexterm", description: "Drives vortex panes."}, "TERM_PROGRAM", "vortexterm"},
	}
	skills := orqFillers(249)
	for _, d := range detected {
		skills = append(skills, d.skill)
	}
	h := newOrqHarness(t, skills)
	for _, d := range detected {
		t.Setenv(d.signal, d.value)
	}

	first := h.exec()
	for run := 2; run <= 12; run++ {
		if got := h.exec(); got != first {
			t.Fatalf("assembly %d differs from assembly 1; the system prompt must be byte-stable.\nfirst:\n%s\nrun %d:\n%s", run, orqTail(first), run, orqTail(got))
		}
	}

	list := orqSkillList(first)
	firstFiller := strings.Index(list, "filler-skill-")
	for _, d := range detected {
		assertOrqDetected(t, first, d.skill.name, d.signal)
		if at := strings.Index(list, d.skill.name); at < 0 || (firstFiller >= 0 && at > firstFiller) {
			t.Errorf("detected skill %s is not pinned above the other skills", d.skill.name)
		}
	}
	for index := 1; index <= 249; index++ {
		if name := "filler-skill-" + fmt.Sprintf("%03d", index); !strings.Contains(list, name) {
			t.Errorf("skill %s missing from the skills list", name)
			break
		}
	}
	h.assertNoValueLeak("orq-secret-stable-a1", "orq-secret-stable-b2", "orq-secret-stable-c3", "orq-secret-stable-d4")
}

// The run builds tool-call history, then the provider rejects a request as too
// long. QG must summarize (the stub answers every tool-less request with a
// marker) and retry; the retried request must still open with the same system
// prompt, environment block included.
func TestOrchestratorBlockSurvivesCompaction(t *testing.T) {
	const summaryMarker = "ORQ-SUMMARY-MARKER-4c7d"
	const historyTurns = 10
	h := newOrqHarness(t, append(orqFillers(3), orqSkill{name: "quokka-orq", description: "Coordinates quokka terminals."}))
	t.Setenv("QUOKKA_ORQ_WORKSPACE_ID", "orq-secret-compact-e5")
	h.reply = func(req orqRequest, mainIndex int) orqReply {
		switch {
		case mainIndex < 0:
			return orqReply{status: http.StatusOK, body: orqSSEText(summaryMarker)}
		case mainIndex <= historyTurns:
			args := `{"plan":[{"content":"orq step ` + strconv.Itoa(mainIndex) + ` ` + strings.Repeat("padding ", 200) + `","status":"in_progress"}]}`
			return orqReply{status: http.StatusOK, body: orqSSEToolCall("call_"+strconv.Itoa(mainIndex), "update_plan", args)}
		case mainIndex == historyTurns+1:
			return orqReply{status: http.StatusBadRequest, body: `{"error":{"message":"This model's maximum context length is 8192 tokens (context_length_exceeded).","type":"invalid_request_error","code":"context_length_exceeded"}}`}
		default:
			return orqReply{status: http.StatusOK, body: orqSSEText("PRONTO")}
		}
	}

	h.exec("--max-turns", "30")

	var main []orqRequest
	for _, req := range h.snapshot() {
		if req.tools > 0 {
			main = append(main, req)
		}
	}
	if len(main) < historyTurns+2 {
		t.Fatalf("expected at least %d tool-bearing requests, got %d", historyTurns+2, len(main))
	}
	retried := main[historyTurns+1]
	compacted := false
	for _, message := range retried.messages {
		if strings.Contains(message.content, summaryMarker) {
			compacted = true
		}
	}
	if !compacted {
		t.Fatalf("request after the context-limit error carries no compaction summary; the run never compacted")
	}
	if retried.system != main[0].system {
		t.Errorf("system prompt changed across compaction; it must stay the fixed conversation prefix")
	}
	assertOrqDetected(t, retried.system, "quokka-orq", "QUOKKA_ORQ_WORKSPACE_ID")
	h.assertNoValueLeak("orq-secret-compact-e5")
}

func TestDoctorReportsDetectedOrchestrator(t *testing.T) {
	h := newOrqHarness(t, append(orqFillers(3), orqSkill{name: "quokka-orq", description: "Coordinates quokka terminals."}))
	t.Chdir(h.workdir)

	var quiet bytes.Buffer
	Run([]string{"doctor"}, &quiet, io.Discard)
	if strings.Contains(quiet.String(), "quokka-orq") {
		t.Fatalf("doctor names quokka-orq without any signal:\n%s", quiet.String())
	}

	const value = "/tmp/orq-secret-doctor-6b"
	t.Setenv("QUOKKA_ORQ_CLI", value)
	var stdout bytes.Buffer
	Run([]string{"doctor"}, &stdout, io.Discard)
	report := stdout.String()
	if !strings.Contains(report, "quokka-orq") || !strings.Contains(report, "QUOKKA_ORQ_CLI") {
		t.Errorf("doctor must show the detected environment quokka-orq and its signal QUOKKA_ORQ_CLI:\n%s", report)
	}
	if strings.Contains(report, value) {
		t.Errorf("doctor prints the value of QUOKKA_ORQ_CLI; only the variable name may be shown:\n%s", report)
	}
}

// The detection must come from installed skills, never from a table of app
// names compiled into the binary. Scans every non-test file under internal/ and
// cmd/, embedded prompt files included.
func TestBinarySourcesNameNoOrchestratorApp(t *testing.T) {
	appNames := []string{"maestri", "elyra"}
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
			for line := 1; scanner.Scan(); line++ {
				lowered := strings.ToLower(scanner.Text())
				for _, name := range appNames {
					if strings.Contains(lowered, name) {
						t.Errorf("%s:%d names the app %q; detection must come from installed skills", path, line, name)
					}
				}
			}
			return scanner.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
