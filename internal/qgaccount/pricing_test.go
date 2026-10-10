package qgaccount

import (
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"testing"
)

func TestAPIPricingKeepsExactPerMillionCosts(t *testing.T) {
	p := ModelPricing{Currency: "BRL", Input: "1.648", Output: "4.9955", CachedInput: "0.16995"}
	amount, err := p.Cost(qgruntime.Usage{InputTokens: 1000, CachedInputTokens: 250, OutputTokens: 100, ReasoningTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got := amount.FloatString(10); got != "0.0017780375" {
		t.Fatalf("cost=%s, want exact 0.0017780375", got)
	}
	if got := amount.FloatString(2); got != "0.00" {
		t.Fatalf("display cost=%s", got)
	}
	p.Currency = "USD"
	p.Input = "0.04"
	p.Output = "0.08"
	p.CachedInput = "0"
	amount, err = p.Cost(qgruntime.Usage{InputTokens: 1000000, CachedInputTokens: 750000, OutputTokens: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	if got := amount.FloatString(8); got != "0.09000000" {
		t.Fatalf("free cached input billed: %s", got)
	}
}

func TestAPIPricingDoesNotInventMissingRates(t *testing.T) {
	for _, p := range []ModelPricing{
		{Currency: "BRL", Input: "1", Output: "2"},
		{Currency: "BRL", Input: "NaN", Output: "2", CachedInput: "1"},
		{Currency: "BRL", Input: "-1", Output: "2", CachedInput: "1"},
		{Currency: "EUR", Input: "1", Output: "2", CachedInput: "1"},
	} {
		if _, err := p.Cost(qgruntime.Usage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 10}); err == nil {
			t.Fatalf("unknown/invalid rate must fail: %+v", p)
		}
	}
}
