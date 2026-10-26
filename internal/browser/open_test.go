package browser

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCommandForPlatforms(t *testing.T) {
	const u = "https://example.com/device"
	tests := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{"darwin", "open", []string{u}},
		{"linux", "xdg-open", []string{u}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", u}},
	}
	for _, tt := range tests {
		name, args := commandFor(tt.goos, u)
		if name != tt.wantName || !reflect.DeepEqual(args, tt.wantArgs) {
			t.Errorf("commandFor(%q) = %q %v, want %q %v", tt.goos, name, args, tt.wantName, tt.wantArgs)
		}
	}
}

func stubRunCommand(t *testing.T, fn func(ctx context.Context, name string, args ...string) error) {
	t.Helper()
	orig := runCommand
	runCommand = fn
	t.Cleanup(func() { runCommand = orig })
}

func TestOpenRejectsNonHTTPURL(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "", "javascript:alert(1)"} {
		called := false
		stubRunCommand(t, func(context.Context, string, ...string) error {
			called = true
			return nil
		})
		if err := Open(context.Background(), raw); err == nil {
			t.Errorf("Open(%q) returned nil error, want error", raw)
		}
		if called {
			t.Errorf("Open(%q) invoked the command runner", raw)
		}
	}
}

func TestOpenPropagatesRunError(t *testing.T) {
	want := errors.New("no opener")
	stubRunCommand(t, func(context.Context, string, ...string) error { return want })
	if err := Open(context.Background(), "https://example.com"); !errors.Is(err, want) {
		t.Errorf("Open error = %v, want wrapping %v", err, want)
	}
}
