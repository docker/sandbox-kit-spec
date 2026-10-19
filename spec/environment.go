package spec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var envRef = regexp.MustCompile(`\$\{\{\s*kit\.env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
var envOpener = regexp.MustCompile(`\$\{\{\s*kit\.env\b`)

// ContainsEnvRef reports environment template syntax, including malformed
// references that must fail instead of becoming literal runtime policy.
func ContainsEnvRef(s string) bool { return envOpener.MatchString(s) }

// ExpandEnvironment copies a descriptor and substitutes final-container
// environment values in capability configuration strings, including groups.
// It never reads the process environment or expands keys or descriptor metadata.
// Callers validate the result before selection and composition.
func ExpandEnvironment(d *Descriptor, environment map[string]string) (*Descriptor, error) {
	if d == nil {
		return nil, fmt.Errorf("expand environment: no descriptor")
	}
	remaining := SizeErrorBytes
	var expand func(any, string) (any, error)
	expand = func(value any, at string) (any, error) {
		switch v := value.(type) {
		case map[string]any:
			out := make(map[string]any, len(v))
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if ContainsEnvRef(key) {
					return nil, fieldErrorf(at, "environment references are not allowed in mapping keys")
				}
				var err error
				out[key], err = expand(v[key], at+"."+key)
				if err != nil {
					return nil, err
				}
			}
			return out, nil
		case []any:
			out := make([]any, len(v))
			for i, child := range v {
				var err error
				out[i], err = expand(child, fmt.Sprintf("%s[%d]", at, i))
				if err != nil {
					return nil, err
				}
			}
			return out, nil
		case string:
			var out strings.Builder
			appendText := func(text string) error {
				if len(text) > remaining {
					return fieldErrorf(at, "environment expansion exceeds descriptor size budget: %w", ErrDescriptorTooLarge)
				}
				remaining -= len(text)
				out.WriteString(text)
				return nil
			}
			for ContainsEnvRef(v) {
				start := envOpener.FindStringIndex(v)[0]
				match := envRef.FindStringSubmatchIndex(v[start:])
				if match == nil || match[0] != 0 {
					return nil, fieldErrorf(at, "malformed kit.env reference")
				}
				name := v[start+match[2] : start+match[3]]
				replacement, ok := environment[name]
				if !ok {
					return nil, fieldErrorf(at, "final container environment has no variable %q", name)
				}
				if strings.ContainsRune(replacement, '\x00') || ContainsKitPlaceholder(replacement) {
					return nil, fieldErrorf(at, "environment variable %q contains NUL or Kit placeholders", name)
				}
				if err := appendText(v[:start]); err != nil {
					return nil, err
				}
				if err := appendText(replacement); err != nil {
					return nil, err
				}
				v = v[start+match[1]:]
			}
			if err := appendText(v); err != nil {
				return nil, err
			}
			return out.String(), nil
		default:
			return value, nil
		}
	}
	// JSON normalizes caller-created config containers as well as decoded ones.
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > SizeErrorBytes {
		return nil, fmt.Errorf("expand environment: invalid or oversized descriptor")
	}
	out, err := Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("expand environment: invalid descriptor")
	}
	var walk func([]Capability, string) error
	walk = func(items []Capability, at string) error {
		for i := range items {
			item := &items[i]
			field := fmt.Sprintf("%s[%d]", at, i)
			if item.Group != nil {
				if err := walk(item.Group.Capabilities, field+".group.capabilities"); err != nil {
					return err
				}
				continue
			}
			if item.Config == nil {
				continue
			}
			config, err := expand(item.Config, field+".config")
			if err != nil {
				return err
			}
			item.Config = config.(map[string]any)
		}
		return nil
	}
	if err := walk(out.Capabilities, "capabilities"); err != nil {
		return nil, err
	}
	return out, nil
}

// HasEnvReferences reports references in decoded configuration strings and keys,
// including group members. Checking serialized bytes would miss escaped whitespace.
func HasEnvReferences(items []Capability) bool {
	for _, c := range DeclaredCapabilities(items) {
		values, keys := environmentReferences(c.Config)
		if values || keys {
			return true
		}
	}
	return false
}

// Normalize caller-created containers as well as decoded ones. Key references
// are invalid at publication; only value references can defer typed validation.
func environmentReferences(config map[string]any) (values, keys bool) {
	raw, err := json.Marshal(config)
	if err != nil {
		return false, false // Configuration validation owns unrepresentable values.
	}
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return false, false
	}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case string:
			values = values || ContainsEnvRef(v)
		case map[string]any:
			for key, child := range v {
				keys = keys || ContainsEnvRef(key)
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(root)
	return values, keys
}
