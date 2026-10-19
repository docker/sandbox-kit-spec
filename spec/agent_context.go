package spec

import "fmt"

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
