package spec

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Check the raw shape even when text is parameterized: expansion cannot make
// an unknown field or a null into a valid inline-context declaration.
func validateContextV2(field string, i int, n Capability) error {
	var a AgentContextV2
	if err := DecodeCapabilityConfig(n, &a); err != nil {
		return fieldErrorf(field+".config", "capabilities[%d]: %v", i, err)
	}
	for key, value := range n.Config {
		text, ok := value.(string)
		if !ok {
			return fieldErrorf(field+".config."+key, "capabilities[%d]: %s must be a string", i, key)
		}
		if ContainsKitPlaceholder(envRef.ReplaceAllString(argRef.ReplaceAllString(text, ""), "")) {
			return fieldErrorf(field+".config."+key, "capabilities[%d]: malformed Kit reference", i)
		}
	}
	if a.Filename == "" && a.Content == "" {
		return fieldErrorf(field+".config", "capabilities[%d]: agent-context@2 requires a profile or nonempty content", i)
	}
	return nil
}

func mixedContextVersions() error {
	return fmt.Errorf("agent-context: selected @1 and @2 requests cannot compose; migrate all selected guidance to one version")
}

// Bound repeated serialized content before constructing effective profiles.
// The final descriptor check still counts destinations and other metadata.
func joinContextV2(sources []ContextSource, profiles int) (string, error) {
	remaining := SizeErrorBytes / profiles
	tooLarge := fmt.Errorf("merge: repeated agent-context content exceeds the %d byte budget: %w", SizeErrorBytes, ErrDescriptorTooLarge)
	bodies := make([]string, 0, len(sources))
	for _, source := range sources {
		if len(bodies) > 0 {
			remaining -= 4 // Two newlines serialize as two JSON escape pairs.
		}
		if len(source.Content) > remaining {
			return "", tooLarge
		}
		encoded, err := json.Marshal(source.Content)
		if err != nil {
			return "", fmt.Errorf("merge: serialize agent-context content: %w", err)
		}
		remaining -= len(encoded) - 2 // String delimiters belong to the profile.
		if remaining < 0 {
			return "", tooLarge
		}
		bodies = append(bodies, source.Content)
	}
	return strings.Join(bodies, "\n\n"), nil
}
