package dictation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanguageFromLocale(t *testing.T) {
	cases := []struct{ locale, want string }{
		{"pt_BR.UTF-8", "pt"},
		{"en_US", "en"},
		{"pt-BR", "pt"},
		{"pt_PT@euro", "pt"},
		{"zh-Hans-CN", "zh"},
		{"C", ""},
		{"POSIX", ""},
		{"", ""},
		{"x", ""},
	}
	for _, tc := range cases {
		if got := LanguageFromLocale(tc.locale); got != tc.want {
			t.Errorf("LanguageFromLocale(%q) = %q, want %q", tc.locale, got, tc.want)
		}
	}
}

func TestDictationLanguageConfigured(t *testing.T) {
	if got := DictationLanguage("es"); got != "es" {
		t.Errorf("an explicit stt.language should win, got %q", got)
	}
}

func TestLanguageRank(t *testing.T) {
	pt := ModelVariant{Language: "pt"}
	en := ModelVariant{Language: "en"}
	multi := ModelVariant{} // multilingual
	cases := []struct {
		v    ModelVariant
		lang string
		want int
	}{
		{pt, "pt", 0},
		{multi, "pt", 1},
		{en, "pt", 2},
		{en, "", 1},
		{multi, "", 1},
	}
	for _, tc := range cases {
		if got := LanguageRank(tc.v, tc.lang); got != tc.want {
			t.Errorf("LanguageRank(%q, %q) = %d, want %d", tc.v.Language, tc.lang, got, tc.want)
		}
	}
	// Over the curated list: every English variant ranks first for an English
	// user, while the multilingual ones (SenseVoice, Whisper) rank next; for a
	// Portuguese user no curated variant ranks first (today's truth — which
	// Portuguese-capable model becomes curated is still undecided).
	for _, v := range ModelVariants() {
		switch v.Language {
		case "en":
			if got := LanguageRank(v, "en"); got != 0 {
				t.Errorf("LanguageRank(en, en) = %d, want 0", got)
			}
		default:
			if got := LanguageRank(v, "en"); got != 1 {
				t.Errorf("LanguageRank(%q, en) = %d, want 1", v.Language, got)
			}
		}
		if got := LanguageRank(v, "pt"); got == 0 {
			t.Errorf("LanguageRank(%q, pt) = 0, want no curated variant first for pt", v.Language)
		}
	}
}

func TestCuratedLanguageInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range ModelVariants() {
		if seen[v.AssetName] {
			t.Errorf("duplicate curated asset %q", v.AssetName)
		}
		seen[v.AssetName] = true
		if v.Language != "" {
			if len(v.Language) != 2 || strings.ToLower(v.Language) != v.Language {
				t.Errorf("variant %q has Language %q, want \"\" or two lowercase letters", v.ID, v.Language)
			}
			for _, c := range v.Language {
				if c < 'a' || c > 'z' {
					t.Errorf("variant %q has Language %q, want \"\" or two lowercase letters", v.ID, v.Language)
				}
			}
		}
		// DirName follows the rule from the ModelVariants comment: "model-"
		// plus the asset id (asset minus prefix and suffix).
		id := strings.TrimSuffix(strings.TrimPrefix(v.AssetName, "sherpa-onnx-"), ".tar.bz2")
		if want := "model-" + id; v.DirName != want {
			t.Errorf("variant %q has DirName %q, want %q", v.ID, v.DirName, want)
		}
	}
}

func TestNoHintMentionsMissingDoc(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "docs/dictation.md") {
			t.Errorf("%s still points at docs/dictation.md, which does not exist — send users to /stt-model instead", name)
		}
	}
}
