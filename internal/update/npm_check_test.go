package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckNpmReadsLatestVersion(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		body          string
		current       string
		wantLatest    string
		wantAvailable bool
		wantErr       bool
	}{
		{name: "newer", status: http.StatusOK, body: `{"version":"0.0.11"}`, current: "0.0.10", wantLatest: "0.0.11", wantAvailable: true},
		{name: "equal", status: http.StatusOK, body: `{"version":"0.0.10"}`, current: "0.0.10", wantLatest: "0.0.10", wantAvailable: false},
		{name: "older", status: http.StatusOK, body: `{"version":"0.0.9"}`, current: "0.0.10", wantLatest: "0.0.9", wantAvailable: false},
		{name: "server error", status: http.StatusInternalServerError, body: "registry down", current: "0.0.10", wantErr: true},
		{name: "invalid json", status: http.StatusOK, body: "{not json", current: "0.0.10", wantErr: true},
		{name: "invalid version", status: http.StatusOK, body: `{"version":"nightly"}`, current: "0.0.10", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				gotPath = request.URL.EscapedPath()
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()

			result, err := CheckNpm(context.Background(), Options{CurrentVersion: test.current, Endpoint: server.URL})
			if test.wantErr {
				if err == nil {
					t.Fatalf("CheckNpm succeeded, want error (status %d, body %q)", test.status, test.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckNpm returned error: %v", err)
			}
			if want := "/@quebragalho-dev%2Fcli/latest"; gotPath != want {
				t.Fatalf("npm request path = %q, want %q", gotPath, want)
			}
			if result.LatestVersion != test.wantLatest {
				t.Fatalf("LatestVersion = %q, want %q", result.LatestVersion, test.wantLatest)
			}
			if result.UpdateAvailable != test.wantAvailable {
				t.Fatalf("UpdateAvailable = %v, want %v", result.UpdateAvailable, test.wantAvailable)
			}
			if result.CurrentVersion != test.current {
				t.Fatalf("CurrentVersion = %q, want %q", result.CurrentVersion, test.current)
			}
		})
	}
}
