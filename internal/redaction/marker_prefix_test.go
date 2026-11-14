package redaction

import (
	"strings"
	"testing"
)

// TestRedactStringMarkerPrefixCoverage pins one minimal example per
// shaped-secret pattern and per accepted variant. Every case is shaped so
// that only its own pattern can fire (no "=", quotes, or scheme words that
// would let an earlier pass redact it first): the secret must be gone from
// the output while the surrounding text survives. This is also the
// mutation net for the substring probes that guard each scan — every probe
// is a necessary condition of its pattern, so weakening a probe (a rarer
// marker, a longer one, or dropping a variant) leaves one of these inputs
// unredacted and fails the table.
func TestRedactStringMarkerPrefixCoverage(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
		keep   string
	}{
		{
			name:   "github token ghp",
			input:  "deploy token ghp_1234567890abcdefghijklmnopqrstuvwxyz done",
			secret: "ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			keep:   "deploy token",
		},
		{
			name:   "github token gho",
			input:  "deploy token gho_1234567890abcdefghijklmnopqrstuvwxyz done",
			secret: "gho_1234567890abcdefghijklmnopqrstuvwxyz",
			keep:   "deploy token",
		},
		{
			name:   "github token ghs",
			input:  "deploy token ghs_1234567890abcdefghijklmnopqrstuvwxyz done",
			secret: "ghs_1234567890abcdefghijklmnopqrstuvwxyz",
			keep:   "deploy token",
		},
		{
			name:   "github token ghr",
			input:  "deploy token ghr_1234567890abcdefghijklmnopqrstuvwxyz done",
			secret: "ghr_1234567890abcdefghijklmnopqrstuvwxyz",
			keep:   "deploy token",
		},
		{
			name:   "github token ghu",
			input:  "deploy token ghu_1234567890abcdefghijklmnopqrstuvwxyz done",
			secret: "ghu_1234567890abcdefghijklmnopqrstuvwxyz",
			keep:   "deploy token",
		},
		{
			name:   "aws access key",
			input:  "temp creds AKIAIOSFODNN7EXAMPLE rotating",
			secret: "AKIAIOSFODNN7EXAMPLE",
			keep:   "temp creds",
		},
		{
			name:   "aws temporary access key",
			input:  "temp creds ASIAIOSFODNN7EXAMPLE rotating",
			secret: "ASIAIOSFODNN7EXAMPLE",
			keep:   "temp creds",
		},
		{
			name:   "wss url password",
			input:  "backup wss://alice:s3cret-db-pass@example.com/db nightly",
			secret: "s3cret-db-pass",
			keep:   "alice",
		},
		{
			name:   "ftp url password",
			input:  "mirror ftp://bob:ftppass9@files.example.com/x weekly",
			secret: "ftppass9",
			keep:   "bob",
		},
		{
			// Only the query pass can fire here: the "+" keeps the
			// assignment pass from matching the key, so this is the
			// query-pattern probe's case (an "&key=" form with no "?").
			name:   "query amp key without question mark",
			input:  "cb x=1&api+key=s3cr3t end",
			secret: "s3cr3t",
			keep:   "x=1",
		},
		{
			name:   "query plus key",
			input:  "cb ?api+key=s3cr3t end",
			secret: "s3cr3t",
			keep:   "?api+key=",
		},
		{
			name:   "jwt strict header payload",
			input:  "saw eyJ0eXAiOiJKV1QifQ.eyJzdWIiOiIxIn0.c2lnbmF0dXJlMTIz in logs",
			secret: "eyJ0eXAiOiJKV1QifQ.eyJzdWIiOiIxIn0.c2lnbmF0dXJlMTIz",
			keep:   "saw",
		},
		{
			name:   "jwt loose non eyJ payload",
			input:  "saw eyJ0eXAiOiJKV1QifQ.cGF5bG9hZDEy.c2lnbmF0dXJlMTIz in logs",
			secret: "eyJ0eXAiOiJKV1QifQ.cGF5bG9hZDEy.c2lnbmF0dXJlMTIz",
			keep:   "saw",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactString(tc.input, Options{})
			if strings.Contains(got, tc.secret) {
				t.Fatalf("leaked %q in %q", tc.secret, got)
			}
			if tc.keep != "" && !strings.Contains(got, tc.keep) {
				t.Fatalf("lost surrounding text %q in %q", tc.keep, got)
			}
		})
	}
}
