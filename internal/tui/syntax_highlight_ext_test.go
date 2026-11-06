package tui

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func lexerDisplayName(t *testing.T, lexer chroma.Lexer) string {
	t.Helper()
	if lexer == nil {
		return "<nil>"
	}
	return lexer.Config().Name
}

// cachedLexerForPath must resolve exactly what lexers.Match returns for every
// name: distinct files sharing an extension skip the registry scan, while
// exact names (Makefile), prefix globs (Dockerfile.*), double extensions
// (.tar.gz) and case variants (FOO.GO) keep today's outcome.
func TestCachedLexerForPathMatchesChromaMatch(t *testing.T) {
	names := []string{
		"handler_1.go", "handler_2.go", "Dockerfile.go", "Makefile.go",
		"Caddyfile.go", "Containerfile.go", "a.go", "FOO.GO", "foo.GO",
		"foo.c", "foo.C", "foo.h", "v.mod", "go.mod", "foo.mod",
		"Makefile", "Dockerfile", "foo.Dockerfile", "MAKEFILE", "DOCKERFILE",
		".bashrc", "foo.bashrc", ".zshrc", "foo.zshrc",
		"foo.d.ts", "foo.test.ts", "foo.ts",
		"foo.blade.php", "foo.php", "foo.php3", "foo.php4", "foo.php5",
		"foo.inc", "foo.pl", "foo.pm", "foo.t", "foo.m",
		"foo.s", "foo.S", "foo.asm", "foo.ASM",
		"foo.tar.gz", "foo.go.tmpl", "foo.tmpl",
		"foo.json.jbuilder", "foo.xml.erb", "foo.html.erb",
		"noext", "foo", ".env", "foo.env", "APKBUILD",
		"foo.py", "SConstruct", "foo.star",
		"Dockerfile.dev", "foo.dev", "Caddyfile",
		"foo.txt", "CMakeLists.txt", "foo.cmake",
		"package.json", "foo.json", "foo.yaml", "foo.toml",
		"foo.ini", "foo.cfg", "foo.service", "foo.socket", "foo.network",
		"Dockerfile.bak", "foo.go.bak", "foo.bak", "foo.go.in", "foo.in",
		"internal/server/handler_9.go", "./relative/path/main.py",
		"  padded.go  ", "foo.", ".hidden",
	}
	for _, name := range names {
		want := lexerDisplayName(t, lexers.Match(strings.TrimSpace(name)))
		got := lexerDisplayName(t, cachedLexerForPath(name))
		if got != want {
			t.Errorf("cachedLexerForPath(%q) = %s, want %s (lexers.Match)", name, got, want)
		}
	}
}

// Every registry glob contributes one example filename, so future chroma
// updates that add or reshape globs fail here instead of silently diverging.
func TestCachedLexerForPathMatchesEveryRegistryGlob(t *testing.T) {
	for _, lexer := range lexers.GlobalLexerRegistry.Lexers {
		for _, glob := range lexer.Config().Filenames {
			name := exampleNameForGlob(glob)
			want := lexerDisplayName(t, lexers.Match(name))
			got := lexerDisplayName(t, cachedLexerForPath(name))
			if got != want {
				t.Errorf("cachedLexerForPath(%q) = %s, want %s (glob %q of %s)",
					name, got, want, glob, lexer.Config().Name)
			}
		}
	}
}

// exampleNameForGlob builds a filename that tries to match glob by replacing
// each wildcard with a representative character.
func exampleNameForGlob(glob string) string {
	var builder strings.Builder
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '\\':
			if i+1 < len(glob) {
				i++
				builder.WriteByte(glob[i])
			}
		case '*':
			builder.WriteString("example")
		case '?':
			builder.WriteByte('q')
		case '[':
			j := i + 1
			if j < len(glob) && glob[j] == '^' {
				j++
			}
			picked := byte('a')
			if j < len(glob) && glob[j] != ']' {
				picked = glob[j]
				if picked == '\\' && j+1 < len(glob) {
					picked = glob[j+1]
				}
			}
			builder.WriteByte(picked)
			for j < len(glob) && glob[j] != ']' {
				j++
			}
			i = j
		default:
			builder.WriteByte(glob[i])
		}
	}
	if builder.Len() == 0 {
		return "example"
	}
	return builder.String()
}

// Distinct basenames sharing an extension must not grow the per-basename slow
// cache: the first file already proves the extension path never scans.
func TestCachedLexerForPathDistinctFilesSkipSlowScan(t *testing.T) {
	const files = 50
	prefix := "costprobeext7_"
	lexerCacheMu.RLock()
	before := countCacheKeysWithPrefix(prefix)
	lexerCacheMu.RUnlock()
	for i := 0; i < files; i++ {
		name := prefix + strings.Repeat("x", 8) + itoa(i) + ".go"
		if got := cachedLexerForPath(name); got == nil || got.Config().Name != "Go" {
			t.Fatalf("cachedLexerForPath(%q) should be Go", name)
		}
	}
	lexerCacheMu.RLock()
	after := countCacheKeysWithPrefix(prefix)
	lexerCacheMu.RUnlock()
	if after != before {
		t.Errorf("resolving %d distinct .go files added %d per-basename cache entries, want none",
			files, after-before)
	}
	// A basename matching a competitor glob still takes the slow path once.
	if got := cachedLexerForPath("Dockerfile.go"); got == nil || got.Config().Name != "Docker" {
		t.Fatalf("cachedLexerForPath(%q) should be Docker, got %v", "Dockerfile.go", got)
	}
	lexerCacheMu.RLock()
	_, slowCached := lexerCache["path:Dockerfile.go"]
	lexerCacheMu.RUnlock()
	if !slowCached {
		t.Error("special basename should be cached by the slow path")
	}
}

func countCacheKeysWithPrefix(prefix string) int {
	n := 0
	for key := range lexerCache {
		if strings.HasPrefix(key, "path:"+prefix) {
			n++
		}
	}
	return n
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
