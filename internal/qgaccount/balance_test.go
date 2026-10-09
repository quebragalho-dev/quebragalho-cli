package qgaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBalanceKeepsDecimalStringsAndBRLCurrency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/balance" {
			t.Fatalf("path = %q, want /balance", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"currency":  "BRL",
			"available": "145.05489680807",
			"default":   "136.81190563054",
			"bonus":     "8.24299117753",
		})
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	balance, err := client.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if balance.Currency != "BRL" {
		t.Fatalf("Currency = %q, want BRL", balance.Currency)
	}
	if balance.Available != "145.05489680807" || balance.Default != "136.81190563054" || balance.Bonus != "8.24299117753" {
		t.Fatalf("decimal strings mangled: %+v", balance)
	}
}

func TestFormatBRLRoundsHalfUpToCents(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"145.05489680807", "R$ 145,05"},
		{"136.81190563054", "R$ 136,81"},
		{"8.24299117753", "R$ 8,24"},
		{"0.005", "R$ 0,01"},
		{"0.0049", "R$ 0,00"},
		{"1234.5", "R$ 1.234,50"},
		{"0", "R$ 0,00"},
	}
	for _, tt := range tests {
		got, err := FormatBRL(tt.in)
		if err != nil {
			t.Fatalf("FormatBRL(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("FormatBRL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatBRLRejectsNonDecimal(t *testing.T) {
	for _, bad := range []string{"", "abc", "1,50", "NaN", "Inf"} {
		if _, err := FormatBRL(bad); err == nil {
			t.Fatalf("FormatBRL(%q) succeeded, want error", bad)
		}
	}
}
