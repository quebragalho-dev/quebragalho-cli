package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
)

// fakeQGDevice stands in for the device-flow client so the login test asserts
// the CLI's behavior (printing the code, validating and storing the key)
// without an HTTP round trip — the HTTP side is qgaccount's own test suite.
type fakeQGDevice struct {
	auth    qgaccount.DeviceAuth
	authErr error
	key     string
	pollErr error
	polls   int
	polled  qgaccount.DeviceAuth
}

func (f *fakeQGDevice) RequestDeviceCode(context.Context) (qgaccount.DeviceAuth, error) {
	return f.auth, f.authErr
}

func (f *fakeQGDevice) PollDeviceToken(_ context.Context, auth qgaccount.DeviceAuth, _ qgaccount.DevicePollOptions) (string, error) {
	f.polls++
	f.polled = auth
	return f.key, f.pollErr
}

func TestRunLoginDeviceFlowSavesKeyWithStoredMarker(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	const deviceKey = "qg-sk-device-0123456789abcdef"
	fake := &fakeQGDevice{
		auth: qgaccount.DeviceAuth{
			DeviceCode:              "dev-code",
			UserCode:                "ABCD-1234",
			VerificationURI:         "https://app.quebragalho.dev/connect/cli",
			VerificationURIComplete: "https://app.quebragalho.dev/connect/cli?user_code=ABCD-1234",
		},
		key: deviceKey,
	}
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")
	var validated string
	deps := appDeps{
		userConfigPath: func() (string, error) { return configPath, nil },
		validateLoginKey: func(_ context.Context, key string) error {
			validated = key
			return nil
		},
		newQGDeviceClient: func() (qgDeviceAPI, error) { return fake, nil },
		openBrowser:       func(context.Context, string) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"login", "--device"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if validated != deviceKey {
		t.Fatalf("validated key = %q, want the device-flow key", validated)
	}
	if fake.polls != 1 || fake.polled.DeviceCode != fake.auth.DeviceCode {
		t.Fatalf("polls = %d with device_code %q, want one poll carrying the issued code", fake.polls, fake.polled.DeviceCode)
	}
	out := stdout.String()
	for _, want := range []string{"ABCD-1234", "connect/cli"} {
		if !strings.Contains(out, want) {
			t.Fatalf("device output missing %q:\n%s", want, out)
		}
	}

	// The key must land in the credential store with config.json carrying the
	// apiKeyStored marker — never cleartext in the config file.
	cfg := readFileConfig(t, configPath)
	if cfg.ActiveProvider != "qg" {
		t.Fatalf("ActiveProvider = %q, want qg", cfg.ActiveProvider)
	}
	if len(cfg.Providers) != 1 || !cfg.Providers[0].APIKeyStored {
		t.Fatalf("providers = %+v, want one profile with apiKeyStored", cfg.Providers)
	}
	if cfg.Providers[0].APIKey != "" {
		t.Fatalf("config.json holds cleartext key: %q", cfg.Providers[0].APIKey)
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatalf("ProviderKeyStoreAt: %v", err)
	}
	stored, found, err := store.Get("qg")
	if err != nil || !found || stored != deviceKey {
		t.Fatalf("credential store = (%q, %v, %v), want the device key", stored, found, err)
	}
}

func TestRunLoginDeviceRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"login", "--device", "--bogus"}, &stdout, &stderr, appDeps{}); code != exitUsage {
		t.Fatalf("exit = %d, want usage", code)
	}
}

// deviceBrowserDeps wires a fake device client and opener so the browser
// behavior of `qg login --device` is observable without a real browser.
func deviceBrowserDeps(t *testing.T, openErr error) (appDeps, *fakeQGDevice, *[]string) {
	t.Helper()
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	fake := &fakeQGDevice{
		auth: qgaccount.DeviceAuth{
			DeviceCode:              "dev-code",
			UserCode:                "ABCD-1234",
			VerificationURI:         "https://app.quebragalho.dev/connect/cli",
			VerificationURIComplete: "https://app.quebragalho.dev/connect/cli?user_code=ABCD-1234",
		},
		key: "qg-sk-device-0123456789abcdef",
	}
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")
	opened := &[]string{}
	deps := appDeps{
		userConfigPath:    func() (string, error) { return configPath, nil },
		validateLoginKey:  func(context.Context, string) error { return nil },
		newQGDeviceClient: func() (qgDeviceAPI, error) { return fake, nil },
		openBrowser: func(_ context.Context, url string) error {
			*opened = append(*opened, url)
			return openErr
		},
	}
	return deps, fake, opened
}

func TestLoginDeviceOpensBrowserWithCompleteURI(t *testing.T) {
	deps, fake, opened := deviceBrowserDeps(t, nil)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"login", "--device"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if len(*opened) != 1 || (*opened)[0] != fake.auth.VerificationURIComplete {
		t.Fatalf("opened = %v, want the verification_uri_complete once", *opened)
	}
}

func TestLoginDeviceNoBrowserFlagSkipsOpen(t *testing.T) {
	deps, _, opened := deviceBrowserDeps(t, nil)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"login", "--device", "--no-browser"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if len(*opened) != 0 {
		t.Fatalf("opened = %v, want no browser launch", *opened)
	}
	if !strings.Contains(stdout.String(), "user_code=ABCD-1234") {
		t.Fatalf("link must stay printed:\n%s", stdout.String())
	}
}

func TestLoginDeviceOpenerErrorStillPrintsLinkAndPolls(t *testing.T) {
	deps, fake, _ := deviceBrowserDeps(t, errors.New("no opener available"))
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"login", "--device"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if fake.polls != 1 {
		t.Fatalf("polls = %d, want the flow to continue after the opener failed", fake.polls)
	}
	if !strings.Contains(stdout.String(), "user_code=ABCD-1234") {
		t.Fatalf("link must stay printed:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no opener available") {
		t.Fatalf("stderr = %q, want a one-line opener warning", stderr.String())
	}
}
