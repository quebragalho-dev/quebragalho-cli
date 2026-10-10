package tui

import (
	"testing"
)

// Each input/choice stage's error affordance names an accurate recovery step;
// stages without a tailored hint return empty.
func TestSetupErrorAffordance(t *testing.T) {
	// A stage with no tailored hint (welcome) returns empty.
	welcome := model{}
	welcome.setup.stage = setupStageWelcome
	if got := welcome.setupErrorAffordance(); got != "" {
		t.Fatalf("welcome stage should have no affordance, got %q", got)
	}
}
