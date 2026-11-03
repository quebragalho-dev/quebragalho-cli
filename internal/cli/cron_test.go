package cli

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/cron"
)

func testCronStore(t *testing.T) *cron.Store {
	t.Helper()
	return cron.NewStore(cron.StoreOptions{RootDir: t.TempDir(), Now: func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }})
}

func TestCronAddListRemove(t *testing.T) {
	store := testCronStore(t)
	var out, errb bytes.Buffer
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }

	if code := runCronWith(store, now, []string{"add", "0 9 * * *", "--prompt", "daily"}, &out, &errb); code != 0 {
		t.Fatalf("add exit=%d err=%s", code, errb.String())
	}
	jobs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Expr != "0 9 * * *" || jobs[0].Prompt != "daily" {
		t.Fatalf("job not stored: %+v", jobs)
	}
	if jobs[0].NextRunAt.IsZero() {
		t.Fatal("NextRunAt should be set on add")
	}

	out.Reset()
	if code := runCronWith(store, now, []string{"list"}, &out, &errb); code != 0 {
		t.Fatalf("list exit=%d", code)
	}
	if !strings.Contains(out.String(), jobs[0].ID) || !strings.Contains(out.String(), "0 9 * * *") {
		t.Fatalf("list output missing job:\n%s", out.String())
	}

	if code := runCronWith(store, now, []string{"pause", jobs[0].ID}, &out, &errb); code != 0 {
		t.Fatalf("pause exit=%d", code)
	}
	j, err := store.Get(jobs[0].ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if j.Status != cron.StatusPaused {
		t.Fatalf("pause did not persist, status=%q", j.Status)
	}

	if code := runCronWith(store, now, []string{"rm", jobs[0].ID}, &out, &errb); code != 0 {
		t.Fatalf("rm exit=%d", code)
	}
	jobs, err = store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected removed, got %v", jobs)
	}
}

func TestCronAddRejectsBadExpr(t *testing.T) {
	store := testCronStore(t)
	var out, errb bytes.Buffer
	now := func() time.Time { return time.Now() }
	if code := runCronWith(store, now, []string{"add", "99 * * * *", "--prompt", "x"}, &out, &errb); code == 0 {
		t.Fatal("expected non-zero exit for invalid cron expr")
	}
	if !strings.Contains(errb.String(), "minute") {
		t.Fatalf("error should name the bad field, got %q", errb.String())
	}
}

func TestCronAddRecipe(t *testing.T) {
	store := testCronStore(t)
	var out, errb bytes.Buffer
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }
	if code := runCronWith(store, now, []string{"add", "--recipe", "git-recap"}, &out, &errb); code != 0 {
		t.Fatalf("add --recipe exit=%d err=%s", code, errb.String())
	}
	jobs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Expr != "*/30 * * * *" {
		t.Fatalf("recipe job not stored: %+v", jobs)
	}
}

func TestCronAddExplicitExprOverridesRecipe(t *testing.T) {
	store := testCronStore(t)
	var out, errb bytes.Buffer
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }
	// An explicit positional schedule must win over the recipe's default expr
	// instead of being silently dropped.
	if code := runCronWith(store, now, []string{"add", "0 9 * * *", "--recipe", "git-recap"}, &out, &errb); code != 0 {
		t.Fatalf("add exit=%d err=%s", code, errb.String())
	}
	jobs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Expr != "0 9 * * *" {
		t.Fatalf("explicit expr should win over recipe, got: %+v", jobs)
	}
	// The recipe's prompt must actually propagate — assert the exact recipe text,
	// not just non-empty (which a default prompt would also satisfy).
	recipe, ok := cron.Recipe("git-recap")
	if !ok {
		t.Fatal("git-recap recipe missing")
	}
	if jobs[0].Prompt != recipe.Prompt {
		t.Fatalf("recipe prompt not propagated: got %q want %q", jobs[0].Prompt, recipe.Prompt)
	}
}

func TestCronAddRejectsExtraArgs(t *testing.T) {
	store := testCronStore(t)
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }
	var out, errb bytes.Buffer
	if code := runCronWith(store, now, []string{"add", "0 9 * * *", "extra", "--prompt", "x"}, &out, &errb); code == 0 {
		t.Fatal("expected non-zero exit for extra positional args")
	}
	jobs, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("no job should be stored on a usage error, got %v", jobs)
	}
}

func TestCronResumeRejectsImpossibleSchedule(t *testing.T) {
	store := testCronStore(t)
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }
	job, err := store.Add(cron.Job{Expr: "0 0 30 2 *", Prompt: "x", Status: cron.StatusPaused})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	var out, errb bytes.Buffer
	if code := runCronWith(store, now, []string{"resume", job.ID}, &out, &errb); code == 0 {
		t.Fatal("resume of an impossible schedule must be rejected")
	}
	j, err := store.Get(job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if j.Status != cron.StatusPaused {
		t.Fatalf("job must remain paused after a rejected resume, got %q", j.Status)
	}
}

func TestCronListShowsAtivaEPausada(t *testing.T) {
	store := testCronStore(t)
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }
	active, err := store.Add(cron.Job{Expr: "0 9 * * *", Prompt: "daily", Status: cron.StatusActive, NextRunAt: now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Add active: %v", err)
	}
	paused, err := store.Add(cron.Job{Expr: "0 10 * * *", Prompt: "later", Status: cron.StatusPaused, NextRunAt: now().Add(2 * time.Hour)})
	if err != nil {
		t.Fatalf("Add paused: %v", err)
	}
	var out, errb bytes.Buffer
	if code := runCronWith(store, now, []string{"list"}, &out, &errb); code != 0 {
		t.Fatalf("list exit=%d err=%s", code, errb.String())
	}
	text := out.String()
	if !strings.Contains(text, active.ID+" · ativa ·") {
		t.Fatalf("active job must show ativa:\n%s", text)
	}
	if !strings.Contains(text, paused.ID+" · pausada ·") {
		t.Fatalf("paused job must show pausada:\n%s", text)
	}
	if strings.Contains(text, "· active ·") || strings.Contains(text, "· paused ·") {
		t.Fatalf("list leaked English status:\n%s", text)
	}
	stored, _ := store.Get(active.ID)
	if stored.Status != cron.StatusActive {
		t.Fatalf("persisted status must stay English active, got %q", stored.Status)
	}
}

// TestCronIDCommandsRejectFlags proves rm/pause/resume fail loudly on dash-led
// arguments (e.g. a trailing --json) instead of silently swallowing them: the
// job must be left untouched and nothing may be printed to stdout. -h/--help
// prints help before the flag guard.
func TestCronIDCommandsRejectFlags(t *testing.T) {
	store := testCronStore(t)
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }
	job, err := store.Add(cron.Job{Expr: "0 9 * * *", Prompt: "daily", Status: cron.StatusPaused, NextRunAt: now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, args := range [][]string{
		{"rm", job.ID, "--json"},
		{"pause", job.ID, "--json"},
		{"resume", job.ID, "--json"},
	} {
		var out, errb bytes.Buffer
		if code := runCronWith(store, now, args, &out, &errb); code != exitUsage {
			t.Fatalf("cron %v exit=%d, want %d; stdout=%q stderr=%q", args, code, exitUsage, out.String(), errb.String())
		}
		if out.Len() != 0 {
			t.Fatalf("cron %v must not print to stdout on a flag error, got %q", args, out.String())
		}
		if _, err := store.Get(job.ID); err != nil {
			t.Fatalf("cron %v must leave the job untouched: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"rm", "--help"},
		{"pause", "-h"},
		{"resume", "--help", "--json"},
	} {
		var out, errb bytes.Buffer
		if code := runCronWith(store, now, args, &out, &errb); code != exitSuccess {
			t.Fatalf("cron %v exit=%d, want %d; stderr=%q", args, code, exitSuccess, errb.String())
		}
		if !strings.Contains(out.String(), "qg cron") {
			t.Fatalf("cron %v must print help to stdout, got %q", args, out.String())
		}
	}
}

// TestCronListJSON proves `cron list --json` emits {"jobs": [...]} with the
// stored jobs (English machine fields, UTC timestamps, redacted prompts) and
// {"jobs": []} for an empty store. Field names are pinned by decoding into
// maps, so a renamed struct tag fails here. Unknown positionals and flags are
// usage errors that write nothing to stdout.
func TestCronListJSON(t *testing.T) {
	store := testCronStore(t)
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }
	const token = "sk-fake-test-token-123456789"

	var out, errb bytes.Buffer
	if code := runCronWith(store, now, []string{"list", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("list --json exit=%d err=%s", code, errb.String())
	}
	var empty map[string]any
	if err := json.Unmarshal(out.Bytes(), &empty); err != nil {
		t.Fatalf("empty list --json is not valid JSON %q: %v", out.String(), err)
	}
	emptyJobs, ok := empty["jobs"].([]any)
	if !ok || len(emptyJobs) != 0 {
		t.Fatalf("empty list --json = %q, want {\"jobs\": []}", out.String())
	}
	if len(empty) != 1 {
		t.Fatalf("empty list --json keys = %v, want only [jobs]", slices.Sorted(maps.Keys(empty)))
	}

	added, err := store.Add(cron.Job{Expr: "0 9 * * *", Prompt: "rotate api_key=" + token, Status: cron.StatusActive, NextRunAt: now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	zoned, err := store.Add(cron.Job{Expr: "0 10 * * *", Prompt: "zoned", Status: cron.StatusPaused, NextRunAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.FixedZone("X", 3*3600))})
	if err != nil {
		t.Fatalf("Add zoned: %v", err)
	}
	out.Reset()
	errb.Reset()
	if code := runCronWith(store, now, []string{"list", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("list --json exit=%d err=%s", code, errb.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("list --json is not valid JSON %q: %v", out.String(), err)
	}
	rawJobs, ok := payload["jobs"].([]any)
	if !ok || len(rawJobs) != 2 {
		t.Fatalf("list --json jobs = %v, want 2 jobs in %q", payload["jobs"], out.String())
	}
	byID := map[string]map[string]any{}
	for _, raw := range rawJobs {
		jobMap, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("list --json job is not an object: %v", raw)
		}
		id, _ := jobMap["id"].(string)
		byID[id] = jobMap
	}
	for id, want := range map[string]map[string]any{
		added.ID: {
			"expr":      "0 9 * * *",
			"prompt":    "rotate api_key=[REDACTED]",
			"status":    cron.StatusActive,
			"fireCount": float64(0),
			"nextRunAt": "2026-06-09T09:00:00Z",
			"createdAt": "2026-06-09T08:00:00Z",
		},
		zoned.ID: {
			"expr":      "0 10 * * *",
			"prompt":    "zoned",
			"status":    cron.StatusPaused,
			"fireCount": float64(0),
			"nextRunAt": "2026-06-09T09:00:00Z",
			"createdAt": "2026-06-09T08:00:00Z",
		},
	} {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("list --json missing job %q in %q", id, out.String())
		}
		if got["id"] != id {
			t.Fatalf("list --json job[id] = %v, want %q", got["id"], id)
		}
		if len(got) != len(want)+1 {
			t.Fatalf("list --json job keys = %v, want %v plus id", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
		}
		for key, wantValue := range want {
			if got[key] != wantValue {
				t.Fatalf("list --json job[%q] = %v, want %v (full: %q)", key, got[key], wantValue, out.String())
			}
		}
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("list --json leaked the token: %q", out.String())
	}

	// The human text redacts too, before the excerpt is cut.
	out.Reset()
	errb.Reset()
	if code := runCronWith(store, now, []string{"list"}, &out, &errb); code != 0 {
		t.Fatalf("list exit=%d err=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "[REDACTED]") || strings.Contains(out.String(), token) {
		t.Fatalf("list text must redact the token, got %q", out.String())
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"list", "extra"}, want: "argumento inesperado em cron list"},
		{args: []string{"list", "-x"}, want: "flag desconhecida em cron list"},
	} {
		out.Reset()
		errb.Reset()
		if code := runCronWith(store, now, tc.args, &out, &errb); code != exitUsage {
			t.Fatalf("cron %v exit=%d, want %d", tc.args, code, exitUsage)
		}
		if out.Len() != 0 {
			t.Fatalf("cron %v must not print to stdout, got %q", tc.args, out.String())
		}
		if !strings.Contains(errb.String(), tc.want) {
			t.Fatalf("cron %v stderr = %q, want %q", tc.args, errb.String(), tc.want)
		}
	}
}
