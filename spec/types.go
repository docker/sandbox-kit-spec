package spec

import (
	"bytes"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only schema version this package decodes.
const SchemaVersion = "3"

// AnnotationDescriptor is the OCI manifest annotation carrying the published
// descriptor as compact JSON. Authoring is YAML; the published form is a
// derived artifact (args expanded, contentFile rewritten), serialized as
// JSON to match the manifest it rides in. Consumers decode it with the same
// YAML decoder as authored files — YAML accepts JSON as a subset — so
// older kits whose annotations carry YAML keep decoding.
const AnnotationDescriptor = "vnd.docker.sandbox.kit.descriptor"

// AnnotationSchemaVersion carries the descriptor's schemaVersion beside it,
// so tooling can dispatch on the grammar version from the manifest alone —
// no YAML parse — and a future schema bump is visible in a registry listing.
// Always equal to the schemaVersion field inside the descriptor.
const AnnotationSchemaVersion = "vnd.docker.sandbox.kit.schema-version"

// AnnotationCapabilities lists the requested capability types as a
// comma-separated string, deduplicated and sorted — e.g.
// "com.docker.sandbox/credential@1,com.docker.sandbox/network-policy@1".
// An index, not a source: the descriptor annotation stays authoritative,
// this exists so existence checks ("does this kit want privileged@1?")
// and policy filters read one small value without parsing the
// descriptor. Commas cannot appear in a type string, so splitting is
// unambiguous. Absent when the kit requests nothing.
const AnnotationCapabilities = "vnd.docker.sandbox.kit.capabilities"

// AnnotationBuiltBy names the frontend build that published the kit, as
// compact JSON decoding into BuiltBy. It identifies the tool that
// produced the artifact rather than the source the artifact was produced
// from — unlike revision, which §9.3 refuses. Absent on kits
// published before the annotation existed, so readers must tolerate it
// missing; and self-asserted, like every annotation here, so it answers
// "what claims to have built this", never "what is this allowed to do".
const AnnotationBuiltBy = "vnd.docker.sandbox.kit.built-by"

// PortTransport reports the transport a port request names, resolving the
// default. An omitted transport and an explicit "tcp" are the same request,
// so everything that identifies a port — the permission surface, the
// uniqueness check — has to see one spelling.
func PortTransport(p Port) string {
	if p.Transport == "" {
		return "tcp"
	}
	return p.Transport
}

// PortKey identifies a port request for uniqueness.
func PortKey(p Port) string {
	return fmt.Sprintf("%d/%s", p.Container, PortTransport(p))
}

// Kit kinds. A workload's layers are a root filesystem that may stand at the
// bottom of the stack; a mixin's are an overlay that must land on one.
const (
	KindWorkload = "workload"
	KindMixin    = "mixin"

	// KindSet is an authoring kind that never reaches a consumer: it
	// declares a kit whose content is the Kits list, and the frontend
	// publishes the merged result under the kind those kits imply —
	// workload when one of them is a workload, mixin when they all are.
	// Publishing "set" would oblige every consumer to learn a third kind
	// for a role that behaves exactly like one of the two that already
	// exist; deriving it also spares the author a claim about the set
	// that its kits already answer. Stating workload or mixin beside
	// Kits stays legal — it says the same thing explicitly, and the
	// frontend holds it to what the listed kits imply.
	KindSet = "set"
)

// Descriptor is the authored kit declaration. Published descriptors carry
// the same declarations with build-phase arg references (${{ kit.args.* }}
// for args with buildArg:) expanded, re-serialized as compact JSON.
type Descriptor struct {
	declarationsOnly bool

	// SchemaVersion must be "3".
	SchemaVersion string `json:"schemaVersion" yaml:"schemaVersion"`

	// DisplayName is what humans read; identity lives in the reference.
	DisplayName string `json:"displayName,omitempty" yaml:"displayName,omitempty"`

	// Author is who published the kit, in the freeform
	// org.opencontainers.image.authors convention ("Name <email>", commas
	// for multiples). Display metadata only, exactly like DisplayName: a
	// self-asserted claim, never identity and never a trust input —
	// publisher identity lives in the reference's registry namespace and
	// in the signature.
	Author string `json:"author,omitempty" yaml:"author,omitempty"`

	// Description is a short human-readable summary.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// SourceURL points at the kit's source repository or documentation.
	SourceURL string `json:"sourceUrl,omitempty" yaml:"sourceUrl,omitempty"`

	// IconURL is an https URL to an image representing the kit in catalogs
	// and pickers. Display metadata like DisplayName, and self-asserted
	// the same way — but unlike the other display fields a consumer
	// fetches and renders it, so the scheme is constrained rather than
	// free text: https only keeps javascript:, data:, and file: out of
	// whatever surface ends up rendering it.
	//
	// A URL rather than staged content because the surfaces that want an
	// icon — a registry listing, a picker — have the manifest and not the
	// layers. An icon in a layer could not be shown without pulling the
	// kit, which is the moment the icon existed to inform.
	IconURL string `json:"iconUrl,omitempty" yaml:"iconUrl,omitempty"`

	// Version is the fallback version for unversioned provides entries,
	// for kits consumed by references that carry no version of their own:
	// a local directory, a git branch or commit. It never overrides the
	// consumption reference — an OCI tag or a version-shaped git ref wins,
	// so a stale Version cannot lie to the resolver — and an explicit
	// provides@version outranks both. May reference a build-phase arg
	// (`${{ kit.args.version }}`), expanded into the published descriptor
	// like provides.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`

	// Licenses lists SPDX license identifiers.
	Licenses []string `json:"licenses,omitempty" yaml:"licenses,omitempty"`

	// Kind is "workload" or "mixin". It names the role; the layer nature
	// follows from it, and it tells the frontend what to emit.
	Kind string `json:"kind" yaml:"kind"`

	// Provides lists capabilities this kit offers, optionally versioned as
	// name@version. Nothing is provided implicitly: a kit that wants to be
	// requirable by name states so here.
	Provides []string `json:"provides,omitempty" yaml:"provides,omitempty"`

	// Requires lists capabilities that must be satisfied by the resolved
	// set, as "name" or "name <op> version" (comma-AND'd constraints;
	// ops: >=, >, <=, <, =). Capability names resolve to nothing and
	// validate against something.
	Requires []string `json:"requires,omitempty" yaml:"requires,omitempty"`

	// Integrates lists capabilities the kit works with when present but
	// functions without, in the requires grammar. An absent provider is
	// fine; a PRESENT one that violates the stated constraints still fails
	// resolution — an integration that would break is incoherence, not an
	// option. A met entry orders like requires: the provider composes
	// before the kit that integrates with it.
	Integrates []string `json:"integrates,omitempty" yaml:"integrates,omitempty"`

	// Conflicts lists capability names that must be absent from the
	// resolved set.
	Conflicts []string `json:"conflicts,omitempty" yaml:"conflicts,omitempty"`

	// Capabilities declares everything the kit needs but cannot supply itself:
	// a list of typed, versioned capability requests, each answered —
	// granted, refused, or prompted — by the host. One model for every
	// capability: well-known types get strict config schemas, cross-entry
	// validation, and direction-aware gating; unknown types are the
	// extension point, refused by name when required, skipped and
	// recorded when optional. Everything the kit asks of its host lives
	// here — resource grants and engine-executed behaviors alike
	// (lifecycle hooks, agent context) — so a host answers the whole ask
	// through one mechanism, or refuses the parts it does not know.
	Capabilities []CapabilityItem `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`

	// Args declares installer-supplied values, keyed by arg name. Private
	// by default: an arg reaches the container only via env:, and the
	// build only via buildArg:.
	Args map[string]Arg `json:"args,omitempty" yaml:"args,omitempty"`

	// Build is the kit's content recipe inline: literal Dockerfile text,
	// the single-file alternative to the companion <stem>.dockerfile
	// (declaring both is an error). Full Dockerfile semantics apply —
	// multi-stage, mounts, even a foreign # syntax= first line — because
	// the frontend hands the block to the Dockerfile frontend verbatim;
	// nothing is transpiled. Build-phase args reach it as --build-arg
	// under their declared buildArg names, exactly like a companion.
	// The published descriptor carries the block, so the annotation also
	// records how the content was produced.
	Build string `json:"build,omitempty" yaml:"build,omitempty"`

	// Kits is this kit's content as a list of published kits rather than
	// Dockerfile text: the fourth authoring form, for an environment
	// assembled out of kits someone already published. Mutually
	// exclusive with Build and Dockerfile — a kit's content recipe lives
	// in exactly one place — and declared by kind: set, or beside the
	// kind the listed kits imply.
	//
	// The frontend resolves every one of them, merges their layers and
	// their declarations, and publishes one ordinary kit. The published
	// descriptor retains the list with each entry pinned by Digest, the
	// way it retains an inline Build block: the artifact still records
	// how its content was produced.
	Kits []Kit `json:"kits,omitempty" yaml:"kits,omitempty"`

	// Dockerfile names the companion recipe explicitly, as a path
	// relative to the descriptor's directory. Optional sugar over the
	// filename-stem convention (<stem>.dockerfile beside the
	// descriptor): stating it makes the pairing visible in the
	// descriptor and frees the recipe's name from the stem. Unlike the
	// conventional companion — whose absence just means a
	// declaration-only kit — a named recipe that does not exist is an
	// error: an explicit reference failing silently would build the
	// wrong kit. Mutually exclusive with build:.
	Dockerfile string `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty"`
}

// Kit is one entry in a set's Kits list: a published kit the set is
// composed of.
//
// It is named the way any kit is consumed — by a registry reference —
// because the set's whole content comes from it, and the frontend
// resolving that reference is what pins it. It carries no role: whether
// it lands as the base or as an overlay is its own descriptor's kind,
// which the set does not restate.
type Kit struct {
	// Ref is the reference this kit is consumed by, and the identity it
	// is named by in every merge diagnostic. A registry reference: a
	// local directory or a git URL names a kit that is not published
	// yet, and a set's kits have to be resolvable from the manifest
	// alone.
	Ref string `json:"ref" yaml:"ref"`

	// Digest pins this kit's manifest. Optional in the authored form —
	// where a tag is how an author names a version — and required in
	// the published one, which the frontend fills in from the reference
	// it actually resolved.
	Digest string `json:"digest,omitempty" yaml:"digest,omitempty"`

	// Args supplies this kit's create-phase args, keyed by its own arg
	// names. A literal value is expanded into the merged descriptor at
	// publish, which is what makes the set a pinned artifact. A value
	// that is itself a `${{ kit.args.* }}` reference re-exports the
	// input under the set's name: the reference lands where the
	// placeholder was, and the set's own declaration — signed with the
	// set — is what bounds the value an installer can supply.
	Args map[string]string `json:"args,omitempty" yaml:"args,omitempty"`
}

// Capability is one typed, versioned capability request. The type string is
// <namespace>/<name>@<version>: the namespace admits host-specific
// capability types without touching this grammar, and the version moves
// when the type's config schema does — capabilities evolve without a
// descriptor schema-major bump.
type Capability struct {
	// Group is the alternative to an ordinary entry. It is not a capability
	// type and must be selected before typed accessors are used.
	Group *CapabilityGroup `json:"group,omitempty" yaml:"group,omitempty"`

	// Source preserves publisher attribution. It is untrusted diagnostic
	// metadata; the consuming artifact still determines trust.
	Source      *CapabilitySource `json:"source,omitempty" yaml:"source,omitempty"`
	optionalSet bool
	groupSet    bool

	// Type names the capability and its config-schema version, e.g.
	// "com.docker.sandbox/network-policy@1".
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// Name is a display label, not identity: duplicate labels are allowed
	// and renaming a request changes neither its grants nor its merge key.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Optional marks a capability the kit degrades gracefully without:
	// an unknown or unprovided optional capability is skipped and recorded,
	// while a required one fails resolution closed. For credentials this
	// subsumes the older per-credential required flag.
	Optional bool `json:"optional,omitempty" yaml:"optional,omitempty"`

	// Config is the type-specific request payload. Well-known types are
	// decoded strictly against the schema their version pins; unknown
	// types carry it opaquely. Part of the permission surface: config
	// changes gate like any widening.
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`

	// configSet records that the document carried a `config` key, which
	// the decoded map cannot express: an explicit null and an omitted key
	// both decode to nil, while the config-less types' schemas reject any
	// value including null and {}.
	configSet bool

	// Description is shown wherever the request is listed or prompted.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// UnmarshalYAML records whether the document stated a config at all,
// which the decoded map cannot distinguish from an omitted key. The
// callback form, not the node form: a node decodes through a fresh
// decoder that does not inherit KnownFields, which would silently accept
// misspelled capability fields.
func (c *Capability) UnmarshalYAML(unmarshal func(any) error) error {
	type plain Capability
	if err := unmarshal((*plain)(c)); err != nil {
		return err
	}
	var keys map[string]yaml.Node
	if err := unmarshal(&keys); err != nil {
		return err
	}
	// YAML coerces numbers and booleans into Go strings and accepts null.
	// Match the schema's string-only label instead of silently renaming it.
	if name, ok := keys["name"]; ok && name.ShortTag() != "!!str" {
		return fmt.Errorf("capability name must be a string")
	}
	_, c.configSet = keys["config"]
	_, c.optionalSet = keys["optional"]
	_, c.groupSet = keys["group"]
	if _, stated := keys["source"]; stated && c.Source == nil {
		return fmt.Errorf("capability source must be an object")
	}
	if c.groupSet {
		for _, key := range []string{"type", "name", "description", "optional", "config"} {
			if _, ok := keys[key]; ok {
				return fmt.Errorf("group item must not state %s", key)
			}
		}
	}
	return nil
}

// UnmarshalJSON records config presence for the JSON spelling. Decoding
// is strict, as every other well-known config is — a custom unmarshaler
// does not inherit the caller's DisallowUnknownFields.
func (c *Capability) UnmarshalJSON(data []byte) error {
	type plain Capability
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(c)); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	if name, ok := keys["name"]; ok && bytes.Equal(bytes.TrimSpace(name), []byte("null")) {
		return fmt.Errorf("capability name must be a string")
	}
	_, c.configSet = keys["config"]
	_, c.optionalSet = keys["optional"]
	_, c.groupSet = keys["group"]
	if _, stated := keys["source"]; stated && c.Source == nil {
		return fmt.Errorf("capability source must be an object")
	}
	if c.groupSet {
		for _, key := range []string{"type", "name", "description", "optional", "config"} {
			if _, ok := keys[key]; ok {
				return fmt.Errorf("group item must not state %s", key)
			}
		}
	}
	return nil
}

// ConfigStated reports whether the document carried a `config` key,
// including the null and empty spellings the decoded map loses.
func (c Capability) ConfigStated() bool { return c.configSet || c.Config != nil }

// Well-known capability types. Policy-shaped types appear at most once
// per descriptor; instance-shaped types (credential, volume, host-mount, port,
// usb-device, agent-skills) appear once per thing requested.
const (
	// CapabilityNetworkPolicy is the phase-scoped egress policy; config
	// decodes to PhasedNetwork.
	CapabilityNetworkPolicy = "com.docker.sandbox/network-policy@1"

	// CapabilityNetworkPolicyV2 is the phase-scoped egress policy with
	// HTTP rules; config decodes to PhasedNetworkV2. A descriptor
	// declares one network-policy version, never both: the two describe
	// the same grant, and a host merging them would have to guess which
	// one bounds the other.
	CapabilityNetworkPolicyV2 = "com.docker.sandbox/network-policy@2"

	// CapabilityGitIdentity shares runtime-provided Git attribution, not credentials.
	CapabilityGitIdentity = "com.docker.sandbox/git-identity@1"

	// CapabilityCredential is one service the workload authenticates to;
	// config decodes to Credential. Instance-shaped, keyed by
	// (service, phase).
	CapabilityCredential = "com.docker.sandbox/credential@1"

	// CapabilitySSHAgent exposes an SSH agent to one phase, relaying to a
	// backing agent of the runtime's choosing; config decodes to SSHAgent.
	// Instance-shaped, keyed by phase.
	CapabilitySSHAgent = "com.docker.sandbox/ssh-agent@1"

	// CapabilityVolume is one persistent (or tmpfs) path; config decodes to
	// Volume. Instance-shaped, keyed by path.
	CapabilityVolume = "com.docker.sandbox/volume@1"

	// CapabilityHostMount shares a runtime-owned host directory across
	// sandboxes of one Kit. Its path key is shared with CapabilityVolume.
	CapabilityHostMount = "com.docker.sandbox/host-mount@1"

	// CapabilityPort is one in-container port to publish; config decodes to
	// Port. Instance-shaped, keyed by container port/transport.
	CapabilityPort = "com.docker.sandbox/port@1"

	// CapabilityAgentSkills declares one discovery path for bundled and
	// available host-shared skills; config decodes to AgentSkills.
	// Instance-shaped, keyed by path: a composition hosting two agents
	// that read skills from different places declares one entry each.
	CapabilityAgentSkills = "com.docker.sandbox/agent-skills@1"

	// CapabilityAgentSkill exposes one image-bundled skill, keyed by its effective name.
	CapabilityAgentSkill = "com.docker.sandbox/agent-skill@1"

	// CapabilityUSBDevice is one device passthrough request; config decodes
	// to USBDevice. Instance-shaped.
	CapabilityUSBDevice = "com.docker.sandbox/usb-device@1"

	// CapabilityResources constrains CPU, memory, and GPU; config decodes to
	// Resources.
	CapabilityResources = "com.docker.sandbox/resources@1"

	// CapabilityPrivileged requests elevated privilege; no config. Present or
	// absent — the host may refuse.
	CapabilityPrivileged = "com.docker.sandbox/privileged@1"

	// CapabilityLifecycle asks the engine to execute the kit's setup
	// across the sandbox's life: install hooks once at create, startup
	// hooks every boot, files written at start, and the interactive
	// argv tail for TTY sessions. A capability rather than
	// grammar fields so a host that cannot execute them refuses the kit
	// by name at preflight instead of silently never running its setup.
	// Like agent-sessions, not permission surface: hooks and files run
	// inside the sandbox on the entrypoint's trust plane.
	CapabilityLifecycle = "com.docker.sandbox/lifecycle@1"

	// CapabilityAgentContext asks the host to surface written instruction
	// content to the agent (the AGENTS.md family). A host with no such
	// concept skips an optional declaration or refuses a required one.
	CapabilityAgentContext = "com.docker.sandbox/agent-context@1"

	// CapabilityAgentSessions declares the workload's session control surface:
	// the argv shapes a harness uses to drive the agent — run one prompt
	// headlessly, list past sessions, resume or continue one. Not a
	// grant: the host consumes it to operate the agent, the way it
	// consumes the image config's entrypoint. Workload kits only in
	// practice — the agent the verbs drive is the workload's.
	CapabilityAgentSessions = "com.docker.sandbox/agent-sessions@1"

	// CapabilitySbx declares that a workload targets the sandbox agent
	// platform: the host launches the agent rather than letting the image
	// entrypoint be PID 1, honors the identity the image config states,
	// and provides the persistent-environment file the image names. The
	// kit's half is the floor that makes those duties performable — a
	// shell, bash, and a resolvable user — which the kit conformance
	// suite judges from the artifact. Not a grant: nothing crosses the
	// boundary the gate guards. No config; the identity lives in the
	// image config, where images already express it.
	CapabilitySbx = "com.docker.sandbox/sbx@1"

	// CapabilityLongRunning keeps a workload running independently of client
	// sessions. Workloads and mixins can request it; it takes no config.
	CapabilityLongRunning = "com.docker.sandbox/long-running@1"

	// CapabilityKitRegistry requests the runtime's kit registry — the daemon's
	// registry facade over the engine image store, where kit builds push
	// results and pull the kit frontend. Reaching it is the host's
	// answer (routing, address, transport), never an address the kit
	// guesses; the request is surfaced because image-store access is a
	// grant, not a side effect of network egress. No config.
	CapabilityKitRegistry = "com.docker.sandbox/kit-registry@1"
)

// PhasedNetwork is CapabilityNetworkPolicy's config: egress by phase — install
// is open while the install hooks run and closed before the agent
// starts; runtime is what the agent gets.
type PhasedNetwork struct {
	Install *NetworkRules `json:"install,omitempty" yaml:"install,omitempty"`
	Runtime *NetworkRules `json:"runtime,omitempty" yaml:"runtime,omitempty"`
}

// NetworkRules is an allow/deny pair of domain patterns. Deny wins.
type NetworkRules struct {
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty" yaml:"deny,omitempty"`
}

// PhasedNetworkV2 is CapabilityNetworkPolicyV2's config: PhasedNetwork's
// phases, each an allow/deny pair of entries rather than of bare hosts.
type PhasedNetworkV2 struct {
	Install *NetworkRulesV2 `json:"install,omitempty" yaml:"install,omitempty"`
	Runtime *NetworkRulesV2 `json:"runtime,omitempty" yaml:"runtime,omitempty"`
}

// NetworkRulesV2 is one phase's egress. Deny wins, as in @1.
type NetworkRulesV2 struct {
	Allow []NetworkEntry `json:"allow,omitempty" yaml:"allow,omitempty"`
	Deny  []NetworkEntry `json:"deny,omitempty" yaml:"deny,omitempty"`
}

// NetworkEntry is one egress grant: the hosts it names, and optionally
// the methods and paths it is bounded to.
//
// A bare entry — hosts and nothing else — grants the connection, which is
// the whole of what @1 could say, and is written as a plain string. An
// entry stating methods or paths grants only matching HTTP requests; the
// connection carries no other traffic, so what the runtime cannot read as
// HTTP it refuses.
//
// One list rather than a host list beside a separate HTTP block: the two
// would name the same host twice and leave the wider grant overriding the
// narrower, which is the opposite of what declaring the narrower means.
type NetworkEntry struct {
	// Hosts are the domain patterns this entry names. A bounded entry in
	// an Allow list names them literally, because a pattern cannot be
	// bounded and unbounded at once; Deny entries and unbounded entries
	// may name patterns.
	Hosts []string `json:"hosts" yaml:"hosts"`

	// Methods are uppercase HTTP method tokens, or the single entry
	// MethodAny. Stating any of them bounds the entry.
	Methods []string `json:"methods,omitempty" yaml:"methods,omitempty"`

	// Paths are globs matched against the request path. Stating one
	// requires stating Methods, so a path always says which methods it
	// bounds; omitted alongside methods, it means every path.
	Paths []string `json:"paths,omitempty" yaml:"paths,omitempty"`
}

// Bounded reports whether the entry narrows its hosts to particular
// requests rather than granting the connection outright.
func (e NetworkEntry) Bounded() bool { return len(e.Methods) > 0 || len(e.Paths) > 0 }

// UnmarshalJSON accepts a bare host string as shorthand for an unbounded
// entry, so a kit that bounds nothing writes the @1 list it always wrote.
// Object form decodes strictly, as every other well-known config does —
// a custom unmarshaler does not inherit the caller's DisallowUnknownFields.
func (e *NetworkEntry) UnmarshalJSON(data []byte) error {
	var host string
	if err := json.Unmarshal(data, &host); err == nil {
		*e = NetworkEntry{Hosts: []string{host}}
		return nil
	}
	// A distinct type, or decoding recurses into this method.
	type entry NetworkEntry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var v entry
	if err := dec.Decode(&v); err != nil {
		return err
	}
	// json decodes null into a nil slice, which validation cannot tell
	// from an omitted field — and omission is what carries the wildcard,
	// so a stated methods: null would read as "every method", the
	// opposite of what writing the key at all suggests. Normalizing it to
	// an empty list keeps the distinction alive: stated-but-empty is
	// precisely what validation rejects, and it reports the mistake with
	// the whole entry in view, where the right remedy depends on what
	// else the entry says.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for field, list := range map[string]*[]string{"hosts": &v.Hosts, "methods": &v.Methods, "paths": &v.Paths} {
		if stated, ok := raw[field]; ok && bytes.Equal(bytes.TrimSpace(stated), []byte("null")) {
			*list = []string{}
		}
	}
	*e = NetworkEntry(v)
	return nil
}

// MarshalJSON writes the shorthand back as a string, so a descriptor
// round-trips to what its author wrote instead of growing an object form
// they never used.
//
// A stated-but-empty list is the one thing this type cannot write back:
// Bounded reads it as unbounded, the shorthand has nowhere to put it, and
// omitempty drops it from the object form. Either way the entry would
// come out wider than it went in, laundering something validation rejects
// into something it accepts, so it is refused rather than serialized.
func (e NetworkEntry) MarshalJSON() ([]byte, error) {
	for _, f := range []struct {
		name string
		list []string
	}{{"methods", e.Methods}, {"paths", e.Paths}} {
		if f.list != nil && len(f.list) == 0 {
			return nil, fmt.Errorf("network entry states an empty %s list; omit the field or give it a value", f.name)
		}
	}
	if !e.Bounded() && len(e.Hosts) == 1 {
		return json.Marshal(e.Hosts[0])
	}
	type entry NetworkEntry
	return json.Marshal(entry(e))
}

// MethodAny is the NetworkEntry.Methods entry naming every method. It
// exists so a path-scoped entry can say "any method" explicitly rather
// than by omitting the field, which would leave the entry unbounded.
const MethodAny = "ANY"

// Credential is CapabilityCredential's config: one service the workload
// authenticates to and how the proxy presents proof — never where the
// secret lives. Phase-scoped like network: an install-only credential is
// injected only while install runs and is gone before the workload starts.
// An entry declaring both install and runtime uses the same configuration
// in both phases.
type Credential struct {
	// Service is the identifier in the host credential store.
	Service string `json:"service" yaml:"service"`

	// Phase accepts one phase or a non-empty list of distinct phases.
	Phase Phases `json:"phase" yaml:"phase"`

	// APIKey configures header injection of an API key.
	APIKey *APIKey `json:"apiKey,omitempty" yaml:"apiKey,omitempty"`

	// OAuth configures proxy-managed OAuth token interception.
	OAuth *OAuth `json:"oauth,omitempty" yaml:"oauth,omitempty"`
}

// SSHAgent is CapabilitySSHAgent's config. Phase accepts one phase or a
// non-empty list; each phase gets the same signing rules.
type SSHAgent struct {
	Phase SSHAgentPhases `json:"phase" yaml:"phase"`
	// Unrestricted defaults to true. False requires at least one bound.
	Unrestricted *bool    `json:"unrestricted,omitempty" yaml:"unrestricted,omitempty"`
	Sign         []string `json:"sign,omitempty" yaml:"sign,omitempty"`
	Authenticate []string `json:"authenticate,omitempty" yaml:"authenticate,omitempty"`
}

// SSHAgentPhases is the phase list used by SSH-agent configurations.
type SSHAgentPhases = Phases

// Phases accepts a scalar or list, preserving the scalar spelling when
// encoding a single-phase entry. Credentials and SSH agents share this grammar.
type Phases []string

func (p *Phases) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*p = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("phase must be a string or list of strings: %w", err)
	}
	*p = many
	return nil
}

func (p Phases) MarshalJSON() ([]byte, error) {
	if len(p) == 1 {
		return json.Marshal(p[0])
	}
	return json.Marshal([]string(p))
}

// Bounded reports whether the entry limits signatures to named purposes.
func (a SSHAgent) Bounded() bool { return a.Unrestricted != nil && !*a.Unrestricted }

// APIKey configures proxy injection of an API key on outbound requests.
type APIKey struct {
	// Name is the in-container env var, set to a sentinel when
	// proxy-managed. Empty declares an inject-only credential: Inject
	// rules are then required, and no variable is created for it.
	Name string `json:"name" yaml:"name"`

	// ProxyManaged keeps the real value on the host. A named key's
	// variable carries a sentinel; an inject-only key (empty Name with
	// Inject rules) has no in-container presence at all. Outbound, the
	// boundary presents the real value either way.
	ProxyManaged bool `json:"proxyManaged,omitempty" yaml:"proxyManaged,omitempty"`

	// Inject lists the outbound domains the proxy rewrites.
	Inject []Inject `json:"inject,omitempty" yaml:"inject,omitempty"`
}

// Inject is one proxy header-injection rule.
type Inject struct {
	Domain string `json:"domain" yaml:"domain"`
	Header string `json:"header,omitempty" yaml:"header,omitempty"`
	// Format renders the credential into the header value, e.g. "Bearer %s".
	Format string `json:"format,omitempty" yaml:"format,omitempty"`
	// Scheme selects a non-header presentation, e.g. "basic".
	Scheme string `json:"scheme,omitempty" yaml:"scheme,omitempty"`
	// Username is the basic-auth username when Scheme is "basic".
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
}

// OAuth configures the proxy's OAuth interception for one service.
type OAuth struct {
	TokenEndpoint  *TokenEndpoint  `json:"tokenEndpoint,omitempty" yaml:"tokenEndpoint,omitempty"`
	ResourceHosts  []string        `json:"resourceHosts,omitempty" yaml:"resourceHosts,omitempty"`
	Sentinels      *Sentinels      `json:"sentinels,omitempty" yaml:"sentinels,omitempty"`
	CredentialFile *CredentialFile `json:"credentialFile,omitempty" yaml:"credentialFile,omitempty"`
	ResponseFields *ResponseFields `json:"responseFields,omitempty" yaml:"responseFields,omitempty"`
	// Passthrough returns the real token to the container: a downgrade.
	Passthrough bool `json:"passthrough,omitempty" yaml:"passthrough,omitempty"`
}

// TokenEndpoint names the OAuth token endpoint the proxy intercepts.
type TokenEndpoint struct {
	Host string `json:"host" yaml:"host"`
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

// Sentinels are the placeholder tokens rendered in-container.
type Sentinels struct {
	AccessToken  string `json:"accessToken,omitempty" yaml:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty" yaml:"refreshToken,omitempty"`
}

// CredentialFile renders sentinels into a file the agent reads.
type CredentialFile struct {
	Path string `json:"path" yaml:"path"`
	// Structure is a declarative nested map, encoded after placeholder
	// substitution in the encoding Format selects, so its output is
	// well-formed regardless of the values. Leaf strings may reference
	// {{.AccessToken}} and {{.RefreshToken}} (strings), {{.ExpiresAt}}
	// (a number), {{.Scopes}} (an array), and {{.PrimaryApiKey}} (a
	// string whose enclosing key is omitted when no key is captured);
	// each placeholder renders in the target encoding's own type.
	// Unknown placeholders are errors.
	Structure map[string]any `json:"structure,omitempty" yaml:"structure,omitempty"`
	// Format selects the encoding of the substituted structure: "json"
	// (the default when empty) or "toml", for agents that read TOML
	// credential files (devin). Encoding stays declarative either way —
	// nested maps become TOML tables, {{.ExpiresAt}} a TOML integer, and
	// {{.Scopes}} a TOML array.
	Format string `json:"format,omitempty" yaml:"format,omitempty"`
}

// ResponseFields maps the provider's token-response field names. The
// refresh mapping is what lets a runtime mask the real refresh token when
// the provider spells the field nonstandardly (cursor's camelCase
// refreshToken) or reuses one field for both roles (devin's token).
type ResponseFields struct {
	AccessToken  string `json:"accessToken,omitempty" yaml:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty" yaml:"refreshToken,omitempty"`
	ExpiresIn    string `json:"expiresIn,omitempty" yaml:"expiresIn,omitempty"`
}

// AgentSkills declares Path as a destination for selected AgentSkill bundles
// and for the host's shared skills when available and enabled.
//
// Only the kit knows where its agent looks: an agent shipped behind a
// wrapper, or one the runtime has never heard of, reads skills from a
// path no table on the host side can predict. Declaring the path is what
// makes the store reach kits the runtime does not recognize.
//
// Mode is the most access the kit is willing to take, not a demand. The
// host's own setting narrows it further and can withhold the mount
// entirely without making the request unsatisfiable. Mode does not constrain
// bundled skills. Neither side can exceed the other, so a kit that only reads
// skills says so and never receives write access on a permissive host.
type AgentSkills struct {
	Path string `json:"path" yaml:"path"`
	// Mode is "readonly" (default) or "readwrite".
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// AgentSkillsCapability is one skills request with the entry fields a
// runtime needs to answer it.
type AgentSkillsCapability struct {
	AgentSkills
	Optional    bool
	Name        string
	Description string
}

// Skills access modes.
const (
	SkillsReadOnly  = "readonly"
	SkillsReadWrite = "readwrite"
)

// SkillsMode reports the access a request names, resolving the default.
func SkillsMode(s AgentSkills) string {
	if s.Mode == "" {
		return SkillsReadOnly
	}
	return s.Mode
}

// SkillsWritable reports whether a request asks for write access.
//
// Access is carried in a separate surface field rather than folded into
// the path string: a path is arbitrary text, so any suffix marking write
// could also be a real path, and a grant for one would silently cover a
// request for the other.
func SkillsWritable(s AgentSkills) bool {
	return SkillsMode(s) == SkillsReadWrite
}

// Volume is CapabilityVolume's config: one persistent (or tmpfs) path
// and its characteristics.
type Volume struct {
	Path string `json:"path" yaml:"path"`
	Size string `json:"size,omitempty" yaml:"size,omitempty"`
	// Tmpfs makes the path a tmpfs mount instead of a block volume.
	Tmpfs bool `json:"tmpfs,omitempty" yaml:"tmpfs,omitempty"`
	// Mode is an octal permission string, e.g. "1777".
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// HostMount requests host-shared storage at an in-container path. The
// host location and provisioning mechanism belong to the runtime.
type HostMount struct {
	Path string `json:"path" yaml:"path"`
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// Port is CapabilityPort's config: one in-container port to publish.
type Port struct {
	Name      string `json:"name,omitempty" yaml:"name,omitempty"`
	Container int    `json:"container" yaml:"container"`
	Transport string `json:"transport,omitempty" yaml:"transport,omitempty"`
}

// USBDevice is CapabilityUSBDevice's config: one passthrough match by
// vendor/product or class.
type USBDevice struct {
	VendorID  string `json:"vendorId,omitempty" yaml:"vendorId,omitempty"`
	ProductID string `json:"productId,omitempty" yaml:"productId,omitempty"`
	Class     string `json:"class,omitempty" yaml:"class,omitempty"`
}

// Resources is CapabilityResources's config: the workload's compute.
type Resources struct {
	CPU    float64 `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory string  `json:"memory,omitempty" yaml:"memory,omitempty"`
	GPU    string  `json:"gpu,omitempty" yaml:"gpu,omitempty"`
}

// Placeholders the host substitutes into agent-sessions argv before
// execution. The {{.X}} shape matches the credential-file structure
// placeholders — one substitution vocabulary across the grammar.
const (
	SessionPromptPlaceholder = "{{.Prompt}}"
	SessionIDPlaceholder     = "{{.SessionID}}"
)

// AgentSessions is CapabilityAgentSessions's config. Prompt, Resume, and
// Continue are argv tails appended to the workload's launch command the
// way user-supplied args are: Prompt runs one prompt non-interactively
// and must reference {{.Prompt}}; Resume reopens a named session and
// must reference {{.SessionID}}; Continue reopens the most recent
// session. List is a complete command whose stdout enumerates resumable
// session ids, one per line, most recent first. Every verb is optional
// — an absent verb means the agent has no such operation — but a
// declaration with no verbs at all says nothing and is invalid.
type AgentSessions struct {
	Prompt   []string    `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Resume   []string    `json:"resume,omitempty" yaml:"resume,omitempty"`
	Continue []string    `json:"continue,omitempty" yaml:"continue,omitempty"`
	List     CommandLine `json:"list,omitempty" yaml:"list,omitempty"`
}

// Arg declares one installer-supplied value. Private by default: it reaches
// the running container only through Env, and the build only through
// BuildArg. The two are mutually exclusive because they resolve in different
// phases.
type Arg struct {
	// Default is substituted when the caller supplies nothing. A nil
	// default with Required unset still means the arg is optional and
	// expands to the empty string only if referenced; declaring Required
	// alongside Default is an error.
	Default *string `json:"default,omitempty" yaml:"default,omitempty"`

	// Required marks a value the caller must supply.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`

	// Description is shown wherever a kit's inputs are listed.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Enum restricts the value to an exact set. Exclusive with Pattern.
	Enum []string `json:"enum,omitempty" yaml:"enum,omitempty"`

	// Pattern restricts the value to an RE2 regexp matched against the
	// whole value. Exclusive with Enum.
	Pattern string `json:"pattern,omitempty" yaml:"pattern,omitempty"`

	// Env opts the resolved value into the container environment under
	// this variable name.
	Env string `json:"env,omitempty" yaml:"env,omitempty"`

	// BuildArg resolves the arg at build instead: the frontend validates
	// it, hands it to the companion dockerfile under this build-arg name,
	// and expands its references into the published descriptor.
	BuildArg string `json:"buildArg,omitempty" yaml:"buildArg,omitempty"`
}

// CommandLine accepts either a YAML string (run via "sh -c") or a list of
// argv elements.
type CommandLine []string

// UnmarshalYAML implements the string-or-list flexibility.
func (c *CommandLine) UnmarshalYAML(unmarshal func(any) error) error {
	var list []string
	if err := unmarshal(&list); err == nil {
		*c = list
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("command must be a string or a list of strings")
	}
	*c = []string{"sh", "-c", s}
	return nil
}

// UnmarshalJSON mirrors the YAML flexibility on the JSON wire: lifecycle
// hooks live in a capability config, and configs reach their typed
// structs through a JSON round-trip (DecodeCapabilityConfig), so the
// string form must survive both decoders.
func (c *CommandLine) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*c = list
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("command must be a string or a list of strings")
	}
	*c = []string{"sh", "-c", s}
	return nil
}

// InstallHook runs once per sandbox at create. It sees only the environment
// it declares — deny by default — which is hygiene first and also makes its
// inputs enumerable.
type InstallHook struct {
	Command     CommandLine `json:"command" yaml:"command"`
	User        string      `json:"user,omitempty" yaml:"user,omitempty"`
	Env         []string    `json:"env,omitempty" yaml:"env,omitempty"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
}

// StartupHook runs every boot and must tolerate being re-run.
type StartupHook struct {
	Command     CommandLine `json:"command" yaml:"command"`
	User        string      `json:"user,omitempty" yaml:"user,omitempty"`
	Background  bool        `json:"background,omitempty" yaml:"background,omitempty"`
	Env         []string    `json:"env,omitempty" yaml:"env,omitempty"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
}

// File is written at sandbox start because its content depends on runtime
// state the image cannot contain.
type File struct {
	Path    string `json:"path" yaml:"path"`
	Content string `json:"content" yaml:"content"`
	Mode    string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Overwrite defaults to true; nil means unset. overwrite: false skips
	// the write when the file already exists (e.g. on a persistent volume).
	Overwrite   *bool  `json:"overwrite,omitempty" yaml:"overwrite,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Lifecycle is CapabilityLifecycle's config: everything the engine
// executes on the kit's behalf across the sandbox's life. Install runs
// once per sandbox at create, in dependency order, with the network
// policy's install phase open; a hook sees only the env it declares.
// Startup runs every boot and must tolerate being re-run. Files are
// written at start rather than baked because their content depends on
// runtime state; ${{ kit.args.* }} expands at create, $VAR is left for
// the shell.
type Lifecycle struct {
	Install []InstallHook `json:"install,omitempty" yaml:"install,omitempty"`
	Startup []StartupHook `json:"startup,omitempty" yaml:"startup,omitempty"`
	Files   []File        `json:"files,omitempty" yaml:"files,omitempty"`

	// Interactive is the argv tail appended to the workload's launch
	// command for an interactive (TTY) session — the one piece of
	// runtime config the image config has no native slot for (Cmd is
	// single-valued). It lives in the lifecycle capability because the
	// engine consumes it the way it consumes the hooks: behavior across
	// the sandbox's life, not a grant.
	Interactive []string `json:"interactive,omitempty" yaml:"interactive,omitempty"`
}

// AgentContext is CapabilityAgentContext's config: instruction content
// the agent reads. An explicit Directory and Filename select the agent's
// discovery profile; a workload Filename alone supplies the legacy fallback.
type AgentContext struct {
	// Directory overrides workspace-sibling placement for agents whose
	// startup discovery only reads a dedicated configuration directory.
	Directory string `json:"directory,omitempty" yaml:"directory,omitempty"`
	Filename  string `json:"filename,omitempty" yaml:"filename,omitempty"`
	// ContentFile points at the context body: a context-relative path in
	// the authored descriptor, rewritten by the frontend to the staged
	// in-image path in the published one.
	ContentFile string `json:"contentFile,omitempty" yaml:"contentFile,omitempty"`
	// Content carries small instruction content inline, for content-free
	// kits.
	Content string `json:"content,omitempty" yaml:"content,omitempty"`
}
