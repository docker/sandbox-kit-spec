package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON preserves the distinction between omitted delivery and an
// invalid explicit empty value before a typed round trip can erase it.
// Decode each string once: a second full-object pass would duplicate large
// escaped instruction bodies before composition can enforce its budget.
func (a *AgentContextV2) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	start, err := dec.Token()
	if err != nil {
		return err
	}
	if start == nil {
		*a = AgentContextV2{}
		return nil
	}
	if start != json.Delim('{') {
		return fmt.Errorf("agent-context@2 config must be an object")
	}
	var value AgentContextV2
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var target *string
		switch key {
		case "directory":
			target = &value.Directory
		case "filename":
			target = &value.Filename
		case "content":
			target = &value.Content
		case "delivery":
			target = &value.Delivery
		default:
			return fmt.Errorf("json: unknown field %q", key)
		}
		var text *string
		if err := dec.Decode(&text); err != nil {
			return err
		}
		if text == nil {
			return fmt.Errorf("%s must be a string", key)
		}
		if key == "delivery" && *text == "" {
			return fmt.Errorf("delivery must be progressive or inline")
		}
		*target = *text
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	*a = value
	return nil
}

// Check the raw shape even when text is parameterized: expansion cannot make
// an unknown field or a null into a valid inline-context declaration.
func validateContextV2(field string, i int, n Capability) error {
	var a AgentContextV2
	if err := DecodeCapabilityConfig(n, &a); err != nil {
		return configErrorf(field, i, err)
	}
	// Decoding admits only exact field names, so each key is directory,
	// filename, content, or delivery.
	for key, value := range n.Config {
		text, ok := value.(string)
		if !ok {
			return fieldErrorf(field+".config."+key, "capabilities[%d]: %s must be a string", i, key)
		}
		if ContainsKitPlaceholder(envRef.ReplaceAllString(argRef.ReplaceAllString(text, ""), "")) {
			return fieldErrorf(field+".config."+key, "capabilities[%d]: malformed Kit reference", i)
		}
	}
	if delivery, stated := n.Config["delivery"]; stated {
		value, _ := delivery.(string)
		if !ContainsKitPlaceholder(value) && value != AgentContextDeliveryProgressive && value != AgentContextDeliveryInline {
			return fieldErrorf(field+".config.delivery", "capabilities[%d]: delivery must be progressive or inline", i)
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
