package cli

import (
	"bytes"
	"encoding/json"
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
// job must be left untouched and nothing may be printed to stdout.
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
}

// TestCronListJSON proves `cron list --json` emits the stored jobs as a JSON
// array (English machine fields, never translated statuses) and encodes an
// empty store as [] rather than null.
func TestCronListJSON(t *testing.T) {
	store := testCronStore(t)
	now := func() time.Time { return time.Date(2026, 6, 9, 8, 0, 0, 0, time.UTC) }

	var out, errb bytes.Buffer
	if code := runCronWith(store, now, []string{"list", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("list --json exit=%d err=%s", code, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty list --json = %q, want [] (never null)", out.String())
	}

	added, err := store.Add(cron.Job{Expr: "0 9 * * *", Prompt: "daily", Status: cron.StatusActive, NextRunAt: now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	out.Reset()
	errb.Reset()
	if code := runCronWith(store, now, []string{"list", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("list --json exit=%d err=%s", code, errb.String())
	}
	var jobs []cron.Job
	if err := json.Unmarshal(out.Bytes(), &jobs); err != nil {
		t.Fatalf("list --json is not valid JSON %q: %v", out.String(), err)
	}
	if len(jobs) != 1 {
		t.Fatalf("list --json decoded %d jobs, want 1: %q", len(jobs), out.String())
	}
	got := jobs[0]
	if got.ID != added.ID || got.Expr != "0 9 * * *" || got.Prompt != "daily" || got.Status != cron.StatusActive {
		t.Fatalf("list --json job = %+v, want id %q expr %q prompt %q status %q", got, added.ID, "0 9 * * *", "daily", cron.StatusActive)
	}
}
