package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const insecureUpdateURLMarker = "insecure update"

func shortContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Pins that custom endpoints with a non-loopback plain-http (or non-web)
// scheme are refused.
func TestResolveEndpointRefusesPlainHTTP(t *testing.T) {
	for _, endpoint := range []string{
		"http://example.test/releases/latest",
		"HTTP://example.test/releases/latest",
		"http://127.0.0.1.example.test/latest",
		"ftp://example.test/latest",
	} {
		if got, err := ResolveEndpoint(endpoint, DefaultRepository); err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
			t.Errorf("ResolveEndpoint(%q) = %q, %v; want an insecure-URL refusal", endpoint, got, err)
		}
	}
}

// Pins that loopback plain-http, https and data endpoints keep working.
func TestResolveEndpointKeepsLoopbackHTTPAndDataEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:8080/latest",
		"http://localhost:8080/latest",
		"http://[::1]:8080/latest",
		"https://example.test/latest",
		`data:application/json,{"tag_name":"v0.2.0"}`,
	} {
		if got, err := ResolveEndpoint(endpoint, DefaultRepository); err != nil || got != endpoint {
			t.Errorf("ResolveEndpoint(%q) = %q, %v; want it accepted unchanged", endpoint, got, err)
		}
	}
}

// Pins that release metadata is never fetched over plain http.
func TestFetchReleaseRefusesPlainHTTP(t *testing.T) {
	_, err := fetchRelease(shortContext(t), "http://example.test/releases/latest")
	if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
		t.Fatalf("fetchRelease over plain http = %v, want an insecure-URL refusal", err)
	}
}

// Pins that release files are never downloaded over plain http, and that a
// refused download leaves no file behind.
func TestDownloadFileRefusesPlainHTTP(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "archive")
	err := downloadFile(shortContext(t), "http://example.test/qg.tar.gz", dest)
	if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
		t.Fatalf("downloadFile over plain http = %v, want an insecure-URL refusal", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("a refused download left a file behind: %v", statErr)
	}
}

// Pins that an https-to-http downgrade redirect is refused for downloads.
func TestDownloadFileRefusesARedirectFromHTTPSToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("served over plaintext"))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/qg.tar.gz", http.StatusFound)
	}))
	defer secure.Close()
	// The updater's client has no transport of its own, so it uses the default
	// one; swapping it is how the test server's certificate becomes trusted.
	previous := http.DefaultTransport
	http.DefaultTransport = secure.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	dest := filepath.Join(t.TempDir(), "archive")
	err := downloadFile(shortContext(t), secure.URL+"/qg.tar.gz", dest)
	if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
		t.Fatalf("downloadFile across an https to http redirect = %v, want an insecure-redirect refusal", err)
	}
	if data, readErr := os.ReadFile(dest); readErr == nil {
		t.Fatalf("the plaintext hop was downloaded: %q", data)
	}
}

// Pins that an https-to-http downgrade redirect is refused for metadata.
func TestFetchReleaseRefusesARedirectFromHTTPSToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/latest", http.StatusFound)
	}))
	defer secure.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = secure.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	release, err := fetchRelease(shortContext(t), secure.URL+"/latest")
	if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
		t.Fatalf("fetchRelease across an https to http redirect = %+v, %v; want an insecure-redirect refusal", release, err)
	}
}

// Pins that a redirect from a loopback URL to a remote plain-http URL is refused.
func TestDownloadFileRefusesARedirectFromLoopbackToRemoteHTTP(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.test/qg.tar.gz", http.StatusFound)
	}))
	defer local.Close()

	err := downloadFile(shortContext(t), local.URL+"/qg.tar.gz", filepath.Join(t.TempDir(), "archive"))
	if err == nil || !strings.Contains(err.Error(), insecureUpdateURLMarker) {
		t.Fatalf("downloadFile redirected to remote plain http = %v, want an insecure-URL refusal", err)
	}
}

// Pins that https-to-https redirects keep being followed for downloads.
func TestDownloadFileStillFollowsAnHTTPSRedirect(t *testing.T) {
	var secure *httptest.Server
	secure = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, secure.URL+"/final", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer secure.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = secure.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	dest := filepath.Join(t.TempDir(), "archive")
	if err := downloadFile(shortContext(t), secure.URL+"/start", dest); err != nil {
		t.Fatalf("downloadFile across an https redirect: %v", err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "payload" {
		t.Fatalf("downloaded %q, %v; want the redirected payload", data, err)
	}
}
