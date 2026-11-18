package cli

import (
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

// Default batch cloud models (§8). Overridable via stt.model.
const (
	defaultGroqSTTModel   = "whisper-large-v3-turbo"
	defaultOpenAISTTModel = "whisper-1"
	groqSTTBaseURL        = "https://api.groq.com/openai/v1"
)

// newDictationTranscriberFactory returns the closure the TUI calls to build a
// transcriber when a recording starts. It lives here, not in the TUI, because it
// resolves provider API keys (credstore + env + configured providers) — logic
// that belongs with the rest of the CLI's credential handling.
//
// preferStreaming asks for the streaming backend; the returned bool reports
// whether streaming was actually built (false → the caller uses the batch
// pipeline). A misconfigured streaming backend surfaces its own SetupError.
func newDictationTranscriberFactory(resolved config.ResolvedConfig, userConfigPath string) func(config.STTConfig, bool) (tui.Transcriber, bool, error) {
	return func(stt config.STTConfig, preferStreaming bool) (tui.Transcriber, bool, error) {
		if preferStreaming {
			t, err := buildStreamingTranscriber(stt, resolved, userConfigPath)
			switch {
			case err == nil && t != nil:
				return t, true, nil
			case err != nil && stt.STTStreamProvider() != config.STTProviderLocal:
				// An explicitly-chosen cloud streaming backend that failed to build
				// (e.g. a missing key) should surface its own error, not silently
				// fall back to batch — the user asked for that backend specifically.
				return nil, true, err
			}
			// The default local streaming backend isn't set up: fall through to the
			// batch pipeline so a cloud-batch provider (or a batch-only setup) still
			// works instead of erroring on a local setup the user never asked for.
		}
		t, err := buildBatchTranscriber(stt, resolved, userConfigPath)
		return t, false, err
	}
}

// buildStreamingTranscriber constructs the configured streaming backend, or
// returns (nil, nil) when streaming is not applicable so the caller falls back
// to batch.
func buildStreamingTranscriber(stt config.STTConfig, resolved config.ResolvedConfig, userConfigPath string) (dictation.Transcriber, error) {
	switch stt.STTStreamProvider() {
	case config.STTProviderDeepgram:
		key := resolveSTTKey(resolved, userConfigPath, dictation.StreamProviderDeepgram, "DEEPGRAM_API_KEY")
		return dictation.NewDeepgramTranscriber(dictation.DeepgramConfig{
			APIKey:   key,
			Model:    stt.StreamModel,
			Language: stt.Language,
		})
	case config.STTProviderOpenAI:
		key := resolveSTTKey(resolved, userConfigPath, dictation.ProviderOpenAI, "OPENAI_API_KEY")
		return dictation.NewOpenAIRealtimeTranscriber(dictation.OpenAIRealtimeConfig{
			APIKey: key,
			Model:  stt.StreamModel,
		})
	default: // local live transcription via rolling file execution (no network server)
		// STTStreamProvider() defaults to "local" whenever streamProvider is unset —
		// including when the user picked a CLOUD batch provider (Groq/OpenAI) and
		// never touched streaming. Don't silently hijack that with local rolling:
		// local live transcription is only appropriate when it was explicitly
		// requested (streamProvider: "local") OR the batch provider is itself local.
		// Otherwise report "not applicable" so the factory falls back to the cloud
		// batch pipeline the user actually chose.
		localWanted := stt.StreamProvider == config.STTProviderLocal || stt.STTProvider() == config.STTProviderLocal
		if !localWanted {
			return nil, nil
		}
		if strings.TrimSpace(stt.LocalModelPath) == "" {
			return nil, nil
		}
		// Both batch and streaming local models run file-based decoding wrapped in
		// rollingTranscriber (sherpa-onnx-offline for batch models, sherpa-onnx for
		// streaming models), so no local model ever starts a network server.
		inner, err := newLocalFileTranscriber(stt)
		if err != nil {
			return nil, err
		}
		return dictation.NewRollingTranscriber(inner, dictation.RollingOptions{}), nil
	}
}

// buildBatchTranscriber constructs the configured batch transcriber, resolving
// credentials for the cloud providers.
func buildBatchTranscriber(stt config.STTConfig, resolved config.ResolvedConfig, userConfigPath string) (dictation.Transcriber, error) {
	switch stt.STTProvider() {
	case config.STTProviderGroq:
		key := resolveSTTKey(resolved, userConfigPath, dictation.ProviderGroq, "GROQ_API_KEY")
		model := stt.Model
		if model == "" {
			model = defaultGroqSTTModel
		}
		return dictation.NewCloudTranscriber(dictation.CloudConfig{
			Provider: dictation.ProviderGroq,
			BaseURL:  groqSTTBaseURL,
			APIKey:   key,
			Model:    model,
			Language: stt.Language,
		})
	case config.STTProviderOpenAI:
		key := resolveSTTKey(resolved, userConfigPath, dictation.ProviderOpenAI, "OPENAI_API_KEY")
		model := stt.Model
		if model == "" {
			model = defaultOpenAISTTModel
		}
		return dictation.NewCloudTranscriber(dictation.CloudConfig{
			Provider: dictation.ProviderOpenAI,
			BaseURL:  config.OpenAIBaseURL,
			APIKey:   key,
			Model:    model,
			Language: stt.Language,
		})
	default: // local
		return newLocalFileTranscriber(stt)
	}
}

// newLocalFileTranscriber builds the file-based transcriber for a local model
// directory. Streaming models run sherpa-onnx (online file decoder); batch
// models run sherpa-onnx-offline. Shared by the batch pipeline and the rolling
// live path.
func newLocalFileTranscriber(stt config.STTConfig) (dictation.Transcriber, error) {
	online := dictation.StreamingModelDir(stt.LocalModelPath)
	binary := stt.LocalBinary
	if online {
		binary = resolveLocalOnlineBinary(stt)
	}
	return dictation.NewLocalTranscriber(dictation.LocalConfig{
		Binary:     binary,
		ModelPath:  stt.LocalModelPath,
		NumThreads: stt.NumThreads,
		Online:     online,
	})
}

// resolveLocalOnlineBinary picks the executable for streaming/online models
// (sherpa-onnx). An explicit stt.localOnlineBinary wins; otherwise, if
// stt.localBinary is a path (e.g. from an earlier engine download before
// localOnlineBinary existed) and a sibling sherpa-onnx binary lives next to it,
// use that sibling. Empty falls back to "sherpa-onnx" on PATH.
func resolveLocalOnlineBinary(stt config.STTConfig) string {
	if b := strings.TrimSpace(stt.LocalOnlineBinary); b != "" {
		return b
	}
	if bin := strings.TrimSpace(stt.LocalBinary); bin != "" && strings.ContainsAny(bin, `/\`) {
		name := "sherpa-onnx"
		if strings.EqualFold(filepath.Ext(bin), ".exe") {
			name += ".exe"
		}
		candidate := filepath.Join(filepath.Dir(bin), name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// sttEnvVar returns the conventional environment variable a cloud STT provider's
// key is read from.
func sttEnvVar(provider string) string {
	switch provider {
	case dictation.ProviderGroq:
		return "GROQ_API_KEY"
	case dictation.ProviderOpenAI: // == StreamProviderOpenAI ("openai")
		return "OPENAI_API_KEY"
	case dictation.StreamProviderDeepgram:
		return "DEEPGRAM_API_KEY"
	}
	return ""
}

// newSTTKeyStatus returns a predicate reporting whether a cloud STT provider
// already has a resolvable API key (configured provider, credstore, or env).
func newSTTKeyStatus(resolved config.ResolvedConfig, userConfigPath string) func(string) bool {
	return func(provider string) bool {
		return resolveSTTKey(resolved, userConfigPath, provider, sttEnvVar(provider)) != ""
	}
}

// newSaveSTTKey returns a function that stores a cloud STT provider's API key in
// the encrypted credential store, so the inline prompt can persist it.
func newSaveSTTKey(userConfigPath string) func(string, string) error {
	return func(provider, key string) error {
		if strings.TrimSpace(userConfigPath) == "" {
			return fmt.Errorf("no config path to store the key")
		}
		store, err := config.ProviderKeyStoreAt(filepath.Dir(userConfigPath))
		if err != nil {
			return err
		}
		return store.Set(provider, strings.TrimSpace(key))
	}
}

// resolveSTTKey finds an API key for an STT cloud provider, reusing QG's
// existing credentials (§2 — "qg new credential UI"). Precedence: a matching
// configured provider's resolved key, then the encrypted credential store, then
// the provider's conventional environment variable.
func resolveSTTKey(resolved config.ResolvedConfig, userConfigPath, provider, envVar string) string {
	if key := keyFromConfiguredProviders(resolved, provider); key != "" {
		return key
	}
	if userConfigPath != "" {
		if store, err := config.ProviderKeyStoreAt(filepath.Dir(userConfigPath)); err == nil {
			if key, ok, _ := store.Get(provider); ok && strings.TrimSpace(key) != "" {
				return strings.TrimSpace(key)
			}
		}
	}
	return strings.TrimSpace(os.Getenv(envVar))
}

// keyFromConfiguredProviders returns the resolved API key of a configured
// provider that belongs to the given cloud provider (matched by catalog id,
// provider name/kind, or base-URL host), or "" when none is configured.
func keyFromConfiguredProviders(resolved config.ResolvedConfig, provider string) string {
	provider = strings.ToLower(provider)
	profiles := append([]config.ProviderProfile{resolved.Provider}, resolved.Providers...)
	for _, p := range profiles {
		if strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		if providerMatches(p, provider) {
			return strings.TrimSpace(p.APIKey)
		}
	}
	return ""
}

func providerMatches(p config.ProviderProfile, provider string) bool {
	provider = strings.ToLower(provider)
	// Match on stable identifiers (catalog id, provider name/kind, profile
	// name) rather than raw BaseURL substring matching: a Groq profile's
	// BaseURL (api.groq.com/openai/v1) contains "openai", so a substring
	// check can hand the wrong key to an OpenAI STT lookup and fail auth.
	for _, f := range []string{
		strings.ToLower(p.CatalogID),
		strings.ToLower(p.Provider),
		strings.ToLower(string(p.ProviderKind)),
		strings.ToLower(p.Name),
	} {
		if f != "" && strings.Contains(f, provider) {
			return true
		}
	}
	// BaseURL: compare the parsed host only, not a raw substring on the
	// full URL (which lets "openai" match api.groq.com/openai/v1).
	if u, err := neturl.Parse(p.BaseURL); err == nil && u.Host != "" {
		if host := strings.ToLower(u.Host); strings.Contains(host, provider) {
			return true
		}
	}
	return false
}
