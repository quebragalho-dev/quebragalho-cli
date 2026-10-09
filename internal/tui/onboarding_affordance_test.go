package tui

import (
	"testing"
)

// Each input/choice stage's error affordance names an accurate recovery step;
// stages without a tailored hint return empty.
func TestSetupErrorAffordance(t *testing.T) {
	// A stage with no tailored hint (method chooser) returns empty.
	method := model{}
	method.setup.stage = setupStageMethod
	if got := method.setupErrorAffordance(); got != "" {
		t.Fatalf("method stage should have no affordance, got %q", got)
	}
}
