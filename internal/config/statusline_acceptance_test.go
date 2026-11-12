package config

import (
	"testing"
)

// A statusline block written per the documented schema (docs/TUI_TELEMETRY.md)
// is a known config key: `qg config validate` / doctor report nothing for it.
func TestStatusLineAcceptance_ValidateKnowsStatusLineBlock(t *testing.T) {
	doc := []byte(`{
  "statusline": {
    "lines": [
      {"left": ["mode", "effort"], "right": ["context", "branch"]},
      {"left": ["cwd"], "right": ["time"]}
    ],
    "separator": " · ",
    "command": {"command": "sh /home/u/.config/qg/statusline/statusline.sh"},
    "timeoutMs": 2000,
    "refreshMs": 5000
  }
}`)
	_, issues := ValidateBytes(doc)
	for _, issue := range issues {
		t.Errorf("valid statusline config flagged by validate: %q: %s", issue.FieldPath, issue.Message)
	}
}
