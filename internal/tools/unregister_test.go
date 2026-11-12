package tools

import (
	"context"
	"testing"
)

type unregisterStubTool struct{ name string }

func (tool unregisterStubTool) Name() string        { return tool.name }
func (tool unregisterStubTool) Description() string { return tool.name }
func (tool unregisterStubTool) Parameters() Schema  { return Schema{} }
func (tool unregisterStubTool) Safety() Safety      { return Safety{} }
func (tool unregisterStubTool) Run(context.Context, map[string]any) Result {
	return Result{Status: StatusOK}
}

func TestUnregisterRemovesToolAndBumpsGeneration(t *testing.T) {
	registry := NewRegistry()
	before := registry.Snapshot().Generation
	registry.Register(unregisterStubTool{name: "gone"})
	if !registry.Unregister("gone") {
		t.Fatal("Unregister() = false, want true")
	}
	if _, ok := registry.Get("gone"); ok {
		t.Fatal("tool still registered after Unregister()")
	}
	if got := registry.Snapshot().Generation; got <= before {
		t.Fatalf("generation = %d, want greater than %d", got, before)
	}
	if registry.Unregister("missing") {
		t.Fatal("Unregister(missing) = true, want false")
	}
}
