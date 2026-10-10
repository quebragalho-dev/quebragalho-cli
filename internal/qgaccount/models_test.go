package qgaccount

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelsParsesNullableFieldsAndPricing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		// Shape observed live, plus an unknown field and a relâmpago offer.
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"deepseek-v4-pro","object":"model","owned_by":"DeepSeek","display_name":null,
			 "context_length":null,"max_output_tokens":null,
			 "capabilities":{"tools":true,"vision":false,"reasoning":true},
			 "description":null,"accepts_bonus":false,
			 "pricing":{"currency":"USD","input":"0.32","output":"0.97","cached_input":"0.033"},
			 "pricing_brl":{"currency":"BRL","input":"1.648","output":"4.9955","cached_input":"0.16995"},
			 "offer":null,"future_field":{"x":1}},
			{"id":"muse-spark","object":"model","owned_by":"QG","display_name":"Muse Spark",
			 "context_length":1000000,"max_output_tokens":64000,
			 "capabilities":{"tools":true,"vision":true,"reasoning":false},
			 "description":"fast","accepts_bonus":true,
			 "pricing":{"currency":"USD","input":"0.10","output":"0.40","cached_input":"0.01"},
			 "pricing_brl":{"currency":"BRL","input":"0.5","output":"2.0","cached_input":"0.05"},
			 "offer":{"type":"relampago","discount_percent":20}}
		]}`))
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	first := models[0]
	if first.ContextLength != nil || first.MaxOutputTokens != nil {
		t.Fatalf("null limits must stay nil, got %+v", first)
	}
	if first.DisplayName != nil || first.Description != nil {
		t.Fatalf("null strings must stay nil, got %+v", first)
	}
	if !first.Capabilities.Tools || first.Capabilities.Vision || !first.Capabilities.Reasoning {
		t.Fatalf("capabilities mismatch: %+v", first.Capabilities)
	}
	if first.Pricing.Input != "0.32" || first.Pricing.Output != "0.97" || first.Pricing.CachedInput != "0.033" || first.Pricing.Currency != "USD" {
		t.Fatalf("pricing mismatch: %+v", first.Pricing)
	}
	if first.PricingBRL.Input != "1.648" || first.PricingBRL.Currency != "BRL" {
		t.Fatalf("pricing_brl mismatch: %+v", first.PricingBRL)
	}
	if first.AcceptsBonus {
		t.Fatal("accepts_bonus = true, want false")
	}
	if first.Offer != nil {
		t.Fatalf("offer = %+v, want nil", first.Offer)
	}

	second := models[1]
	if second.ContextLength == nil || *second.ContextLength != 1000000 {
		t.Fatalf("context_length = %+v, want 1000000", second.ContextLength)
	}
	if second.MaxOutputTokens == nil || *second.MaxOutputTokens != 64000 {
		t.Fatalf("max_output_tokens = %+v, want 64000", second.MaxOutputTokens)
	}
	if second.DisplayName == nil || *second.DisplayName != "Muse Spark" {
		t.Fatalf("display_name = %+v", second.DisplayName)
	}
	if !second.AcceptsBonus {
		t.Fatal("accepts_bonus = false, want true")
	}
	if second.Offer == nil || second.Offer.Type != "relampago" || second.Offer.DiscountPercent != 20 {
		t.Fatalf("offer = %+v, want relampago 20", second.Offer)
	}
}

func TestModelsOfferNullVersusZeroStayDistinct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a","capabilities":{},"pricing":{},"pricing_brl":{},"offer":null},
			{"id":"b","capabilities":{},"pricing":{},"pricing_brl":{},"offer":{"type":"relampago"}}
		]}`))
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if models[0].Offer != nil {
		t.Fatalf("offer null became %+v", models[0].Offer)
	}
	if models[1].Offer == nil {
		t.Fatal("offer object became nil")
	}
	if models[1].Offer.DiscountPercent != 0 {
		t.Fatalf("missing discount_percent became %d, want 0 (unknown, not invented)", models[1].Offer.DiscountPercent)
	}
}

func TestModelsReadsInputCachedWithoutLosingFreeRate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"model","context_length":null,"pricing":{"currency":"USD","input":"0.1","output":"0.2","input_cached":"0"},"pricing_brl":{"currency":"BRL","input":"0.5","output":"1","input_cached":"0.025"}}]}`))
	}))
	defer server.Close()
	client := NewClient(testKey)
	client.BaseURL = server.URL
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if models[0].Pricing.CachedInput != "0" || models[0].PricingBRL.CachedInput != "0.025" {
		t.Fatalf("API cache prices discarded: %+v / %+v", models[0].Pricing, models[0].PricingBRL)
	}
}
