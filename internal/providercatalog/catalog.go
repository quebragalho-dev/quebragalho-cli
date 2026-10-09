package providercatalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

type Transport string

const (
	TransportOpenAI              Transport = "openai"
	TransportAnthropic           Transport = "anthropic"
	TransportGoogle              Transport = "google"
	TransportBedrock             Transport = "bedrock"
	TransportVertex              Transport = "vertex"
	TransportOpenAICompatible    Transport = "openai-compatible"
	TransportOpenAICompat        Transport = TransportOpenAICompatible
	TransportAnthropicCompatible Transport = "anthropic-compatible"
)

type APIFormat string

const (
	APIFormatOpenAIChatCompletions APIFormat = "chat-completions"
	APIFormatBedrockConverse       APIFormat = "bedrock-converse"
	APIFormatVertexGenerateContent APIFormat = "vertex-generate-content"
)

var ErrUnknownProvider = errors.New("unknown provider")

// QGID is the single provider catalog identifier. Etapa 2 removes every
// multi-provider preset: qg is the only entry.
const QGID = "qg"

type Descriptor struct {
	ID                  string
	Name                string
	Transport           Transport
	DefaultBaseURL      string
	DefaultModel        string
	AuthEnvVars         []string
	CustomHeaders       map[string]string
	RequiresAuth        bool
	UsesAmbientAuth     bool
	Public              bool
	Local               bool
	SupportedAPIFormats []APIFormat
	Aliases             []string

	// Recommended marks a provider surfaced at the top and badged in
	// catalog-ordered lists and pickers. The recommended descriptors are the first
	// entries in the catalog, in order.
	Recommended bool
}

func RuntimeSupported(descriptor Descriptor) bool {
	switch descriptor.Transport {
	case TransportOpenAI, TransportOpenAICompatible, TransportAnthropic, TransportAnthropicCompatible, TransportGoogle:
		return true
	default:
		return false
	}
}

func RuntimeUnsupportedReason(descriptor Descriptor) string {
	if RuntimeSupported(descriptor) {
		return ""
	}
	switch descriptor.Transport {
	case TransportBedrock, TransportVertex:
		return "native adapter not implemented yet"
	default:
		return "provider transport not implemented yet"
	}
}

var descriptors = []Descriptor{
	// QG — single provider. OpenAI-compatible endpoint at
	// https://api.quebragalho.dev/v1 with a Bearer QUEBRAGALHO_API_KEY key;
	// default model muse-spark-1.3-contributor.
	recommended(openAICompat(QGID, "Quebra Galho", "https://api.quebragalho.dev/v1", "muse-spark-1.3-contributor", []string{"QUEBRAGALHO_API_KEY"}, "quebragalho", "quebra-galho")),
}

func All() []Descriptor {
	copied := make([]Descriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		copied = append(copied, cloneDescriptor(descriptor))
	}
	return copied
}

func Get(id string) (Descriptor, bool) {
	normalized := NormalizeID(id)
	for _, descriptor := range descriptors {
		if descriptor.ID == normalized {
			return cloneDescriptor(descriptor), true
		}
		for _, alias := range descriptor.Aliases {
			if NormalizeID(alias) == normalized {
				return cloneDescriptor(descriptor), true
			}
		}
	}
	return Descriptor{}, false
}

func Require(id string) (Descriptor, error) {
	normalized := NormalizeID(id)
	descriptor, ok := Get(normalized)
	if !ok {
		return Descriptor{}, fmt.Errorf("%w %q", ErrUnknownProvider, normalized)
	}
	return descriptor, nil
}

func NormalizeID(id string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(id)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastDash = false
		default:
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}
func recommended(descriptor Descriptor) Descriptor {
	descriptor.Recommended = true
	return descriptor
}
func openAICompat(id string, name string, baseURL string, model string, env []string, aliases ...string) Descriptor {
	return Descriptor{
		ID:                  id,
		Name:                name,
		Transport:           TransportOpenAICompatible,
		DefaultBaseURL:      baseURL,
		DefaultModel:        model,
		AuthEnvVars:         env,
		RequiresAuth:        len(env) > 0,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
		Aliases:             aliases,
	}
}
func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.AuthEnvVars = append([]string{}, descriptor.AuthEnvVars...)
	descriptor.SupportedAPIFormats = append([]APIFormat{}, descriptor.SupportedAPIFormats...)
	descriptor.Aliases = append([]string{}, descriptor.Aliases...)
	if descriptor.CustomHeaders != nil {
		descriptor.CustomHeaders = copyStringMap(descriptor.CustomHeaders)
	}
	return descriptor
}

func copyStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}
