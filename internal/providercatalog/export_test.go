// Test seams: helpers only test code uses, kept out of the production binary.
package providercatalog

func IDs() []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		ids = append(ids, descriptor.ID)
	}
	return ids
}

func ListByTransport(transport Transport) []Descriptor {
	normalized := Transport(NormalizeID(string(transport)))
	items := make([]Descriptor, 0)
	for _, descriptor := range descriptors {
		if descriptor.Transport == normalized {
			items = append(items, cloneDescriptor(descriptor))
		}
	}
	return items
}

func ValidAPIFormat(format APIFormat) bool {
	return format == APIFormatOpenAIChatCompletions
}

func ValidTransport(transport Transport) bool {
	switch Transport(NormalizeID(string(transport))) {
	case TransportOpenAI, TransportOpenAICompatible:
		return true
	default:
		return false
	}
}
