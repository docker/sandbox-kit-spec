package spec

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Contribution is one descriptor folded into a merged kit, named by the
// reference its diagnostics should point at.
type Contribution struct {
	// Reference is how the contribution is named in merge errors: a
	// listed kit's consumption reference, or the set's own descriptor.
	Reference string

	// Descriptor is the declarations this contribution brings. A
	// listed kit's is its published descriptor with its create-phase
	// args already resolved (KitArgValues, ExpandCreateArgs). Publication
	// preserves final-environment references; Compose requires those to be
	// expanded before reconciliation.
	Descriptor *Descriptor
}

// MergeResult is the merged kit: the descriptor it publishes, plus the
// agent-context bodies the caller has to stage, which the descriptor
// alone cannot carry.
type MergeResult struct {
	// Descriptor is the merged declaration set, under the derived kind.
	Descriptor *Descriptor

	// ContextSources are agent-context bodies in contribution order. Empty
	// when no contribution declared any context content. Sources with an
	// empty Target are concatenated into the merged entry's contentFile.
	// Each source with a Target must instead be staged separately at that
	// path, preserving its conditional selection boundary. The caller owns
	// reading and staging the bodies; this package only computes the paths.
	ContextSources []ContextSource
}

// ContextSource is one contribution's agent-context body: either an
// in-image path (a published kit's rewritten contentFile) or content
// stated inline.
type ContextSource struct {
	// Reference names the contributor, for the staged body's heading and
	// for errors reading it.
	Reference string

	// Path is an in-image path to read the body from; empty when the
	// body is inline.
	Path string

	// Content is the inline body; empty when Path is set.
	Content string

	// Target is set when publication preserves a separately selectable body.
	Target string
}

// MergeOptions carries what the merge cannot derive from the
// contributions themselves.
type MergeOptions struct {
	// ContextPath is the in-image path the merged agent-context body
	// will be staged at, written into the merged entry's contentFile.
	// Required when any contribution declares context content.
	ContextPath string
}

// Merge folds ordered contributions into the one descriptor a flattened
// kit publishes.
//
// The contributions arrive in composition order — providers before
// requirers, the set's own declarations last — because that order is
// what several of the rules below mean by "first" and "last", and
// deriving it needs the resolver, which does not belong here.
//
// Unconditional context bodies are concatenated at opts.ContextPath;
// grouped publication stages each ContextSource at its separate Target.
// Runtime consumers use Compose and retain the input contributions.
func Merge(contributions []Contribution, opts MergeOptions) (*MergeResult, error) {
	if contributionsNeedDeferredMerge(contributions) {
		return preserveGroups(contributions, opts)
	}
	result, err := mergeDeclarations(contributions)
	if err != nil {
		return nil, err
	}
	if len(result.ContextSources) > 0 {
		if opts.ContextPath == "" {
			return nil, fmt.Errorf("merge: %d agent-context bodies to stage but no ContextPath to stage them at", len(result.ContextSources))
		}
		for i := range result.Descriptor.Capabilities {
			c := &result.Descriptor.Capabilities[i]
			if c.Type == CapabilityAgentContext {
				c.Config["contentFile"] = opts.ContextPath
			}
		}
	}
	return result, nil
}

// Compose reconciles ordered, expanded contributions for runtime use.
// It shares Merge's conflict and reconciliation rules, without staging
// content for publication. Retain the input contributions alongside the
// returned descriptor: capability handlers need the individual declarations
// wherever composition would otherwise erase their provenance.
// The caller resolves dependencies and validates inputs before calling;
// Compose does not perform descriptor validation or registry IO.
func Compose(contributions []Contribution) (*Descriptor, error) {
	result, err := mergeDeclarations(contributions)
	if err != nil {
		return nil, err
	}
	// A reconciled entry can represent several Kits. Keep their sources in
	// the input declarations and diagnostics, not on the effective request.
	for i := range result.Descriptor.Capabilities {
		result.Descriptor.Capabilities[i].Source = nil
	}
	return result.Descriptor, nil
}

func mergeDeclarations(contributions []Contribution) (*MergeResult, error) {
	if len(contributions) == 0 {
		return nil, fmt.Errorf("merge: no contributions")
	}
	for _, c := range contributions {
		if c.Descriptor == nil {
			return nil, fmt.Errorf("merge: contribution %s has no descriptor", c.Reference)
		}
	}

	kind, err := derivedKind(contributions)
	if err != nil {
		return nil, err
	}

	out := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind}
	result := &MergeResult{Descriptor: out}

	mergeRelations(contributions, out)
	if err := mergeLicenses(contributions, out); err != nil {
		return nil, err
	}
	capabilities, sources, err := mergeCapabilities(contributions)
	if err != nil {
		return nil, err
	}
	out.Capabilities = capabilities
	result.ContextSources = sources
	return result, nil
}

// derivedKind reads the merged kit's role off its contributions: one
// workload among them makes the result a workload, and none makes it a
// mixin — a set of overlays is an overlay, composable onto a workload
// the way the kits it lists were.
//
// A contribution declaring kind: set is a set that was never merged;
// nothing here can flatten it, because its content lives in kits this
// function cannot resolve.
func derivedKind(contributions []Contribution) (string, error) {
	workload := ""
	for _, c := range contributions {
		switch c.Descriptor.Kind {
		case KindWorkload:
			if workload != "" {
				return "", fmt.Errorf("merge: %s and %s are both workload kits; a composition has exactly one base", workload, c.Reference)
			}
			workload = c.Reference
		case KindSet:
			return "", fmt.Errorf("merge: %s declares kind: %s; a set has to be merged before it can contribute to one", c.Reference, KindSet)
		}
	}
	if workload == "" {
		return KindMixin, nil
	}
	return KindWorkload, nil
}

// mergeRelations unions the kit-to-kit relations, dropping the requires
// and integrates entries the set answers itself.
//
// Provides are facts about content that travelled in, so they carry
// over: the merged kit really does offer what its parts offered, and
// dropping them would let the same kit compose twice under a different
// reference. Requires are the opposite — an entry the set satisfies
// internally names something the merged kit now contains, and a kit
// cannot satisfy its own requirement (Resolve skips self), so keeping
// it would publish a kit that can never resolve.
// ProvidesIndex maps each capability name the contributions offer to
// every provide offering it, with each bare entry's version
// materialized from its own contribution's fallback.
//
// A name maps to a list rather than one entry because the resolver
// counts OWNERS, not entries: two kits providing one name is refused,
// but a single kit may offer it at several versions, and whichever of
// them satisfied another kit's constraint during resolution has to
// still satisfy it here.
//
// Exported because the set's own declarations are judged against the
// kits it lists before the merge runs, and both readings have to agree
// on what a bare provide's version is — the rule lives here so it
// cannot be restated differently in two places.
func ProvidesIndex(contributions []Contribution) map[string][]Provide {
	provided := map[string][]Provide{}
	for _, c := range contributions {
		if c.Descriptor == nil {
			continue
		}
		for _, s := range c.Descriptor.Provides {
			p, err := ParseProvide(s)
			if err != nil {
				continue // the per-descriptor validator owns malformed entries
			}
			if p.Version == "" && c.Descriptor.Version != "" {
				p.Version = c.Descriptor.Version
			}
			provided[p.Name] = append(provided[p.Name], p)
		}
	}
	return provided
}

// OwnedProvide is a provide with the contribution that offered it, so
// a requirement answered by another kit can be told from one a
// contribution aims at itself.
type OwnedProvide struct {
	Provide
	Owner string
}

// OwnedProvides indexes what the contributions offer, by name, keeping
// the contribution each entry came from.
//
// Exported alongside SatisfiedByOther because ownership is the whole
// of the rule: Resolve skips self, so anything judging a merge has to
// skip it the same way, and two implementations of that would drift.
func OwnedProvides(contributions []Contribution) map[string][]OwnedProvide {
	out := map[string][]OwnedProvide{}
	for _, c := range contributions {
		if c.Descriptor == nil {
			continue
		}
		for _, s := range c.Descriptor.Provides {
			p, err := ParseProvide(s)
			if err != nil {
				continue
			}
			if p.Version == "" && c.Descriptor.Version != "" {
				p.Version = c.Descriptor.Version
			}
			out[p.Name] = append(out[p.Name], OwnedProvide{Provide: p, Owner: c.Reference})
		}
	}
	return out
}

// SatisfiedByOther reports whether a contribution other than by
// satisfies r. A contribution's own provide never does: Resolve skips
// self, so a requirement answered only by its own offer is one nothing
// can answer.
func SatisfiedByOther(owned map[string][]OwnedProvide, r Require, by string) bool {
	for _, o := range owned[r.Name] {
		if o.Owner != by && Satisfies(o.Provide, r) {
			return true
		}
	}
	return false
}

// SatisfiedBy reports whether any provide under the require's name
// holds it. One kit may offer a name at several versions; the
// requirement is met if one of them meets it.
func SatisfiedBy(provided map[string][]Provide, r Require) bool {
	for _, p := range provided[r.Name] {
		if Satisfies(p, r) {
			return true
		}
	}
	return false
}

func mergeRelations(contributions []Contribution, out *Descriptor) {
	owned := OwnedProvides(contributions)
	emitted := map[string]bool{}
	for _, c := range contributions {
		for _, s := range c.Descriptor.Provides {
			p, err := ParseProvide(s)
			if err != nil {
				continue // the per-descriptor validator owns malformed entries
			}
			// A bare provide takes its version from the contribution's
			// own version: field, which §9.2 guarantees a published kit
			// has when any provide lacks one. Materializing it here is
			// what lets an internal constrained require be recognized as
			// satisfied — and what keeps the emitted entry carrying the
			// version of the kit that offered it, rather than silently
			// inheriting the set's own version once the merged
			// descriptor is read with a different fallback.
			if p.Version == "" && c.Descriptor.Version != "" {
				p.Version = c.Descriptor.Version
				// Trimmed first: ParseProvide accepts surrounding
				// whitespace, and extending the original text would
				// turn a legal "shell " into "shell @1.0.0" and fail
				// the merged descriptor at publish. The author's own
				// spelling of the name is kept — p.Name is the
				// qualified form, and requalifying it here would
				// rewrite what the kit published.
				s = strings.TrimSpace(s) + "@" + p.Version
			}
			// Every version an owner offers carries over: one provider
			// per name is the resolver's rule, so a repeat is one kit
			// offering its name at more than one version, and dropping
			// the extras would lose whichever satisfied a requirement.
			// Deduplicated on the materialized entry, which collapses
			// the same name at the same version written two ways.
			key := p.Name + "@" + p.Version
			if !emitted[key] {
				emitted[key] = true
				out.Provides = append(out.Provides, s)
			}
		}
	}

	// Ownership matters: Resolve skips self, so a kit's own provide
	// never satisfies its own requirement, and a merge that let it
	// would drop a requirement the resolver would have failed on —
	// silently repairing an incoherent contribution instead of
	// carrying its incoherence where someone can see it.
	satisfied := func(entry, by string) bool {
		r, err := ParseRequire(entry)
		if err != nil {
			return false
		}
		return SatisfiedByOther(owned, r, by)
	}

	seenRequire := map[string]bool{}
	seenIntegrate := map[string]bool{}
	seenConflict := map[string]bool{}
	for _, c := range contributions {
		for _, s := range c.Descriptor.Requires {
			if satisfied(s, c.Reference) || seenRequire[s] {
				continue
			}
			seenRequire[s] = true
			out.Requires = append(out.Requires, s)
		}
		for _, s := range c.Descriptor.Integrates {
			if satisfied(s, c.Reference) || seenIntegrate[s] {
				continue
			}
			seenIntegrate[s] = true
			out.Integrates = append(out.Integrates, s)
		}
		// Conflicts carry over whole. An entry naming something the set
		// itself provides is incoherence the resolver already refused,
		// so what survives here is about the world outside the set.
		for _, s := range c.Descriptor.Conflicts {
			if seenConflict[s] {
				continue
			}
			seenConflict[s] = true
			out.Conflicts = append(out.Conflicts, s)
		}
	}
}

// mergeLicenses unions the contributions' SPDX identifiers. A flattened
// kit ships every listed kit's content, so a licenses list naming only the
// set author's terms would misreport what the artifact contains — and
// the frontend derives org.opencontainers.image.licenses from this
// field, so the misreport would reach registry tooling.
func mergeLicenses(contributions []Contribution, out *Descriptor) error {
	seen := map[string]bool{}
	for _, c := range contributions {
		for _, l := range c.Descriptor.Licenses {
			if seen[l] {
				continue
			}
			seen[l] = true
			out.Licenses = append(out.Licenses, l)
		}
	}
	sort.Strings(out.Licenses)
	return nil
}

// mergeCapabilities reconciles every contribution's capability list into
// one that satisfies the arity rules of §7.1.
//
// Instance-shaped types union on their own key. Policy-shaped types
// admit one entry, so each has a rule for what several contributors
// asking at once means: network policies union, lifecycle hooks
// concatenate, and the types that describe the whole sandbox rather
// than a grant to it — resources, agent-sessions,
// agent-interactive-sessions — admit one author, because two different
// answers cannot both be the sandbox's.
func mergeCapabilities(contributions []Contribution) ([]Capability, []ContextSource, error) {
	m := &capabilityMerge{
		byKey:    map[string]keyed{},
		optional: map[string]bool{},
	}
	for _, c := range contributions {
		for i, n := range c.Descriptor.Capabilities {
			if n.Group != nil {
				return nil, nil, fmt.Errorf("compose: %s capabilities[%d]: select groups before composition", c.Reference, i)
			}
			reference := fmt.Sprintf("%s capabilities[%d]", c.Reference, i)
			if n.Source != nil {
				reference = fmt.Sprintf("%s (%s %s)", c.Reference, n.Source.Kit, n.Source.Path)
			}
			if n.Type == CapabilityCredential {
				var credential Credential
				if err := decodeForMerge(reference, n, &credential); err != nil {
					return nil, nil, err
				}
				if len(credential.Phase) == 0 {
					return nil, nil, fmt.Errorf("merge: %s: credential phase is empty", reference)
				}
				for _, phase := range credential.Phase {
					one := credential
					one.Phase = Phases{phase}
					entry, err := CapabilityWithConfig(n, one)
					if err != nil {
						return nil, nil, err
					}
					if err := m.add(reference, *entry); err != nil {
						return nil, nil, err
					}
				}
				continue
			}
			if n.Type == CapabilitySSHAgent {
				var a SSHAgent
				if err := decodeForMerge(reference, n, &a); err != nil {
					return nil, nil, err
				}
				if len(a.Phase) == 0 {
					return nil, nil, fmt.Errorf("merge: %s: ssh-agent phase is empty", reference)
				}
				for _, phase := range a.Phase {
					one := a
					one.Phase = SSHAgentPhases{phase}
					entry, err := CapabilityWithConfig(n, one)
					if err != nil {
						return nil, nil, err
					}
					if err := m.add(reference, *entry); err != nil {
						return nil, nil, err
					}
				}
				continue
			}
			if err := m.add(reference, n); err != nil {
				return nil, nil, err
			}
			if n.Type == CapabilityAgentContext {
				m.context[len(m.context)-1].contribution = c.Reference
			}
		}
	}
	return m.finish()
}

// decodeForMerge reads one entry's typed config, explaining the one
// failure an author can act on.
//
// A re-exported arg leaves a placeholder in the merged descriptor,
// which is the point of re-exporting — but a placeholder is a string,
// and the merge has to decode every well-known config to reconcile
// it. Where the field is a string the value rides through; where it
// is a number or a boolean the decode fails, and the raw error talks
// about JSON types rather than about the arg the author re-exported.
func decodeForMerge(reference string, n Capability, out any) error {
	err := DecodeCapabilityConfig(n, out)
	if err == nil {
		return nil
	}
	if capabilityIsParameterized(n) {
		return fmt.Errorf("merge: %s: %s is still parameterized where the merge has to read it, and only a field that holds text can carry a reference this far; pin the arg in kits[].args instead of re-exporting it: %w",
			reference, n.Type, err)
	}
	return fmt.Errorf("merge: %s: %s: %w", reference, n.Type, err)
}

// keyed remembers which contribution first asked for an instance-shaped
// capability, so a second, different ask names both parties.
type keyed struct {
	reference  string
	capability Capability
}

type capabilityMerge struct {
	// order preserves first-seen order for the instance-shaped entries,
	// so the merged list reads in composition order.
	order []string
	byKey map[string]keyed

	// optional tracks whether every asker marked an entry optional. An
	// entry one contribution requires is required in the merged kit:
	// optional says the asker degrades without it, and one that does not
	// degrade decides for the whole.
	optional map[string]bool

	network   []networkAsk
	lifecycle []lifecycleAsk
	context   []contextAsk

	resources *keyed
	sessions  *keyed

	interactiveSessions *keyed
}

type networkAsk struct {
	reference string
	name      string
	v2        bool
	policy    *PhasedNetworkV2
	optional  bool
}

type lifecycleAsk struct {
	reference string
	name      string
	lifecycle *Lifecycle
	optional  bool
}

type contextAsk struct {
	reference string
	// Content lookup uses the consuming Kit, while diagnostics retain member paths.
	contribution string
	name         string
	context      *AgentContext
	optional     bool
}

func (m *capabilityMerge) add(reference string, n Capability) error {
	switch n.Type {
	case CapabilityNetworkPolicy, CapabilityNetworkPolicyV2:
		p, err := NetworkPolicyV2Of([]Capability{n})
		if err != nil {
			return fmt.Errorf("merge: %s: %w", reference, err)
		}
		m.network = append(m.network, networkAsk{
			reference: reference,
			name:      n.Name,
			v2:        n.Type == CapabilityNetworkPolicyV2,
			policy:    p,
			optional:  n.Optional,
		})
		return nil

	case CapabilityLifecycle:
		var l Lifecycle
		if err := decodeForMerge(reference, n, &l); err != nil {
			return err
		}
		m.lifecycle = append(m.lifecycle, lifecycleAsk{reference: reference, name: n.Name, lifecycle: &l, optional: n.Optional})
		return nil

	case CapabilityAgentContext:
		var a AgentContext
		if err := decodeForMerge(reference, n, &a); err != nil {
			return err
		}
		m.context = append(m.context, contextAsk{reference: reference, name: n.Name, context: &a, optional: n.Optional})
		return nil

	case CapabilityResources:
		return mergeSole(&m.resources, reference, n, "resources")

	case CapabilityAgentSessions:
		return mergeSole(&m.sessions, reference, n, "agent-sessions")

	case CapabilityAgentInteractiveSessions:
		// Composed as the original Capability, never re-rendered from the
		// struct: an empty newSession is a verb, and omitempty would drop it.
		return mergeSole(&m.interactiveSessions, reference, n, "agent-interactive-sessions")
	}

	// Everything else unions on the key its type dedups by: the same
	// ask from two kits is one ask, a different ask under the same key
	// is a contradiction only their authors can resolve.
	key, err := m.instanceKey(reference, n)
	if err != nil {
		return err
	}
	prev, seen := m.byKey[key]
	if !seen {
		m.order = append(m.order, key)
		m.byKey[key] = keyed{reference: reference, capability: n}
		m.optional[key] = n.Optional
		return nil
	}
	// A host directory belongs to one declaring Kit, so even an identical
	// restatement cannot collapse: doing so would silently choose whose
	// shared data the other Kit receives. Volumes occupy the same mount
	// destination and cannot substitute for this host-sharing grant.
	if n.Type == CapabilityHostMount || prev.capability.Type == CapabilityHostMount {
		return fmt.Errorf("merge: %s and %s both declare storage at the same path; a host mount has one owner", prev.reference, reference)
	}
	// A credential does not union the way the rest do, identical
	// configs included. Resolve refuses two kits declaring one
	// (service, phase) outright — one credential, one owner — so
	// collapsing two into one here would let a set publish what the
	// resolver would have refused, and the merged kit would carry a
	// credential with no telling which kit answers for it.
	if n.Type == CapabilityCredential {
		return fmt.Errorf("merge: %s and %s both declare %s; one credential has one owner",
			prev.reference, reference, describeCapability(n))
	}
	// One socket per phase serves every kit that asked: the merged entry
	// signs what any of them may, which is why the bounds union rather
	// than conflict.
	if n.Type == CapabilitySSHAgent {
		var held, asked SSHAgent
		if err := decodeForMerge(prev.reference, prev.capability, &held); err != nil {
			return err
		}
		if err := decodeForMerge(reference, n, &asked); err != nil {
			return err
		}
		merged, err := CapabilityWithConfig(prev.capability, mergeSSHAgents(held, asked))
		if err != nil {
			return fmt.Errorf("merge: %s and %s: %w", prev.reference, reference, err)
		}
		if merged.Name == "" {
			merged.Name = n.Name
		}
		m.byKey[key] = keyed{reference: prev.reference, capability: *merged}
		if !n.Optional {
			m.optional[key] = false
		}
		return nil
	}
	if !sameRequest(prev.capability, n) {
		return fmt.Errorf("merge: %s and %s both declare %s but ask for different things; one of them has to change",
			prev.reference, reference, describeCapability(n))
	}
	// Identical asks collapse, and the stricter optionality wins.
	if prev.capability.Name == "" {
		prev.capability.Name = n.Name
		m.byKey[key] = prev
	}
	if !n.Optional {
		m.optional[key] = false
	}
	return nil
}

// mergeSole holds a whole-sandbox type to one author, tolerating an
// identical restatement.
func mergeSole(slot **keyed, reference string, n Capability, label string) error {
	if *slot == nil {
		*slot = &keyed{reference: reference, capability: n}
		return nil
	}
	if sameRequest((*slot).capability, n) {
		if (*slot).capability.Name == "" {
			(*slot).capability.Name = n.Name
		}
		if !n.Optional {
			(*slot).capability.Optional = false
		}
		return nil
	}
	return fmt.Errorf("merge: %s and %s both declare %s; it describes the whole sandbox, so it has one author",
		(*slot).reference, reference, label)
}

// instanceKey is the key a type dedups on, per §7.1.
func (m *capabilityMerge) instanceKey(reference string, n Capability) (string, error) {
	switch n.Type {
	case CapabilityCredential:
		var c Credential
		if err := decodeForMerge(reference, n, &c); err != nil {
			return "", err
		}
		if len(c.Phase) != 1 {
			return "", fmt.Errorf("merge: %s: credential needs exactly one phase after expansion", reference)
		}
		return n.Type + "\x00" + c.Service + "\x00" + c.Phase[0], nil
	case CapabilitySSHAgent:
		// One socket per phase, whichever kits asked: two asks for the
		// same phase are one ask, unlike a credential's single owner.
		var a SSHAgent
		if err := decodeForMerge(reference, n, &a); err != nil {
			return "", err
		}
		if len(a.Phase) != 1 {
			return "", fmt.Errorf("merge: %s: ssh-agent needs exactly one phase after expansion", reference)
		}
		return n.Type + "\x00" + a.Phase[0], nil
	case CapabilityVolume:
		var v Volume
		if err := decodeForMerge(reference, n, &v); err != nil {
			return "", err
		}
		// The destination, not the spelling: validation asks only
		// that a volume path be absolute, so /data/cache and
		// /data/./cache are one mount written two ways, and keying on
		// the text would emit both and leave a runtime to reconcile
		// sizes nobody agreed on.
		return n.Type + "\x00" + path.Clean(v.Path), nil
	case CapabilityHostMount:
		var mount HostMount
		if err := decodeForMerge(reference, n, &mount); err != nil {
			return "", err
		}
		return CapabilityVolume + "\x00" + path.Clean(mount.Path), nil
	case CapabilityAgentSkill:
		var s AgentSkill
		if err := decodeForMerge(reference, n, &s); err != nil {
			return "", err
		}
		return n.Type + "\x00" + AgentSkillName(s), nil
	case CapabilityAgentSkills:
		var s AgentSkills
		if err := decodeForMerge(reference, n, &s); err != nil {
			return "", err
		}
		return n.Type + "\x00" + s.Path, nil
	case CapabilityPort:
		var p Port
		if err := decodeForMerge(reference, n, &p); err != nil {
			return "", err
		}
		return n.Type + "\x00" + PortKey(p), nil
	}
	// Config-less singletons (privileged, kit-registry) key on the type
	// alone, so presence is the union. Unknown and instance-shaped
	// types with no key beyond the entry (usb-device) key on the whole
	// request, which makes an exact duplicate collapse and anything
	// else a distinct ask.
	return capabilityIdentity(n), nil
}

// capabilityIdentity is what makes two entries the same request for
// merging: the type and the whole config, canonically encoded.
//
// Not capabilitySurfaceEntry, which shortens the config to six bytes
// of hash. That is sound for a permission surface, where the value is
// compared against another rendering of itself and displayed to a
// human, but as an identity it means two unrelated requests can
// collide and one be dropped — and an unknown type's config is
// arbitrary, so nothing bounds what might collide.
func capabilityIdentity(n Capability) string {
	// An omitted config and one written as {} are the same request —
	// they are the only two spellings a config-less type has — and
	// they marshal to null and {}, so keying on the encoding alone
	// would let both survive the merge and then fail the singleton
	// rule on a set nobody wrote wrong.
	if len(n.Config) == 0 {
		return n.Type
	}
	canonical, err := json.Marshal(n.Config)
	if err != nil {
		return n.Type + "\x00unencodable"
	}
	return n.Type + "\x00" + string(canonical)
}

// sameRequest reports whether two entries ask for the same thing,
// ignoring the fields that describe rather than request.
//
// Types whose config has defaults or informational fields are compared
// as the request they normalize to, not as the text they were written
// as: port@1 defines a publication by container port and transport, so
// an omitted transport and an explicit tcp are one ask, and two kits
// naming the same port differently are not two publications.
func sameRequest(a, b Capability) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case CapabilityPort:
		var pa, pb Port
		if DecodeCapabilityConfig(a, &pa) == nil && DecodeCapabilityConfig(b, &pb) == nil {
			return PortKey(pa) == PortKey(pb)
		}
	case CapabilityAgentSkill:
		var sa, sb AgentSkill
		if DecodeCapabilityConfig(a, &sa) == nil && DecodeCapabilityConfig(b, &sb) == nil {
			return sa.Path == sb.Path && AgentSkillName(sa) == AgentSkillName(sb)
		}
	case CapabilityAgentSkills:
		var sa, sb AgentSkills
		if DecodeCapabilityConfig(a, &sa) == nil && DecodeCapabilityConfig(b, &sb) == nil {
			return sa.Path == sb.Path && SkillsMode(sa) == SkillsMode(sb)
		}
	case CapabilityVolume:
		var va, vb Volume
		if DecodeCapabilityConfig(a, &va) == nil && DecodeCapabilityConfig(b, &vb) == nil {
			va.Path, vb.Path = path.Clean(va.Path), path.Clean(vb.Path)
			return va == vb
		}
	}
	return capabilityIdentity(a) == capabilityIdentity(b)
}

func describeCapability(n Capability) string {
	return n.Type
}

func (m *capabilityMerge) finish() ([]Capability, []ContextSource, error) {
	var out []Capability

	if network, err := m.mergedNetwork(); err != nil {
		return nil, nil, err
	} else if network != nil {
		out = append(out, *network)
	}

	for _, key := range m.order {
		entry := m.byKey[key].capability
		entry.Optional = m.optional[key]
		out = append(out, entry)
	}

	if m.resources != nil {
		out = append(out, m.resources.capability)
	}
	if m.sessions != nil {
		out = append(out, m.sessions.capability)
	}
	if m.interactiveSessions != nil {
		out = append(out, m.interactiveSessions.capability)
	}

	if lifecycle, err := m.mergedLifecycle(); err != nil {
		return nil, nil, err
	} else if lifecycle != nil {
		out = append(out, *lifecycle)
	}

	context, sources, err := m.mergedContext()
	if err != nil {
		return nil, nil, err
	}
	if context != nil {
		out = append(out, *context)
	}
	return out, sources, nil
}

// mergedNetwork unions every contributor's egress into one entry.
//
// Union is the honest reading of two grants: the merged kit really does
// reach both sets of hosts, and a deny survives from whichever
// contributor stated it, because deny wins at enforcement and dropping
// one would hand the merged kit a grant its author refused.
//
// The output states one version. @2 is chosen whenever any contributor
// uses it, and the @1 rules join it unchanged: an @1 host is an
// unbounded @2 entry, which is the bare-string shorthand NetworkEntry
// already accepts, so nothing is invented by the conversion.
func (m *capabilityMerge) mergedNetwork() (*Capability, error) {
	if len(m.network) == 0 {
		return nil, nil
	}
	v2 := false
	name := ""
	// Required wins, as it does for every other merged entry: optional
	// says its asker degrades without the grant, and one that does not
	// degrade decides for the whole.
	optional := true
	for _, ask := range m.network {
		if name == "" {
			name = ask.name
		}
		if ask.v2 {
			v2 = true
		}
		if !ask.optional {
			optional = false
		}
	}

	merged := &PhasedNetworkV2{}
	for _, ask := range m.network {
		if ask.policy == nil {
			continue
		}
		if ask.policy.Install != nil {
			merged.Install = appendRules(merged.Install, ask.policy.Install)
		}
		if ask.policy.Runtime != nil {
			merged.Runtime = appendRules(merged.Runtime, ask.policy.Runtime)
		}
	}
	normalizeRules(merged.Install)
	normalizeRules(merged.Runtime)

	typ, config := CapabilityNetworkPolicy, any(downgradeToV1(merged))
	if v2 {
		typ, config = CapabilityNetworkPolicyV2, merged
	}
	c, err := capabilityFrom(typ, config)
	if err != nil {
		return nil, err
	}
	c.Optional = optional
	c.Name = name
	return c, nil
}

func appendRules(into *NetworkRulesV2, from *NetworkRulesV2) *NetworkRulesV2 {
	if into == nil {
		into = &NetworkRulesV2{}
	}
	into.Allow = append(into.Allow, from.Allow...)
	into.Deny = append(into.Deny, from.Deny...)
	return into
}

// normalizeRules collapses one phase's entries: identical entries
// become one, and an allow entry bounded to particular methods or paths
// is dropped when another entry grants its host outright.
//
// The drop is not a narrowing. The union of a bounded grant and an
// unbounded one over the same host IS the unbounded one, and keeping
// both is the shape network-policy@2 refuses to express — two entries
// naming one host with the wider silently overriding the narrower.
func normalizeRules(r *NetworkRulesV2) {
	if r == nil {
		return
	}
	r.Allow = dedupeEntries(r.Allow)
	r.Deny = dedupeEntries(r.Deny)

	unbounded := map[string]bool{}
	for _, e := range r.Allow {
		if e.Bounded() {
			continue
		}
		for _, h := range e.Hosts {
			unbounded[h] = true
		}
	}
	kept := r.Allow[:0]
	for _, e := range r.Allow {
		if !e.Bounded() {
			kept = append(kept, e)
			continue
		}
		var hosts []string
		for _, h := range e.Hosts {
			if !unbounded[h] {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) == 0 {
			continue
		}
		e.Hosts = hosts
		kept = append(kept, e)
	}
	r.Allow = kept
}

func dedupeEntries(entries []NetworkEntry) []NetworkEntry {
	if len(entries) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]NetworkEntry, 0, len(entries))
	for _, e := range entries {
		// %q quotes every element, so a separator occurring inside one
		// cannot be read as the boundary between two: paths: ["/a,/b"]
		// and paths: ["/a", "/b"] are different grants and must key
		// differently, or the union silently narrows.
		key := fmt.Sprintf("%q|%q|%q", e.Hosts, e.Methods, e.Paths)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// downgradeToV1 renders a union that no contributor bounded back as an
// @1 policy, so a set of @1 kits publishes the version those kits
// wrote rather than being moved onto @2 by the merge.
func downgradeToV1(p *PhasedNetworkV2) *PhasedNetwork {
	out := &PhasedNetwork{}
	if p.Install != nil {
		out.Install = &NetworkRules{Allow: hostsOf(p.Install.Allow), Deny: hostsOf(p.Install.Deny)}
	}
	if p.Runtime != nil {
		out.Runtime = &NetworkRules{Allow: hostsOf(p.Runtime.Allow), Deny: hostsOf(p.Runtime.Deny)}
	}
	return out
}

func hostsOf(entries []NetworkEntry) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		for _, h := range e.Hosts {
			if seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// mergedLifecycle concatenates the contributors' hooks in composition
// order, which is the order a runtime would have run them in.
//
// The interactive tail is the exception: it is the workload's launch
// argv, not an addition to it, so two contributors stating one cannot
// both be honored.
func (m *capabilityMerge) mergedLifecycle() (*Capability, error) {
	if len(m.lifecycle) == 0 {
		return nil, nil
	}
	merged := &Lifecycle{}
	interactiveFrom := ""
	name := ""
	optional := true
	seenFile := map[string]string{}
	for _, ask := range m.lifecycle {
		if name == "" {
			name = ask.name
		}
		merged.Install = append(merged.Install, ask.lifecycle.Install...)
		merged.Startup = append(merged.Startup, ask.lifecycle.Startup...)
		for _, f := range ask.lifecycle.Files {
			if ContainsArgRef(f.Path) {
				return nil, fmt.Errorf("merge: %s: lifecycle file path %q resolves at create; pin the path", ask.reference, f.Path)
			}
			clean := path.Clean(f.Path)
			if prev, dup := seenFile[clean]; dup {
				return nil, fmt.Errorf("merge: %s and %s: %s is written by two contributions; one file has one author", prev, ask.reference, clean)
			}
			seenFile[clean] = ask.reference
		}
		merged.Files = append(merged.Files, ask.lifecycle.Files...)
		if ask.lifecycle.Interactive != nil {
			if interactiveFrom != "" {
				return nil, fmt.Errorf("merge: %s and %s both declare a lifecycle interactive tail; it replaces the launch command's arguments, so it has one author",
					interactiveFrom, ask.reference)
			}
			interactiveFrom = ask.reference
			merged.Interactive = ask.lifecycle.Interactive
		}
		if !ask.optional {
			optional = false
		}
	}

	c, err := capabilityFrom(CapabilityLifecycle, merged)
	if err != nil {
		return nil, err
	}
	c.Optional = optional
	c.Name = name
	return c, nil
}

// mergedContext folds the contributors' agent-context into one entry:
// the profile filename from whichever contribution owns it, and every
// body collected separately for publishers to stage.
func (m *capabilityMerge) mergedContext() (*Capability, []ContextSource, error) {
	if len(m.context) == 0 {
		return nil, nil, nil
	}

	merged := &AgentContext{}
	filenameFrom := ""
	// Select explicit destinations first so a shell's legacy profile
	// cannot win merely because it arrived before the agent mixin.
	for _, ask := range m.context {
		if ask.context.Directory == "" {
			continue
		}
		if filenameFrom != "" && (merged.Directory != ask.context.Directory || merged.Filename != ask.context.Filename) {
			return nil, nil, fmt.Errorf("merge: %s and %s declare conflicting explicit agent-context profiles", filenameFrom, ask.reference)
		}
		filenameFrom = ask.reference
		merged.Filename = ask.context.Filename
		merged.Directory = ask.context.Directory
	}
	name := ""
	optional := true
	var sources []ContextSource
	for _, ask := range m.context {
		if name == "" {
			name = ask.name
		}
		if ask.context.Filename != "" && merged.Directory == "" {
			// Only a workload states a legacy profile, and a composition
			// has one workload, so two is a set that was mis-assembled
			// rather than a choice to arbitrate.
			if filenameFrom != "" {
				return nil, nil, fmt.Errorf("merge: %s and %s both declare an agent-context filename; the profile belongs to the kit that owns the environment",
					filenameFrom, ask.reference)
			}
			filenameFrom = ask.reference
			merged.Filename = ask.context.Filename
		}
		switch {
		case ask.context.ContentFile != "":
			sources = append(sources, ContextSource{Reference: ask.contribution, Path: ask.context.ContentFile})
		case ask.context.Content != "":
			sources = append(sources, ContextSource{Reference: ask.contribution, Content: ask.context.Content})
		}
		if !ask.optional {
			optional = false
		}
	}

	if merged.Filename == "" && len(sources) == 0 {
		// Every contribution declared the type and stated nothing in
		// it; the entry would ask for nothing.
		return nil, nil, nil
	}

	c, err := capabilityFrom(CapabilityAgentContext, merged)
	if err != nil {
		return nil, nil, err
	}
	c.Optional = optional
	c.Name = name
	return c, sources, nil
}

// capabilityFrom renders a typed config back into the untyped Capability
// the grammar carries, through the same JSON round trip
// DecodeCapabilityConfig reads it with, so the merged entry is exactly
// what a consumer will decode.
func capabilityFrom(typ string, config any) (*Capability, error) {
	raw, err := toConfigMap(config)
	if err != nil {
		return nil, fmt.Errorf("merge: encode %s config: %w", typ, err)
	}
	return &Capability{Type: typ, Config: raw}, nil
}

// CheckReExport holds a set's arg declaration to the contract of the
// kit's arg it stands in for.
//
// A re-export means the set's declaration is the only one an installer
// ever sees: the kit's is answered and gone. So the set's has to say
// at least as much — otherwise a required arg becomes optional and its
// reference goes unresolved at create, or an enum becomes anything at
// all and a value the kit would have refused reaches it with the kit's
// own declaration no longer there to refuse it.
//
// Restating the constraint is the set author's work, and cheap: it is
// the one place an installer can read what the value has to be.
func CheckReExport(kitArg string, kit Arg, setName string, set Arg, declared bool) error {
	if !declared {
		return fmt.Errorf("arg %q is re-exported as %q, which the set does not declare; a re-exported arg is the only declaration an installer sees, so the set has to declare it", kitArg, setName)
	}
	if kit.Required && !set.Required && set.Default == nil {
		return fmt.Errorf("arg %q is required by that kit and re-exported as %q, which is optional with no default; an installer supplying nothing would leave the reference unresolved", kitArg, setName)
	}
	if len(kit.Enum) > 0 {
		allowed := map[string]bool{}
		for _, v := range kit.Enum {
			allowed[v] = true
		}
		if len(set.Enum) == 0 {
			return fmt.Errorf("arg %q is re-exported as %q, which does not restate its enum %v; the kit's declaration is answered and gone, so nothing would hold the value to that set", kitArg, setName, kit.Enum)
		}
		for _, v := range set.Enum {
			if !allowed[v] {
				return fmt.Errorf("arg %q is re-exported as %q, which allows %q; that kit's enum is %v, and a value outside it would reach the kit with nothing left to refuse it", kitArg, setName, v, kit.Enum)
			}
		}
	}
	if kit.Pattern != "" && set.Pattern != kit.Pattern {
		return fmt.Errorf("arg %q is re-exported as %q, which does not restate its pattern %q; the kit's declaration is answered and gone, so nothing would hold the value to that shape", kitArg, setName, kit.Pattern)
	}
	if kit.Default != nil && set.Default == nil && !set.Required {
		return fmt.Errorf("arg %q is re-exported as %q, which is optional with no default; that kit defaults it to %q, and an installer supplying nothing would leave the reference unresolved rather than fall back", kitArg, setName, *kit.Default)
	}
	return nil
}

// KitArgValues resolves one listed kit's create-phase args from the
// values its set supplied, separating the two kinds of value a set may
// give.
//
// A literal value is validated against the kit's own enum or pattern and
// expanded into the merged descriptor, which is what pins the set. A
// value that is itself a `${{ kit.args.* }}` reference cannot be
// validated here — it has no value yet — so it lands where the kit's
// placeholder was and the set's own declaration bounds it at create.
// That is the same two-phase judgment a parameterized capability entry
// gets, applied to a listed kit's inputs.
func KitArgValues(decls map[string]Arg, supplied map[string]string) (map[string]string, error) {
	create := map[string]Arg{}
	for name, decl := range decls {
		if decl.BuildArg == "" {
			create[name] = decl
		}
	}

	literal := map[string]string{}
	deferred := map[string]string{}
	for name, value := range supplied {
		if _, declared := create[name]; !declared {
			if _, isBuild := decls[name]; isBuild {
				return nil, fmt.Errorf("arg %q resolved at that kit's build and is already baked into its published descriptor; drop it", name)
			}
			return nil, fmt.Errorf("that kit declares no arg %q", name)
		}
		switch {
		case IsWholeArgRef(value):
			// A re-export: the set's own declaration bounds it, and
			// this kit's enum or pattern cannot judge a value that
			// does not exist yet.
			deferred[name] = value
		case ContainsArgRef(value):
			// A reference inside a larger string is bounded by
			// nobody: this kit's declaration cannot judge it now, and
			// the set's declaration bounds the substituted part
			// rather than the whole the kit will receive.
			return nil, fmt.Errorf("arg %q is set to %q, which embeds a reference in a larger value; supply a literal, or exactly one ${{ kit.args.* }} reference to re-export it", name, value)
		default:
			literal[name] = value
		}
	}

	// The deferred names are withheld from ResolveArgs, or a required
	// arg re-exported by reference would read as unsupplied and a
	// pattern would be matched against the placeholder text.
	remaining := map[string]Arg{}
	for name, decl := range create {
		if _, isDeferred := deferred[name]; !isDeferred {
			remaining[name] = decl
		}
	}
	values, err := ResolveArgs(remaining, literal)
	if err != nil {
		return nil, err
	}
	for name, value := range deferred {
		values[name] = value
	}
	return values, nil
}
