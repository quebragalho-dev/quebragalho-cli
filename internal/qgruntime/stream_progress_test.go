package qgruntime

import (
	"context"
	"testing"
)

func TestBatchSevenStreamingProgressDoesNotBillSnapshots(t *testing.T) {
	events := make(chan StreamEvent, 6)
	events <- StreamEvent{Type: StreamEventText, Content: "first"}
	events <- StreamEvent{Type: StreamEventUsage, Usage: Usage{InputTokens: 12000, OutputTokens: 400}}
	events <- StreamEvent{Type: StreamEventText, Content: "second"}
	events <- StreamEvent{Type: StreamEventUsage, Usage: Usage{OutputTokens: 850}}
	events <- StreamEvent{Type: StreamEventDone}
	close(events)
	var progress []Usage
	bills := 0
	collected := CollectStreamWithOptions(context.Background(), events, CollectOptions{
		OnUsageProgress: func(u Usage) { progress = append(progress, u) },
		OnText: func(s string) {
			if s == "second" && (len(progress) != 2 || progress[1].EffectiveOutputTokens() != 400 || bills != 0) {
				t.Fatalf("no unbilled real-time snapshot: %+v bills=%d", progress, bills)
			}
		},
		OnUsage: func(u Usage) {
			bills++
			if u.TotalTokens() != 12850 {
				t.Errorf("bill=%+v", u)
			}
		},
	})
	if bills != 1 || collected.Usage.TotalTokens() != 12850 {
		t.Fatalf("bills=%d usage=%+v", bills, collected.Usage)
	}
	if len(progress) != 4 || progress[0].TotalTokens() != 0 || progress[2].TotalTokens() != 12850 || progress[2].Elapsed <= 0 {
		t.Fatalf("snapshots=%+v", progress)
	}
	var final Usage
	CollectStreamWithOptions(context.Background(), closedProgressEvents(Usage{OutputTokens: 12}), CollectOptions{OnUsageProgress: func(u Usage) { final = u }})
	if final.Elapsed != 0 {
		t.Fatalf("non-streaming call invented speed: %+v", final)
	}
}

func closedProgressEvents(u Usage) <-chan StreamEvent {
	c := make(chan StreamEvent, 1)
	c <- StreamEvent{Type: StreamEventUsage, Usage: u}
	close(c)
	return c
}
