package sandbox

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent recorders must not lose each other's plans: without the
// inter-process marker lock two runners load the same snapshot and the last
// save silently drops the sibling's plan, forcing a redundant ACL re-apply on
// the next command. Eight writers record eight unique plans each (64 total,
// exactly the marker cap so no eviction can hide a loss); the final marker
// must contain every one of them.
func TestWindowsUnelevatedMarkerConcurrentRecordsKeepEveryPlan(t *testing.T) {
	home := filepath.Join(t.TempDir(), "sandbox")

	const writers = 8
	const plansPerWriter = 8

	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < plansPerWriter; i++ {
				plan := WindowsUnelevatedAppliedPlan{
					ACLPlanHash:    fmt.Sprintf("writer-%d-plan-%d", w, i),
					ACLPlanEntries: i + 1,
				}
				if err := recordWindowsUnelevatedAppliedPlan(home, plan); err != nil {
					errs <- fmt.Errorf("writer %d plan %d: record: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	marker, err := loadWindowsUnelevatedSetupMarker(home)
	if err != nil {
		t.Fatalf("load final marker: %v", err)
	}
	if len(marker.AppliedPlans) != writers*plansPerWriter {
		t.Fatalf("marker holds %d plans, want all %d: a racing save dropped plans",
			len(marker.AppliedPlans), writers*plansPerWriter)
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < plansPerWriter; i++ {
			want := WindowsUnelevatedAppliedPlan{
				ACLPlanHash:    fmt.Sprintf("writer-%d-plan-%d", w, i),
				ACLPlanEntries: i + 1,
			}
			if !marker.contains(want) {
				t.Fatalf("writer %d plan %d missing from the final marker: a racing save dropped it", w, i)
			}
		}
	}
}
