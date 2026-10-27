package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Typed accessors over the needs list. Consumers (the frontend, the
// resolver, the runtime) read capabilities through these instead of
// walking entries: well-known configs decode strictly against the schema
// their type's version pins, so a typo in a known config is an error
// here, not a silently ignored key.

// DecodeCapabilityConfig decodes one entry's config into a well-known type's
// config struct, rejecting unknown fields.
//
// A field is known only by its exact name, as the capability schemas
// state. encoding/json matches names case-insensitively, so without the
// exact walk `Path` would decode as volume@1's path, while the validation
// checks that read raw keys by exact name would see no path at all.
func DecodeCapabilityConfig(n Capability, out any) error {
	data, err := checkConfigFields(n, reflect.TypeOf(out), false)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("capability %s: %w", n.Type, err)
	}
	return nil
}

// configTypes is the struct each config-bearing well-known type decodes
// into. TestCapabilitySchemasAgreeOnCaseVariants requires it to cover
// every such type, so a new one cannot skip that check or
// parameterizedConfigFields.
var configTypes = map[string]reflect.Type{
	CapabilityNetworkPolicy:            reflect.TypeFor[PhasedNetwork](),
	CapabilityNetworkPolicyV2:          reflect.TypeFor[PhasedNetworkV2](),
	CapabilityCredential:               reflect.TypeFor[Credential](),
	CapabilitySSHAgent:                 reflect.TypeFor[SSHAgent](),
	CapabilityVolume:                   reflect.TypeFor[Volume](),
	CapabilityHostMount:                reflect.TypeFor[HostMount](),
	CapabilityPort:                     reflect.TypeFor[Port](),
	CapabilityAgentSkills:              reflect.TypeFor[AgentSkills](),
	CapabilityAgentSkill:               reflect.TypeFor[AgentSkill](),
	CapabilityUSBDevice:                reflect.TypeFor[USBDevice](),
	CapabilityResources:                reflect.TypeFor[Resources](),
	CapabilityLifecycle:                reflect.TypeFor[Lifecycle](),
	CapabilityAgentContext:             reflect.TypeFor[AgentContext](),
	CapabilityAgentContextV2:           reflect.TypeFor[AgentContextV2](),
	CapabilityACP:                      reflect.TypeFor[ACP](),
	CapabilityAgentSessions:            reflect.TypeFor[AgentSessions](),
	CapabilityAgentInteractiveSessions: reflect.TypeFor[AgentInteractiveSessions](),
}

// parameterizedConfigFields refuses a key of a parameterized entry that
// names no field of its type, before expansion lets the entry decode. No
// value changes which field a key names, so create would refuse it
// whatever the args are, and until then the checks that read raw keys by
// exact name would not see it. A key that itself holds a reference waits:
// create expands map keys (SPEC-v3 §6). An unknown type's config is
// opaque and is not walked.
func parameterizedConfigFields(n Capability) error {
	t, known := configTypes[n.Type]
	if !known {
		return nil
	}
	_, err := checkConfigFields(n, t, true)
	return err
}

// checkConfigFields encodes n's config and walks it against t, returning
// the encoding for the decoder. The walk reads the JSON form rather than
// n.Config: a synthesized config can hold typed Go values ([]string,
// structs), which it would not recognize as the lists and objects they
// encode to.
func checkConfigFields(n Capability, t reflect.Type, deferReferences bool) ([]byte, error) {
	data, err := json.Marshal(n.Config)
	if err != nil {
		return nil, fmt.Errorf("capability %s: encode config: %w", n.Type, err)
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("capability %s: %w", n.Type, err)
	}
	if err := exactConfigFields(raw, t, "", deferReferences); err != nil {
		return nil, fmt.Errorf("capability %s: %w", n.Type, err)
	}
	return data, nil
}

// exactConfigFields refuses an object key, at any depth, that is not
// exactly the JSON name of a field of the struct it decodes into. Map
// keys are data, so only map values are walked, and a value of the wrong
// shape is left for the decoder to report. Walking by shape also holds
// for NetworkEntry, whose UnmarshalJSON decodes an object into its own
// fields. deferReferences skips a field key holding an arg or env
// reference, and its value: expansion decides which field an arg
// reference names, and an env reference in a key is refused on its own.
func exactConfigFields(v any, t reflect.Type, at string, deferReferences bool) error {
	if t == nil {
		return nil // A nil target is the decoder's to refuse.
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		fields, err := configFields(t)
		if err != nil {
			return err
		}
		for _, key := range slices.Sorted(maps.Keys(obj)) {
			if deferReferences && (ContainsArgRef(key) || ContainsEnvRef(key)) {
				continue
			}
			field, known := fields[key]
			if !known {
				return unknownConfigField(at, key, fields)
			}
			if err := exactConfigFields(obj[key], field, configPath(at, key), deferReferences); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		for _, key := range slices.Sorted(maps.Keys(obj)) {
			if err := exactConfigFields(obj[key], t.Elem(), configPath(at, key), deferReferences); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		list, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, item := range list {
			if err := exactConfigFields(item, t.Elem(), fmt.Sprintf("%s[%d]", at, i), deferReferences); err != nil {
				return err
			}
		}
	}
	return nil
}

// configFields maps the JSON name of each field of struct t to the
// field's type, named as encoding/json names it: by its tag, or by its Go
// name when untagged; a field tagged "-" or unexported has none. An
// embedded field is refused rather than modeled; no config type has one.
func configFields(t reflect.Type) (map[string]reflect.Type, error) {
	fields := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			return nil, fmt.Errorf("config type %s embeds %s, which exact field checking does not model", t, f.Type)
		}
		tag := f.Tag.Get("json")
		if !f.IsExported() || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields, nil
}

// unknownConfigFieldError is a config key that names no field. path is
// the key's path within the config in the dotted form Positions uses, so
// a diagnostic can point at the key rather than at the whole config.
type unknownConfigFieldError struct {
	path    string
	suggest string
}

func (e *unknownConfigFieldError) Error() string {
	if e.suggest != "" {
		return fmt.Sprintf("unknown field %q: config field names are case-sensitive; did you mean %q?", e.path, e.suggest)
	}
	return fmt.Sprintf("unknown field %q", e.path)
}

// unknownConfigField names the refused key by its path in the config,
// and the field it misspells when the two differ only in case.
func unknownConfigField(at, key string, fields map[string]reflect.Type) error {
	refused := &unknownConfigFieldError{path: configPath(at, key)}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if strings.EqualFold(name, key) {
			refused.suggest = configPath(at, name)
			break
		}
	}
	return refused
}

// configErrorPath is the path a config decoding failure of the entry at
// field belongs to: the key it refuses, when it refuses one, and the
// config otherwise.
func configErrorPath(field string, err error) string {
	field += ".config"
	if key, ok := errors.AsType[*unknownConfigFieldError](err); ok {
		field += "." + key.path
	}
	return field
}

func configErrorf(field string, i int, err error) error {
	return fieldErrorf(configErrorPath(field, err), "capabilities[%d]: %v", i, err)
}

func configPath(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

// toConfigMap is DecodeCapabilityConfig's inverse: it renders a
// well-known config struct back into the untyped map the grammar carries.
// The same JSON round trip both ways is what makes a synthesized entry —
// the merge's output — decode into exactly the struct it came from, and
// omitempty on those structs is what keeps an untouched field out of the
// published descriptor.
func toConfigMap(config any) (map[string]any, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CapabilityWithConfig returns the entry with its config replaced by the
// encoding of config, keeping everything that describes the request
// rather than stating it (type, optional, description). Callers that
// rewrite one well-known config — the frontend inlining a context body —
// go through this so the rewritten entry decodes exactly as an authored
// one would.
func CapabilityWithConfig(n Capability, config any) (*Capability, error) {
	raw, err := toConfigMap(config)
	if err != nil {
		return nil, fmt.Errorf("capability %s: encode config: %w", n.Type, err)
	}
	out := n
	out.Config = raw
	return &out, nil
}

// CapabilityTypes renders AnnotationCapabilities's value: the requested
// types, deduplicated, sorted, comma-joined. Sorted rather than authored
// order because the value is an existence index where order carries no
// meaning — sorting makes it canonical, so kits requesting the same types
// carry byte-identical values. Empty for an empty list, which callers
// translate to omitting the annotation.
func CapabilityTypes(capabilities []Capability) string {
	seen := make(map[string]bool, len(capabilities))
	types := make([]string, 0, len(capabilities))
	for _, c := range DeclaredCapabilities(capabilities) {
		if seen[c.Type] {
			continue
		}
		seen[c.Type] = true
		types = append(types, c.Type)
	}
	sort.Strings(types)
	return strings.Join(types, ",")
}

// HasCapability reports whether a type is requested.
func HasCapability(needs []Capability, typ string) bool {
	for _, n := range needs {
		if n.Type == typ {
			return true
		}
	}
	return false
}

// PrivilegedOf reports whether the descriptor requests privileged
// execution.
func PrivilegedOf(needs []Capability) bool {
	return HasCapability(needs, CapabilityPrivileged)
}

// NetworkPolicyOf returns the network-policy@1 request's config, or nil
// when none is declared. A descriptor on @2 has no @1 entry and reads as
// nil here; callers that must see either version use NetworkPolicyV2Of.
func NetworkPolicyOf(needs []Capability) (*PhasedNetwork, error) {
	for _, n := range needs {
		if n.Type != CapabilityNetworkPolicy {
			continue
		}
		var p PhasedNetwork
		if err := DecodeCapabilityConfig(n, &p); err != nil {
			return nil, err
		}
		return &p, nil
	}
	return nil, nil
}

// NetworkPolicyV2Of returns the network-policy request's config in the @2
// shape whichever version declared it, or nil when neither is. An @1
// config lifts into @2 as unbounded entries, which is what it means:
// every method and path on the hosts it allows. Consumers that read the
// policy to enforce it use this, so a kit does not change what they see
// by moving between versions.
func NetworkPolicyV2Of(needs []Capability) (*PhasedNetworkV2, error) {
	for _, n := range needs {
		switch n.Type {
		case CapabilityNetworkPolicyV2:
			var p PhasedNetworkV2
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				return nil, err
			}
			return &p, nil
		case CapabilityNetworkPolicy:
			var p PhasedNetwork
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				return nil, err
			}
			return &PhasedNetworkV2{
				Install: liftNetworkRules(p.Install),
				Runtime: liftNetworkRules(p.Runtime),
			}, nil
		}
	}
	return nil, nil
}

func liftNetworkRules(r *NetworkRules) *NetworkRulesV2 {
	if r == nil {
		return nil
	}
	return &NetworkRulesV2{Allow: hostEntries(r.Allow), Deny: hostEntries(r.Deny)}
}

// hostEntries renders @1's bare host lists as unbounded @2 entries.
func hostEntries(hosts []string) []NetworkEntry {
	if len(hosts) == 0 {
		return nil
	}
	out := make([]NetworkEntry, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, NetworkEntry{Hosts: []string{h}})
	}
	return out
}

// EntryMethods renders an entry's methods as enforcement reads them:
// MethodAny when it named none, the stated tokens otherwise.
func EntryMethods(e NetworkEntry) []string {
	if len(e.Methods) == 0 {
		return []string{MethodAny}
	}
	return e.Methods
}

// EntryPaths renders an entry's paths as enforcement reads them: every
// path when it named none, the stated globs otherwise.
func EntryPaths(e NetworkEntry) []string {
	if len(e.Paths) == 0 {
		return []string{"/**"}
	}
	return e.Paths
}

// CredentialCapability is one decoded credential request with its entry-level
// declaration state.
type CredentialCapability struct {
	Credential

	// Required mirrors the entry's optionality: a required credential
	// fails preflight when no binding satisfies the service, an optional
	// one is skipped and recorded.
	Required bool

	// Name and Description are the entry's display label and explanation.
	Name        string
	Description string
}

// CredentialsOf returns every credential request, in declaration order.
func CredentialsOf(needs []Capability) ([]CredentialCapability, error) {
	var out []CredentialCapability
	for _, n := range needs {
		if n.Type != CapabilityCredential {
			continue
		}
		var c Credential
		if err := DecodeCapabilityConfig(n, &c); err != nil {
			return nil, err
		}
		out = append(out, CredentialCapability{Credential: c, Required: !n.Optional, Name: n.Name, Description: n.Description})
	}
	return out, nil
}

// CredentialsOfPhase returns the credential requests for one phase.
func CredentialsOfPhase(needs []Capability, phase string) ([]CredentialCapability, error) {
	all, err := CredentialsOf(needs)
	if err != nil {
		return nil, err
	}
	var out []CredentialCapability
	for _, c := range all {
		if slices.Contains(c.Phase, phase) {
			out = append(out, c)
		}
	}
	return out, nil
}

// SSHAgentCapability is one SSH agent request with its entry-level
// optionality and display metadata, which the runtime needs for preflight.
type SSHAgentCapability struct {
	SSHAgent
	Optional    bool
	Name        string
	Description string
}

// SSHAgentsOf returns SSH agent requests in declaration order. One request
// may name both phases; callers enforcing one phase use SSHAgentsOfPhase.
func SSHAgentsOf(needs []Capability) ([]SSHAgentCapability, error) {
	var out []SSHAgentCapability
	for _, n := range needs {
		if n.Type != CapabilitySSHAgent {
			continue
		}
		var a SSHAgent
		if err := DecodeCapabilityConfig(n, &a); err != nil {
			return nil, err
		}
		out = append(out, SSHAgentCapability{SSHAgent: a, Optional: n.Optional, Name: n.Name, Description: n.Description})
	}
	return out, nil
}

// SSHAgentsOfPhase returns requests granting the agent in a given phase.
func SSHAgentsOfPhase(needs []Capability, phase string) ([]SSHAgentCapability, error) {
	all, err := SSHAgentsOf(needs)
	if err != nil {
		return nil, err
	}
	var out []SSHAgentCapability
	for _, a := range all {
		if slices.Contains(a.Phase, phase) {
			out = append(out, a)
		}
	}
	return out, nil
}

// AgentSkillsOf returns selected discovery destinations in declaration order.
// Every destination receives selected AgentSkill bundles. Optional governs
// capability support, not whether the host has skills available to share.
func AgentSkillsOf(needs []Capability) ([]AgentSkillsCapability, error) {
	var out []AgentSkillsCapability
	for _, n := range needs {
		if n.Group != nil {
			return nil, fmt.Errorf("agent skills: select groups first")
		}
		if n.Type != CapabilityAgentSkills {
			continue
		}
		var s AgentSkills
		if err := DecodeCapabilityConfig(n, &s); err != nil {
			return nil, err
		}
		out = append(out, AgentSkillsCapability{
			AgentSkills: s,
			Optional:    n.Optional,
			Name:        n.Name,
			Description: n.Description,
		})
	}
	return out, nil
}

// VolumesOf returns every volume request, in declaration order.
func VolumesOf(needs []Capability) ([]Volume, error) {
	var out []Volume
	for _, n := range needs {
		if n.Type != CapabilityVolume {
			continue
		}
		var v Volume
		if err := DecodeCapabilityConfig(n, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// HostMountsOf returns every selected host-directory request, in declaration order.
func HostMountsOf(needs []Capability) ([]HostMount, error) {
	var out []HostMount
	for _, n := range needs {
		if n.Group != nil {
			return nil, fmt.Errorf("host mounts: select groups first")
		}
		if n.Type != CapabilityHostMount {
			continue
		}
		var mount HostMount
		if err := DecodeCapabilityConfig(n, &mount); err != nil {
			return nil, err
		}
		out = append(out, mount)
	}
	return out, nil
}

// PortsOf returns every port request, in declaration order.
func PortsOf(needs []Capability) ([]Port, error) {
	var out []Port
	for _, n := range needs {
		if n.Type != CapabilityPort {
			continue
		}
		var p Port
		if err := DecodeCapabilityConfig(n, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// USBDeviceCapability is one decoded usb-device request with its entry-level
// declaration state.
type USBDeviceCapability struct {
	USBDevice

	Optional    bool
	Name        string
	Description string
}

// USBDevicesOf returns every usb-device request, in declaration order.
func USBDevicesOf(needs []Capability) ([]USBDeviceCapability, error) {
	var out []USBDeviceCapability
	for _, n := range needs {
		if n.Type != CapabilityUSBDevice {
			continue
		}
		var u USBDevice
		if err := DecodeCapabilityConfig(n, &u); err != nil {
			return nil, err
		}
		out = append(out, USBDeviceCapability{USBDevice: u, Optional: n.Optional, Name: n.Name, Description: n.Description})
	}
	return out, nil
}

// ResourcesOf returns the resources request's config, or nil when none
// is declared.
func ResourcesOf(needs []Capability) (*Resources, error) {
	for _, n := range needs {
		if n.Type != CapabilityResources {
			continue
		}
		var r Resources
		if err := DecodeCapabilityConfig(n, &r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	return nil, nil
}

// LifecycleOf returns the lifecycle declaration's config, or nil when
// none is declared.
func LifecycleOf(capabilities []Capability) (*Lifecycle, error) {
	for _, c := range capabilities {
		if c.Type != CapabilityLifecycle {
			continue
		}
		var l Lifecycle
		if err := DecodeCapabilityConfig(c, &l); err != nil {
			return nil, err
		}
		return &l, nil
	}
	return nil, nil
}

// AgentContextOf returns the first agent-context entry's config, or nil
// when none is declared. A composed descriptor carries one entry per
// explicit profile, and a runtime materializing profiles reads every
// entry with AgentContextsOf. A second explicit profile is an error
// rather than ignored, so a caller that would write only the first fails
// loudly instead of leaving an agent without its guidance.
func AgentContextOf(capabilities []Capability) (*AgentContext, error) {
	var first *AgentContext
	explicit := 0
	for _, c := range capabilities {
		if c.Type != CapabilityAgentContext {
			continue
		}
		var a AgentContext
		if err := DecodeCapabilityConfig(c, &a); err != nil {
			return nil, err
		}
		if a.Directory != "" {
			if explicit++; explicit > 1 {
				return nil, fmt.Errorf("agent context: several explicit profiles; read every profile with AgentContextsOf")
			}
		}
		if first == nil {
			first = &a
		}
	}
	return first, nil
}

// AgentSessionsOf returns the agent-sessions declaration's config, or
// nil when none is declared.
func AgentSessionsOf(needs []Capability) (*AgentSessions, error) {
	for _, n := range needs {
		if n.Type != CapabilityAgentSessions {
			continue
		}
		var a AgentSessions
		if err := DecodeCapabilityConfig(n, &a); err != nil {
			return nil, err
		}
		return &a, nil
	}
	return nil, nil
}

// AgentInteractiveSessionsOf returns the agent-interactive-sessions
// declaration's config, or nil when none is declared. A verb's presence
// is its slice being non-nil: an authored `newSession: []` decodes to an
// empty non-nil slice, an absent key to nil.
func AgentInteractiveSessionsOf(needs []Capability) (*AgentInteractiveSessions, error) {
	for _, n := range needs {
		if n.Type != CapabilityAgentInteractiveSessions {
			continue
		}
		var a AgentInteractiveSessions
		if err := DecodeCapabilityConfig(n, &a); err != nil {
			return nil, err
		}
		return &a, nil
	}
	return nil, nil
}

// AgentContextsOf reads every selected agent-context entry: one Kit's
// contributions, retaining separate bodies from selected groups for
// contributor-specific runtime guidance handlers, or a composed
// descriptor's profiles, one per destination.
func AgentContextsOf(capabilities []Capability) ([]AgentContext, error) {
	var result []AgentContext
	for _, c := range capabilities {
		if c.Group != nil {
			return nil, fmt.Errorf("agent contexts: select groups first")
		}
		if c.Type != CapabilityAgentContext {
			continue
		}
		var context AgentContext
		if err := DecodeCapabilityConfig(c, &context); err != nil {
			return nil, err
		}
		result = append(result, context)
	}
	return result, nil
}

// AgentContextsV2Of reads every selected inline-context config. It deliberately
// does not lift @1, whose body and surfacing contract require a different reader.
func AgentContextsV2Of(capabilities []Capability) ([]AgentContextV2, error) {
	var result []AgentContextV2
	for _, c := range capabilities {
		if c.Group != nil {
			return nil, fmt.Errorf("agent contexts: select groups first")
		}
		if c.Type != CapabilityAgentContextV2 {
			continue
		}
		var a AgentContextV2
		if err := DecodeCapabilityConfig(c, &a); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, nil
}

// AgentContextV2Of reads a sole inline-context request. Use AgentContextsV2Of
// for several selected declarations or a composition with several profiles;
// returning only the first would silently discard guidance or a destination.
func AgentContextV2Of(capabilities []Capability) (*AgentContextV2, error) {
	contexts, err := AgentContextsV2Of(capabilities)
	if err != nil {
		return nil, err
	}
	if len(contexts) > 1 {
		return nil, fmt.Errorf("agent context: several requests; read all with AgentContextsV2Of")
	}
	if len(contexts) == 0 {
		return nil, nil
	}
	return &contexts[0], nil
}
