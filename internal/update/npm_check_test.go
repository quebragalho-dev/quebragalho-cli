package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
			if want := "/@quebragalho%2Fcli/latest"; gotPath != want {
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestCheckNpmRefusesPlainHTTPEndpoints(t *testing.T) {
	var networkHits atomic.Int32
	previous := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		networkHits.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"version":"9.9.9"}`)),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })

	for _, endpoint := range []string{
		"http://registry.example.test",
		"HTTP://registry.example.test/",
		"http://127.0.0.1.example.test",
		"http://localhost.example.test",
		"ftp://registry.example.test",
	} {
		result, err := CheckNpm(shortContext(t), Options{
			CurrentVersion: "0.0.10",
			Endpoint:       endpoint,
		})
		if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
			t.Errorf("CheckNpm(%q) = %+v, %v; want insecure-URL refusal", endpoint, result, err)
		}
	}
	if got := networkHits.Load(); got != 0 {
		t.Fatalf("CheckNpm contacted insecure endpoints %d time(s), want 0", got)
	}

	for _, invalid := range []string{"https://", "https:/missing-host", "http://registry.example.test\x00"} {
		if _, err := CheckNpm(shortContext(t), Options{CurrentVersion: "0.0.10", Endpoint: invalid}); err == nil {
			t.Errorf("CheckNpm(%q) succeeded, want invalid-endpoint error", invalid)
		}
	}
}

func TestCheckNpmRefusesHTTPSToHTTPRedirect(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"9.9.9"}`))
	}))
	defer plain.Close()

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer secure.Close()

	previous := http.DefaultTransport
	http.DefaultTransport = secure.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	result, err := CheckNpm(shortContext(t), Options{
		CurrentVersion: "0.0.10",
		Endpoint:       secure.URL,
	})
	if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
		t.Fatalf("CheckNpm across https->http redirect = %+v, %v; want insecure-redirect refusal", result, err)
	}
	if got := plainHits.Load(); got != 0 {
		t.Fatalf("plaintext redirect hop received %d request(s), want 0", got)
	}
}

func TestCheckNpmRefusesLoopbackRedirectToRemoteHTTP(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://registry.example.test/@quebragalho%2Fcli/latest", http.StatusFound)
	}))
	defer local.Close()

	result, err := CheckNpm(shortContext(t), Options{
		CurrentVersion: "0.0.10",
		Endpoint:       local.URL,
	})
	if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
		t.Fatalf("CheckNpm redirected from loopback to remote http = %+v, %v; want insecure-URL refusal", result, err)
	}
}

func TestCheckNpmFollowsHTTPSRedirect(t *testing.T) {
	var secure *httptest.Server
	secure = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mirror/latest" {
			http.Redirect(w, r, secure.URL+"/mirror/latest", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"0.0.22"}`))
	}))
	defer secure.Close()

	previous := http.DefaultTransport
	http.DefaultTransport = secure.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	result, err := CheckNpm(shortContext(t), Options{
		CurrentVersion: "0.0.21",
		Endpoint:       secure.URL,
	})
	if err != nil {
		t.Fatalf("CheckNpm across https redirect: %v", err)
	}
	if result.LatestVersion != "0.0.22" || !result.UpdateAvailable {
		t.Fatalf("result = %+v, want LatestVersion=0.0.22 UpdateAvailable=true", result)
	}
}
