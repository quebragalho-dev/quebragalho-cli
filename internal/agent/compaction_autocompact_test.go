package agent

import "testing"

// AutoCompactThreshold maps a context window and a user limit to the
// proactive trigger: a negative limit turns proactive compaction off (0),
// anything else follows the automatic threshold ceiling.
func TestAutoCompactThresholdExported(t *testing.T) {
	cases := []struct {
		window int
		limit  int
		want   int
	}{
		{window: 1000, limit: 0, want: 700},
		{window: 1000, limit: 500, want: 500},
		{window: 1000, limit: 900, want: 700},
		{window: 1000, limit: -1, want: 0},
		{window: 0, limit: 500, want: 0},
	}
	for _, tc := range cases {
		if got := AutoCompactThreshold(tc.window, tc.limit); got != tc.want {
			t.Errorf("AutoCompactThreshold(%d, %d) = %d, want %d", tc.window, tc.limit, got, tc.want)
		}
	}
}
