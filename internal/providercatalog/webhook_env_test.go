package providercatalog

import (
	"reflect"
	"testing"
)

// Audit: the user's QG key is not the only bearer secret in the ambient
// environment. The notify webhook URL typically embeds a token (see
// internal/notify/webhook_wire.go), so every child-process path that strips
// QG credentials (agent shell commands, hooks, MCP servers) must strip it
// too. The summary variable stays: it is a free-text run label
// ("nightly audit run"), not a credential.
func TestWithoutQGCredentialsDropsWebhookURLKeepsSummary(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"QG_API_KEY=qg-sk-primary",
		"QG_NOTIFY_WEBHOOK_URL=https://hooks.example.test/token-secret",
		"qg_notify_webhook_url=https://hooks.example.test/lowercase",
		"QG_NOTIFY_WEBHOOK_SUMMARY=nightly audit run",
		"HOME=/home/u",
	}
	original := append([]string(nil), env...)

	got := WithoutQGCredentials(env)
	want := []string{
		"PATH=/usr/bin",
		"QG_NOTIFY_WEBHOOK_SUMMARY=nightly audit run",
		"HOME=/home/u",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutQGCredentials() = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(env, original) {
		t.Fatal("WithoutQGCredentials modified its input")
	}
}
