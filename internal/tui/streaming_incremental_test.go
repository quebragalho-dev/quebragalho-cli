package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// streamingIncrementalCorpus covers the block shapes whose rendering can
// change as a stream grows: open/closed fences, lists split by blank lines,
// tables that gain rows, quotes, headings, rules, inline spans, wide runes
// and stray carriage returns.
func streamingIncrementalCorpus() []string {
	fence := "\x60\x60\x60"
	return []string{
		"",
		"\n\n",
		"Hello world, this is a simple paragraph that wraps across lines\nand continues here with more words to force several wraps.",
		"First paragraph with enough words to wrap at narrow measures several times over.\n\nSecond paragraph after a blank line, also long enough to wrap a few times here.",
		"Here is the script:\n" + fence + "python\nfrom datetime import datetime\nprint(datetime.now())",
		"Intro prose before the block.\n\n" + fence + "go\npackage main\n\nfunc main() {}\n" + fence + "\n\nTrailing prose after the closed fence with **bold**.",
		"~~~text\ntilde fenced block\nstill inside\n~~~\n\nafter tilde fence",
		"- alpha\n- beta\n\n- gamma\n\n1. one\n2. two\n\n- delta",
		"| Name | Age |\n|------|-----|\n| Ana | 30 |\n| Bo | 7 |\n\nDone **bold**.",
		"# Title\n\n> quote line one\n> quote line two\n\n## Sub\n\n---\n\nAfter rule.",
		"See [docs](https://example.com/x) and `code` plus **bold** and *em* plus __under__.",
		"Olá mundo 🌍🚀\n\n日本語テスト mixed with emoji 🎉 and **bold**\n\nEnd — café naïve façade.",
		"line one\r\nline two\rline three\r\n\r\n" + fence + "go\r\npackage main\r\n" + fence + "\r\ntail",
		"from helpers import util\nprint(util.run())\nresult = util.run()\n\nThe answer above shows **prose** now.",
		"**bold without end and `code without end\n\nmore *stars",
		"Intro with a decent amount of prose to push the incremental path over several blocks and lines.\n\n- first item in a list\n- second item\n\n" + fence + "python\ndef f():\n    return 1\n" + fence + "\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n> a closing quote\n\nOutro paragraph at the very end.",
	}
}

// runeAlignedPrefixes returns every ~step bytes of text, snapped forward to a
// rune boundary so prefixes are always valid UTF-8, plus the full text.
func runeAlignedPrefixes(text string, step int) []string {
	out := []string{""}
	for offset := step; offset < len(text); offset += step {
		for offset < len(text) && !utf8.RuneStart(text[offset]) {
			offset++
		}
		out = append(out, text[:offset])
	}
	return append(out, text)
}

func TestStreamingIncrementalMatchesIntegralOnEveryPrefix(t *testing.T) {
	for docIndex, doc := range streamingIncrementalCorpus() {
		resetStreamingMarkdownCache()
		prefixes := runeAlignedPrefixes(doc, 4)
		for prefixIndex, prefix := range prefixes {
			prose, table := 80, 80
			if prefixIndex > len(prefixes)/2 {
				prose, table = 50, 50
			}
			got := strings.Join(renderStreamingAssistantMarkdownText(prefix, prose, table), "\n")
			want := strings.Join(renderAssistantMarkdownText(streamingMarkdownStablePrefix(prefix), prose, table, true), "\n")
			if got != want {
				t.Fatalf("doc %d prefix %d (%q, %dx%d): incremental differs from integral\ngot:  %q\nwant: %q",
					docIndex, prefixIndex, prefix, prose, table, got, want)
			}
		}
	}
}

func TestStreamingIncrementalSurvivesThemeChange(t *testing.T) {
	previous := qgTheme
	defer func() {
		qgTheme = previous
		rebuildStreamingFadePalette()
		resetStreamingMarkdownCache()
	}()

	resetStreamingMarkdownCache()
	text := "# Title\n\nSome **bold** prose.\n\n" + "\x60\x60\x60go\npackage main\n\x60\x60\x60\n\nTail."
	before := strings.Join(renderStreamingAssistantMarkdownText(text, 80, 80), "\n")

	applyTheme("nord", true)
	after := strings.Join(renderStreamingAssistantMarkdownText(text, 80, 80), "\n")
	want := strings.Join(renderAssistantMarkdownText(streamingMarkdownStablePrefix(text), 80, 80, true), "\n")
	if after != want {
		t.Fatalf("after theme change: got %q, want %q", after, want)
	}
	if strings.Contains(before, "\x1b[") && before == after {
		t.Fatalf("theme change should repaint styled output, got identical bytes")
	}
}

// TestStreamingRenderDoesNotChurnSharedCache pins the perf fix: repeated
// streaming frames must not insert a new giant entry per frame into the
// shared LRU (the old sha-of-whole-text key missed on every frame while the
// text grew).
func TestStreamingRenderDoesNotChurnSharedCache(t *testing.T) {
	cache := replaceDefaultRenderCache(t, newStaticRenderCache(8, 1000))
	resetStreamingMarkdownCache()
	text := "Here is the script:\n\x60\x60\x60go\nfunc main() {}\n\x60\x60\x60\nDone."
	first := renderStreamingAssistantMarkdownText(text, 90, 90)
	before := cache.stats()
	second := renderStreamingAssistantMarkdownText(text, 90, 90)
	after := cache.stats()
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Fatalf("repeat render differs:\n%q\n%q", first, second)
	}
	if after != before {
		t.Fatalf("shared cache stats changed on repeat streaming render: before=%+v after=%+v", before, after)
	}
}
