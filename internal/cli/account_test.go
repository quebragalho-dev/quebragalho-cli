package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
)

// fakeQGAccount implements the account surface the CLI commands consume.
type fakeQGAccount struct {
	balance      qgaccount.Balance
	balanceErr   error
	balanceCalls int
	key          qgaccount.KeyInfo
	keyErr       error
	usage        qgaccount.UsageReport
	usageErr     error
	usageParams  qgaccount.UsageParams
	usageCalls   int
	models       []qgaccount.ModelInfo
	modelsErr    error
}

func (f *fakeQGAccount) Balance(context.Context) (qgaccount.Balance, error) {
	f.balanceCalls++
	return f.balance, f.balanceErr
}

func (f *fakeQGAccount) Key(context.Context) (qgaccount.KeyInfo, error) {
	return f.key, f.keyErr
}

func (f *fakeQGAccount) Usage(_ context.Context, params qgaccount.UsageParams) (qgaccount.UsageReport, error) {
	f.usageCalls++
	f.usageParams = params
	return f.usage, f.usageErr
}

func (f *fakeQGAccount) Models(context.Context) ([]qgaccount.ModelInfo, error) {
	return f.models, f.modelsErr
}

func accountDeps(t *testing.T, fake *fakeQGAccount) appDeps {
	t.Helper()
	deps := commandCenterDeps(t)
	deps.newQGAccountClient = func() (qgAccountAPI, error) {
		return fake, nil
	}
	return deps
}

func TestRunBalanceFormatsBRLAndNeverCaches(t *testing.T) {
	fake := &fakeQGAccount{balance: qgaccount.Balance{
		Currency:  "BRL",
		Available: "145.05489680807",
		Default:   "136.81190563054",
		Bonus:     "8.24299117753",
	}}
	var stdout, stderr bytes.Buffer
	deps := accountDeps(t, fake)

	if code := runWithDeps([]string{"balance"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"145,05", "136,81", "8,24", "BRL"} {
		if !strings.Contains(out, want) {
			t.Fatalf("balance output missing %q:\n%s", want, out)
		}
	}

	// Second invocation must hit the API again: a cached saldo is a bug.
	stdout.Reset()
	stderr.Reset()
	if code := runWithDeps([]string{"balance"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("second exit = %d: %s", code, stderr.String())
	}
	if fake.balanceCalls != 2 {
		t.Fatalf("balanceCalls = %d, want 2 (no cache)", fake.balanceCalls)
	}
}

func TestRunBalanceJSONKeepsDecimalStrings(t *testing.T) {
	fake := &fakeQGAccount{balance: qgaccount.Balance{
		Currency:  "BRL",
		Available: "145.05489680807",
		Default:   "136.81190563054",
		Bonus:     "8.24299117753",
	}}
	var stdout, stderr bytes.Buffer
	deps := accountDeps(t, fake)

	if code := runWithDeps([]string{"balance", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("JSON decode: %v\n%s", err, stdout.String())
	}
	if payload["available"] != "145.05489680807" || payload["currency"] != "BRL" {
		t.Fatalf("JSON payload = %v, want raw decimal strings", payload)
	}
}

func TestRunKeyAndWhoamiShareOutput(t *testing.T) {
	fake := &fakeQGAccount{key: qgaccount.KeyInfo{
		ID:        "e1645173",
		Name:      "teste cli",
		KeyPrefix: "qg-sk-Rhcii8",
		CreatedAt: "2026-10-09T03:08:07Z",
		Plan:      qgaccount.KeyPlan{Code: "prepaid", Type: "prepaid"},
	}}
	for _, command := range []string{"key", "whoami"} {
		var stdout, stderr bytes.Buffer
		deps := accountDeps(t, fake)
		if code := runWithDeps([]string{command}, &stdout, &stderr, deps); code != exitSuccess {
			t.Fatalf("%s exit = %d: %s", command, code, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"teste cli", "qg-sk-Rhcii8", "prepaid", "todos"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s output missing %q:\n%s", command, want, out)
			}
		}
	}
}

func TestRunKeyJSON(t *testing.T) {
	fake := &fakeQGAccount{key: qgaccount.KeyInfo{
		ID: "e1645173", Name: "teste cli", KeyPrefix: "qg-sk-Rhcii8",
		CreatedAt:     "2026-10-09T03:08:07Z",
		AllowedModels: nil,
		Plan:          qgaccount.KeyPlan{Code: "prepaid", Type: "prepaid"},
	}}
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"key", "--json"}, &stdout, &stderr, accountDeps(t, fake)); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	var payload struct {
		ID            string   `json:"id"`
		AllowedModels []string `json:"allowed_models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if payload.ID != "e1645173" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.AllowedModels != nil {
		t.Fatalf("allowed_models = %v, want JSON null (todos)", payload.AllowedModels)
	}
}

// The CLI surfaces the provider failure and picks the exit code; key redaction
// inside an APIError is qgaccount's job (TestAPIErrorRedactsKeyFromServerMessage
// puts the key in a real fixture) and cannot be asserted through a fake that
// never held a key.
func TestRunBalanceSurfacesAPIErrors(t *testing.T) {
	fake := &fakeQGAccount{balanceErr: &qgaccount.APIError{StatusCode: 401, Message: "invalid api key"}}
	var stdout, stderr bytes.Buffer
	code := runWithDeps([]string{"balance"}, &stdout, &stderr, accountDeps(t, fake))
	if code != exitProvider {
		t.Fatalf("exit = %d, want %d", code, exitProvider)
	}
	if !strings.Contains(stderr.String(), "invalid api key") {
		t.Fatalf("server message not surfaced: %q", stderr.String())
	}
}

// TestAccountCommandsStayHermeticDespiteQGAPIKey proves the injection wins over
// the ambient QG_API_KEY: even with a key in the environment, an injected client
// is used and no production network resolution happens (hermetic-test contract).
func TestAccountCommandsStayHermeticDespiteQGAPIKey(t *testing.T) {
	t.Setenv("QG_API_KEY", "qg-sk-ambient-should-not-reach-network")
	fake := &fakeQGAccount{balance: qgaccount.Balance{
		Currency: "BRL", Available: "1.00", Default: "1.00", Bonus: "0.00",
	}}
	deps := accountDeps(t, fake)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"balance", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if fake.balanceCalls != 1 {
		t.Fatalf("balanceCalls = %d, want 1 (injected client, not env-driven production)", fake.balanceCalls)
	}
	if !strings.Contains(stdout.String(), `"available": "1.00"`) {
		t.Fatalf("output came from somewhere other than the injected fake:\n%s", stdout.String())
	}
}
