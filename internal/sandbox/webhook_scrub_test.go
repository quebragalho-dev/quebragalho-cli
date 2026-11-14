package sandbox

import (
	"reflect"
	"testing"
)

// Audit: QG_NOTIFY_WEBHOOK_URL typically embeds a token (see
// internal/notify/webhook_wire.go), so sandboxed child processes must not
// inherit it. The summary variable stays: it is a free-text run label
// ("nightly audit run"), not a credential, and dropping it would change
// notification behavior with no security gain.
func TestScrubSensitiveEnvDropsNotifyWebhookURL(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"QG_NOTIFY_WEBHOOK_URL=https://hooks.example.test/token-secret",
		"qg_notify_webhook_url=https://hooks.example.test/lowercase",
		"QG_NOTIFY_WEBHOOK_SUMMARY=nightly audit run",
		"HOME=/home/u",
	}

	got := scrubSensitiveEnv(env)
	want := []string{
		"PATH=/usr/bin",
		"QG_NOTIFY_WEBHOOK_SUMMARY=nightly audit run",
		"HOME=/home/u",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scrubSensitiveEnv() = %v, want %v", got, want)
	}
}
