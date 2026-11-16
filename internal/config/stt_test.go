package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSetSTTLocalEngineResetsStreamProvider(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	// Pre-seed a cloud streaming provider — the situation the fix guards against.
	if err := os.WriteFile(path, []byte(`{"stt":{"streamProvider":"deepgram"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := SetSTTLocalEngine(path, "sherpa-onnx-offline", "sherpa-onnx-online-websocket-server", "/models/kroko", true)
	if err != nil {
		t.Fatalf("SetSTTLocalEngine: %v", err)
	}
	// Switching to a local model must point BOTH pipelines at the local engine, or
	// the leftover cloud streamProvider keeps the live transcript on the cloud.
	if cfg.STT.Provider != STTProviderLocal || cfg.STT.StreamProvider != STTProviderLocal {
		t.Errorf("both providers should be local, got provider=%q streamProvider=%q", cfg.STT.Provider, cfg.STT.StreamProvider)
	}
}

func TestValidateSTTConfigRejectsUnknownProvider(t *testing.T) {
	_, issues := ValidateBytes([]byte(`{"stt":{"provider":"assemblyai"}}`))
	if len(issues) == 0 {
		t.Fatal("expected an issue for unknown stt.provider")
	}
	if !strings.Contains(issues[0].Message, "assemblyai") || !strings.Contains(issues[0].Message, "local, groq, or openai") {
		t.Errorf("issue message = %q, want it to name the bad value and valid options", issues[0].Message)
	}
}

func TestValidateSTTConfigRejectsUnknownStreamProvider(t *testing.T) {
	_, issues := ValidateBytes([]byte(`{"stt":{"streamProvider":"groq"}}`))
	if len(issues) == 0 {
		t.Fatal("expected an issue: groq has no streaming product, not a valid streamProvider")
	}
	if !strings.Contains(issues[0].Message, "deepgram") {
		t.Errorf("issue message = %q", issues[0].Message)
	}
}

func TestValidateSTTConfigAcceptsValid(t *testing.T) {
	_, issues := ValidateBytes([]byte(`{"stt":{"provider":"groq","streamProvider":"deepgram","maxDurationSeconds":120}}`))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
}

func TestValidateSTTConfigRejectsNegativeDuration(t *testing.T) {
	_, issues := ValidateBytes([]byte(`{"stt":{"maxDurationSeconds":-5}}`))
	if len(issues) == 0 {
		t.Fatal("expected an issue for negative maxDurationSeconds")
	}
}

func TestSTTConfigDefaults(t *testing.T) {
	cfg := STTConfig{}
	if cfg.STTProvider() != STTProviderLocal {
		t.Errorf("default provider = %q, want local", cfg.STTProvider())
	}
	if cfg.STTStreamProvider() != STTProviderLocal {
		t.Errorf("default streamProvider = %q, want local", cfg.STTStreamProvider())
	}
	if !cfg.StreamingEnabled() {
		t.Error("streaming should default on")
	}
	if !cfg.SilenceAutoStopEnabled() {
		t.Error("silence auto-stop should default on")
	}
	if cfg.AutoSubmitEnabled() {
		t.Error("auto-submit should default off (insert-for-review is the safety net)")
	}
	if !cfg.Empty() {
		t.Error("zero-value STTConfig should be Empty")
	}
}

func TestSTTConfigTriStateBools(t *testing.T) {
	cfg, issues := ValidateBytes([]byte(`{"stt":{"autoSubmit":true,"streaming":false,"silenceAutoStop":false}}`))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	if !cfg.STT.AutoSubmitEnabled() {
		t.Error("autoSubmit:true should be enabled")
	}
	if cfg.STT.StreamingEnabled() {
		t.Error("streaming:false should be disabled")
	}
	if cfg.STT.SilenceAutoStopEnabled() {
		t.Error("silenceAutoStop:false should be disabled")
	}
}

func TestSTTConfigRoundTripsThroughMarshal(t *testing.T) {
	cfg, _ := ValidateBytes([]byte(`{"stt":{"provider":"groq","model":"whisper-large-v3-turbo","localServerPort":6007}}`))
	data, err := cfg.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"stt"`) {
		t.Errorf("marshaled config dropped stt section: %s", data)
	}
	reparsed, issues := ValidateBytes(data)
	if len(issues) != 0 {
		t.Fatalf("re-parse issues: %+v", issues)
	}
	if reparsed.STT.Provider != STTProviderGroq || reparsed.STT.Model != "whisper-large-v3-turbo" || reparsed.STT.LocalServerPort != 6007 {
		t.Errorf("round-trip lost fields: %+v", reparsed.STT)
	}
}

func TestSetSTTModelPersists(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	// Cloud provider stores under stt.model.
	if _, err := SetSTTModel(path, STTProviderGroq, "whisper-large-v3-turbo"); err != nil {
		t.Fatalf("SetSTTModel: %v", err)
	}
	cfg, issues := ValidateFile(path)
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	if cfg.STT.Provider != STTProviderGroq || cfg.STT.Model != "whisper-large-v3-turbo" {
		t.Errorf("persisted STT = %+v", cfg.STT)
	}
	// Local provider stores under stt.localModelPath.
	if _, err := SetSTTModel(path, STTProviderLocal, "/models/moonshine"); err != nil {
		t.Fatalf("SetSTTModel local: %v", err)
	}
	cfg, _ = ValidateFile(path)
	if cfg.STT.LocalModelPath != "/models/moonshine" {
		t.Errorf("local model path = %q", cfg.STT.LocalModelPath)
	}
}

func TestSetSTTVoiceModeKeepsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	if err := os.WriteFile(path, []byte(`{"providers":[{"name":"main","provider_kind":"openai","model":"gpt-4.1"}],"preferences":{"theme":"dark"},"stt":{"provider":"groq"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSTTVoiceMode(path, true); err != nil {
		t.Fatalf("SetSTTVoiceMode(true): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"voiceMode"`) {
		t.Errorf("enabled config should carry voiceMode:true, got %s", data)
	}
	cfg, issues := ValidateFile(path)
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	if !cfg.STT.VoiceMode {
		t.Error("resolved STT.VoiceMode should be true after enabling")
	}
	if cfg.Preferences.Theme != "dark" || cfg.STT.Provider != STTProviderGroq {
		t.Errorf("other keys must stay intact, got theme=%q provider=%q", cfg.Preferences.Theme, cfg.STT.Provider)
	}
	if _, err := SetSTTVoiceMode(path, false); err != nil {
		t.Fatalf("SetSTTVoiceMode(false): %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "voiceMode") {
		t.Errorf("disabled config should drop the voiceMode key, got %s", data)
	}
	cfg, issues = ValidateFile(path)
	if len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	if cfg.STT.VoiceMode {
		t.Error("resolved STT.VoiceMode should be false after disabling")
	}
	if cfg.Preferences.Theme != "dark" || cfg.STT.Provider != STTProviderGroq {
		t.Errorf("other keys must stay intact, got theme=%q provider=%q", cfg.Preferences.Theme, cfg.STT.Provider)
	}
}

func TestSetSTTVoiceModeFromScratch(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	if _, err := SetSTTVoiceMode(path, true); err != nil {
		t.Fatalf("SetSTTVoiceMode(true): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"voiceMode"`) {
		t.Errorf("fresh config should carry stt.voiceMode:true, got %s", data)
	}
	if _, err := SetSTTVoiceMode(path, false); err != nil {
		t.Fatalf("SetSTTVoiceMode(false): %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"stt"`) {
		t.Errorf("clearing the only stt setting should leave no stt object, got %s", data)
	}
}

func TestSetSTTVoiceModeRequiresPath(t *testing.T) {
	if _, err := SetSTTVoiceMode("   ", true); err == nil {
		t.Error("empty config path should return an error")
	}
}

func TestResolveVoiceMode(t *testing.T) {
	userPath := writeConfig(t, `{"stt":{"voiceMode":true}}`)
	resolved, err := Resolve(ResolveOptions{UserConfigPath: userPath, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolved.STT.VoiceMode {
		t.Error("voiceMode:true in user config should resolve to true")
	}
	absentPath := writeConfig(t, `{"preferences":{"theme":"dark"}}`)
	resolved, err = Resolve(ResolveOptions{UserConfigPath: absentPath, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.STT.VoiceMode {
		t.Error("absent voiceMode should resolve to false")
	}
}

func TestSTTVoiceModeEmpty(t *testing.T) {
	if (STTConfig{VoiceMode: true}).Empty() {
		t.Error("STTConfig{VoiceMode:true} must not be Empty, or it would never be written")
	}
	if !(STTConfig{}).Empty() {
		t.Error("zero-value STTConfig should still be Empty")
	}
}

func TestSTTVoiceModeRoundTrip(t *testing.T) {
	cfg, issues := ValidateBytes([]byte(`{"stt":{"voiceMode":true}}`))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	if !cfg.STT.VoiceMode {
		t.Fatal("parsed STT.VoiceMode should be true")
	}
	data, err := cfg.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"voiceMode":true`) {
		t.Errorf("marshaled config dropped voiceMode: %s", data)
	}
	var reparsed FileConfig
	if err := json.Unmarshal(data, &reparsed); err != nil {
		t.Fatal(err)
	}
	if !reparsed.STT.VoiceMode {
		t.Error("round trip lost STT.VoiceMode")
	}
}

func TestSTTVoiceModeIsKnownField(t *testing.T) {
	_, issues := ValidateBytes([]byte(`{"stt":{"provider":"groq","voiceMode":true}}`))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	for _, issue := range issues {
		if strings.Contains(issue.FieldPath, "voiceMode") {
			t.Errorf("stt.voiceMode flagged as unknown: %+v", issue)
		}
	}
}
