package updatenotice

import "testing"

// TestEnabledKillSwitchUnifiesBooleanParsing locks the shared boolean
// vocabulary for QG_NO_UPDATE_CHECK: the legacy "1"/"true" still disable the
// check, the new true words (yes/on, any case) disable it too, and false
// words, empty, and unrecognized values keep it enabled.
func TestEnabledKillSwitchUnifiesBooleanParsing(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset checks", value: "", want: true},
		{name: "legacy 1 disables", value: "1", want: false},
		{name: "legacy true disables", value: "true", want: false},
		{name: "new yes disables", value: "yes", want: false},
		{name: "new on disables", value: "ON", want: false},
		{name: "false word keeps the check", value: "0", want: true},
		{name: "off keeps the check", value: "off", want: true},
		{name: "garbage keeps the check", value: "banana", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := Config{CurrentVersion: "0.0.10", Env: testEnv(map[string]string{"QG_NO_UPDATE_CHECK": test.value})}
			if got := Enabled(config); got != test.want {
				t.Fatalf("QG_NO_UPDATE_CHECK=%q: Enabled = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
