// Package browser opens http(s) URLs in the user's default browser.
package browser

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// runCommand is a variable so tests can replace the real process launch.
var runCommand = func(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// Open launches rawURL in the default browser. Only http and https URLs are
// accepted so that a crafted value cannot reach a local file or another handler.
func Open(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("browser: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("browser: unsupported URL scheme %q", u.Scheme)
	}
	name, args := commandFor(runtime.GOOS, rawURL)
	if err := runCommand(ctx, name, args...); err != nil {
		return fmt.Errorf("browser: open %s: %w", name, err)
	}
	return nil
}

func commandFor(goos, rawURL string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{rawURL}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		return "xdg-open", []string{rawURL}
	}
}
