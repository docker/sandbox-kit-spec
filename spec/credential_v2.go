package spec

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

const (
	CredentialOnMissingError           = "error"
	CredentialOnMissingUnauthenticated = "unauthenticated"
	OAuthProfileOpenAIChatGPT          = "openai-chatgpt"
)

// CredentialV2 keeps capability support separate from missing host bindings.
// Its OAuth profile is part of the contract and must survive runtime dispatch.
type CredentialV2 struct {
	Service   string   `json:"service" yaml:"service"`
	Phase     Phases   `json:"phase" yaml:"phase"`
	APIKey    *APIKey  `json:"apiKey,omitempty" yaml:"apiKey,omitempty"`
	OAuth     *OAuthV2 `json:"oauth,omitempty" yaml:"oauth,omitempty"`
	OnMissing string   `json:"onMissing,omitempty" yaml:"onMissing,omitempty"`
}

// EffectiveOnMissing returns the missing-binding policy, including its default.
func (c CredentialV2) EffectiveOnMissing() string {
	if c.OnMissing == "" {
		return CredentialOnMissingError
	}
	return c.OnMissing
}

// OAuthV2's explicit profile selects native identity behavior. An omitted
// profile retains the generic OAuth contract; it never infers a profile from
// a service name, endpoint, sentinel, or credential-file template.
type OAuthV2 struct {
	Profile        string          `json:"profile,omitempty" yaml:"profile,omitempty"`
	TokenEndpoint  *TokenEndpoint  `json:"tokenEndpoint,omitempty" yaml:"tokenEndpoint,omitempty"`
	ResourceHosts  []string        `json:"resourceHosts,omitempty" yaml:"resourceHosts,omitempty"`
	Sentinels      *Sentinels      `json:"sentinels,omitempty" yaml:"sentinels,omitempty"`
	CredentialFile *CredentialFile `json:"credentialFile,omitempty" yaml:"credentialFile,omitempty"`
	ResponseFields *ResponseFields `json:"responseFields,omitempty" yaml:"responseFields,omitempty"`
	Passthrough    bool            `json:"passthrough,omitempty" yaml:"passthrough,omitempty"`
}

// CredentialV2Capability retains support requirements independently of OnMissing.
type CredentialV2Capability struct {
	CredentialV2
	Required    bool
	Name        string
	Description string
}

// CredentialsV2Of returns only credential@2 requests in declaration order.
// A caller must implement their versioned contract before claiming support.
func CredentialsV2Of(needs []Capability) ([]CredentialV2Capability, error) {
	var out []CredentialV2Capability
	for _, n := range needs {
		if n.Type != CapabilityCredentialV2 {
			continue
		}
		var c CredentialV2
		if err := DecodeCapabilityConfig(n, &c); err != nil {
			return nil, err
		}
		out = append(out, CredentialV2Capability{CredentialV2: c, Required: !n.Optional, Name: n.Name, Description: n.Description})
	}
	return out, nil
}

func CredentialsV2OfPhase(needs []Capability, phase string) ([]CredentialV2Capability, error) {
	all, err := CredentialsV2Of(needs)
	if err != nil {
		return nil, err
	}
	var out []CredentialV2Capability
	for _, c := range all {
		if slices.Contains(c.Phase, phase) {
			out = append(out, c)
		}
	}
	return out, nil
}

var nativeCredentialSentinel = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validateCredentialV2Need(path string, i int, n Capability) error {
	var c CredentialV2
	if err := DecodeCapabilityConfig(n, &c); err != nil {
		return configErrorf(path, i, err)
	}
	var endpoint *TokenEndpoint
	var file *CredentialFile
	if c.OAuth != nil {
		endpoint, file = c.OAuth.TokenEndpoint, c.OAuth.CredentialFile
	}
	var errs ValidationErrors
	errs.add(validateCredentialFields(path, i, n, c.Service, c.Phase, c.APIKey, c.OAuth != nil, endpoint, file))
	if _, present := n.Config["onMissing"]; present && c.OnMissing == "" {
		errs.add(fieldErrorf(path+".config.onMissing", "capabilities[%d]: onMissing is empty; omit it for error or name a policy", i))
	}
	switch c.EffectiveOnMissing() {
	case CredentialOnMissingError, CredentialOnMissingUnauthenticated:
	default:
		errs.add(fieldErrorf(path+".config.onMissing", "capabilities[%d]: unknown onMissing policy %q", i, c.OnMissing))
	}
	if c.OAuth == nil {
		return errs.err()
	}
	if raw, ok := n.Config["oauth"].(map[string]any); ok {
		if _, present := raw["profile"]; present && c.OAuth.Profile == "" {
			errs.add(fieldErrorf(path+".config.oauth.profile", "capabilities[%d]: profile is empty; omit it for generic OAuth", i))
		}
	}
	switch c.OAuth.Profile {
	case "":
	case OAuthProfileOpenAIChatGPT:
		errs.add(validateOpenAIChatGPT(path, i, n, c))
	default:
		errs.add(fieldErrorf(path+".config.oauth.profile", "capabilities[%d]: unknown OAuth profile %q", i, c.OAuth.Profile))
	}
	if file != nil {
		errs.add(validateCredentialV2Placeholders(path+".config.oauth.credentialFile.structure", file.Structure, c.OAuth.Profile))
	}
	return errs.err()
}

func validateOpenAIChatGPT(path string, i int, n Capability, c CredentialV2) error {
	var errs ValidationErrors
	add := func(field, message string) {
		errs.add(fieldErrorf(path+".config."+field, "capabilities[%d]: openai-chatgpt %s", i, message))
	}
	if c.Service != "openai" {
		add("service", "requires service openai")
	}
	oauth := c.OAuth
	if oauth.TokenEndpoint == nil || oauth.TokenEndpoint.Host != "auth.openai.com" || oauth.TokenEndpoint.Path != "/oauth/token" {
		add("oauth.tokenEndpoint", "requires tokenEndpoint auth.openai.com/oauth/token")
	}
	if oauth.Sentinels == nil || !nativeCredentialSentinel.MatchString(oauth.Sentinels.AccessToken) || !nativeCredentialSentinel.MatchString(oauth.Sentinels.RefreshToken) {
		add("oauth.sentinels", "requires nonempty access and refresh sentinels containing only ASCII letters, digits, underscores, and hyphens")
	} else if oauth.Sentinels.AccessToken == oauth.Sentinels.RefreshToken {
		add("oauth.sentinels", "requires distinct access and refresh sentinels")
	}
	if oauth.Passthrough {
		add("oauth.passthrough", "does not permit passthrough")
	}
	if c.APIKey != nil && !c.APIKey.ProxyManaged {
		add("apiKey.proxyManaged", "requires proxyManaged API-key presentation")
	}
	if raw, ok := nativelyMappedResponseFields(oauth.ResponseFields); !ok {
		add("oauth.responseFields."+raw, "requires standard OAuth response field names")
	}
	if rawOAuth, ok := n.Config["oauth"].(map[string]any); ok {
		if fields, ok := rawOAuth["responseFields"].(map[string]any); ok {
			for _, field := range []string{"accessToken", "refreshToken", "expiresIn"} {
				if value, present := fields[field]; present && value == "" {
					add("oauth.responseFields."+field, "requires standard OAuth response field names")
				}
			}
		}
	}
	return errs.err()
}

func nativelyMappedResponseFields(fields *ResponseFields) (string, bool) {
	if fields == nil {
		return "", true
	}
	for _, field := range []struct{ name, actual, expected string }{
		{"accessToken", fields.AccessToken, "access_token"},
		{"refreshToken", fields.RefreshToken, "refresh_token"},
		{"expiresIn", fields.ExpiresIn, "expires_in"},
	} {
		if field.actual != "" && field.actual != field.expected {
			return field.name, false
		}
	}
	return "", true
}

func validateCredentialV2Placeholders(path string, value any, profile string) error {
	var errs ValidationErrors
	switch v := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			errs.add(validateCredentialV2Placeholders(path+"."+key, v[key], profile))
		}
	case []any:
		for index, item := range v {
			errs.add(validateCredentialV2Placeholders(fmt.Sprintf("%s[%d]", path, index), item, profile))
		}
	case string:
		allowed := []string{".AccessToken", ".RefreshToken", ".ExpiresAt", ".Scopes", ".PrimaryApiKey"}
		if profile == OAuthProfileOpenAIChatGPT {
			allowed = []string{".IDToken", ".AccessToken", ".RefreshToken", ".AccountID", ".LastRefresh"}
		}
		for {
			_, tail, found := strings.Cut(v, "{{")
			if !found {
				break
			}
			placeholder, rest, closed := strings.Cut(tail, "}}")
			if !closed || !slices.Contains(allowed, strings.TrimSpace(placeholder)) {
				errs.add(fieldErrorf(path, "unsupported credential-file placeholder %q", "{{"+placeholder+"}}"))
				break
			}
			v = rest
		}
	}
	return errs.err()
}

func isCredentialType(typ string) bool {
	return typ == CapabilityCredential || typ == CapabilityCredentialV2
}

// credentialBindingFields projects only ownership and API-key injection rules.
// OAuth dispatch must use its versioned accessor to retain the native profile.
func credentialBindingFields(n Capability) (string, Phases, *APIKey, error) {
	if n.Type == CapabilityCredentialV2 {
		var c CredentialV2
		err := DecodeCapabilityConfig(n, &c)
		return c.Service, c.Phase, c.APIKey, err
	}
	var c Credential
	err := DecodeCapabilityConfig(n, &c)
	return c.Service, c.Phase, c.APIKey, err
}

func splitCredentialPhases(n Capability) ([]Capability, error) {
	if n.Type == CapabilityCredentialV2 {
		_, phases, _, err := credentialBindingFields(n)
		if err != nil {
			return nil, err
		}
		if len(phases) == 0 {
			return nil, fmt.Errorf("credential phase is empty")
		}
		entries := make([]Capability, 0, len(phases))
		for _, phase := range phases {
			entry := n
			entry.Config = maps.Clone(n.Config)
			entry.Config["phase"] = phase
			entries = append(entries, entry)
		}
		return entries, nil
	}
	var c Credential
	if err := DecodeCapabilityConfig(n, &c); err != nil {
		return nil, err
	}
	if len(c.Phase) == 0 {
		return nil, fmt.Errorf("credential phase is empty")
	}
	entries := make([]Capability, 0, len(c.Phase))
	for _, phase := range c.Phase {
		one := c
		one.Phase = Phases{phase}
		entry, err := CapabilityWithConfig(n, one)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	return entries, nil
}

func validateCredentialV2Presence(path string, i int, n Capability) error {
	var errs ValidationErrors
	for _, at := range nullConfigPaths("config", n.Config, "config.oauth.credentialFile.structure") {
		errs.add(fieldErrorf(path+"."+at, "capabilities[%d]: %s is null; omit the field or declare a value", i, strings.TrimPrefix(at, "config.")))
	}
	for _, keys := range [][]string{{"onMissing"}, {"oauth", "profile"}, {"oauth", "credentialFile", "format"}} {
		var value any = n.Config
		for _, key := range keys {
			m, ok := value.(map[string]any)
			if !ok {
				value = nil
				break
			}
			value = m[key]
		}
		if value == "" {
			field := strings.Join(keys, ".")
			errs.add(fieldErrorf(path+".config."+field, "capabilities[%d]: %s is empty; omit the field for its default", i, field))
		}
	}
	return errs.err()
}
