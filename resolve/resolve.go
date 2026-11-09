package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Unit is one kit in the set: its published descriptor plus the identity it
// was consumed by.
type Unit struct {
	// Reference is how the caller named the kit: repo:tag, repo@digest, a
	// path, or a git URL. It is the kit's identity in every error message
	// and in the lock.
	Reference string

	// Digest pins the manifest (the index for multi-platform kits).
	Digest string

	// Image is the runtime-resolvable image reference for this kit's
	// content: the digest-pinned registry reference for registry kits, or
	// the store-local tag a source-form kit (directory, git) was built
	// under. Distinct from Reference because a source kit's identity is
	// its source, not the name its build output happens to carry.
	Image string

	// Descriptor is already validated. Callers that resolve create arguments
	// before resolution supply the expanded descriptor here.
	Descriptor *spec.Descriptor

	// Args are the create-phase values resolved for this kit, recorded in
	// the lock so recreate expands the same hooks the same way.
	Args map[string]string

	// Env holds this kit's resolved create-phase env: argument exports when
	// supplied by fetch resolution. It excludes image defaults and runtime
	// overrides and is nil when no arguments export environment variables.
	Env map[string]string
}

// Resolution is a validated, ordered set.
type Resolution struct {
	// Workload is the one kind:workload unit — the base of the
	// composition. Nil only for a partial set (ResolvePartial), which
	// composes onto a workload later.
	Workload *Unit

	// Mixins are the remaining units in composition order: providers
	// before requirers, ties broken lexicographically by reference.
	Mixins []*Unit

	// topological is the order the dependency graph derived, with the
	// workload wherever its own relations put it.
	topological []*Unit
}

// Topological returns the set in the order its dependency graph
// derived: every provider before the kits that require or integrate
// with it, the workload included.
//
// Ordered answers a different question — what lands on what — and so
// always starts with the workload, because a workload's layers are
// the filesystem the overlays land on whatever it happens to require.
// Anything reconciling DECLARATIONS wants this one instead: a
// workload that requires a mixin's capability should see that mixin's
// hooks run before its own, which is exactly what the graph says and
// what the layer order cannot.
func (r *Resolution) Topological() []*Unit {
	return append([]*Unit{}, r.topological...)
}

// Ordered returns the workload followed by the mixins in composition order.
// A partial set (ResolvePartial, no workload) yields the mixins alone.
func (r *Resolution) Ordered() []*Unit {
	if r.Workload == nil {
		return append([]*Unit{}, r.Mixins...)
	}
	return append([]*Unit{r.Workload}, r.Mixins...)
}

// Resolve validates the closed set and derives its composition order.
// Capabilities must already be selected and flattened; use Dependencies
// to order declarations that still contain groups before selection.
// Every violation in the set is reported, not just the first: the caller
// assembled the set by hand and deserves the full picture in one pass.
func Resolve(units []*Unit) (*Resolution, error) {
	return resolve(units, false, true)
}

// ResolvePartial is Resolve for a set that does not have to be runnable:
// every coherence rule holds, but a set with no workload is judged
// complete rather than incomplete.
//
// It exists for the one caller that composes kits without launching
// them — a kit whose content is a list of other kits (spec.Kit). A set
// of overlays is a legitimate thing to publish: it composes onto a
// workload later, exactly as those kits would have. More than one
// workload stays an error, because that is incoherent whether or not
// anything runs.
//
// The returned Resolution's Workload is nil for a partial set, so
// Ordered() yields the mixins alone.
func ResolvePartial(units []*Unit) (*Resolution, error) {
	return resolve(units, true, true)
}

// Dependencies validates Kit relationships and derives declaration order before
// capability selection. Capability coherence is checked on selected entries by
// Resolve/ResolvePartial or spec.Compose, never on rejected contributions.
func Dependencies(units []*Unit, partial bool) (*Resolution, error) {
	return resolve(units, partial, false)
}

func resolve(units []*Unit, partial, capabilities bool) (*Resolution, error) {
	if len(units) == 0 {
		return nil, fmt.Errorf("resolve: empty kit set")
	}
	for _, u := range units {
		if u.Descriptor == nil {
			return nil, fmt.Errorf("resolve: kit %s has no descriptor", u.Reference)
		}
	}

	var problems []string
	addProblem := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	workload := checkOneWorkload(units, addProblem, partial)
	provides := providesIndex(units, addProblem)
	checkOneProviderPerName(provides, addProblem)
	if capabilities {
		for _, u := range units {
			for i, c := range u.Descriptor.Capabilities {
				if c.Group != nil {
					addProblem("%s capabilities[%d]: select groups before capability-coherent resolution; use Dependencies to order declarations before selection", u.Reference, i)
				}
			}
		}
		checkOneCredentialOwner(units, addProblem)
	}
	checkRequires(units, provides, addProblem)
	checkIntegrates(units, provides, addProblem)
	checkConflicts(units, provides, addProblem)

	if len(problems) > 0 {
		return nil, fmt.Errorf("resolve: the kit set is not coherent:\n  - %s", strings.Join(problems, "\n  - "))
	}

	ordered, err := order(units, provides)
	if err != nil {
		return nil, err
	}

	mixins := make([]*Unit, 0, len(ordered)-1)
	for _, u := range ordered {
		if u != workload {
			mixins = append(mixins, u)
		}
	}
	return &Resolution{Workload: workload, Mixins: mixins, topological: ordered}, nil
}

// checkOneWorkload finds the set's base. When partial, an absent
// workload is allowed: the caller is composing kits rather than
// launching them, so a set of overlays is complete as it stands.
func checkOneWorkload(units []*Unit, addProblem func(string, ...any), partial bool) *Unit {
	var workload *Unit
	for _, u := range units {
		if u.Descriptor.Kind != spec.KindWorkload {
			continue
		}
		if workload != nil {
			addProblem("more than one workload kit: %s and %s both declare kind: workload; a composition has exactly one base", workload.Reference, u.Reference)
			continue
		}
		workload = u
	}
	if workload == nil && !partial {
		addProblem("no workload kit in the set; every composition needs exactly one kind: workload base")
	}
	return workload
}

// checkOneProviderPerName rejects a set in which two kits provide the
// same normalized name, at any versions. Only one provider's content is
// reachable after composition (mixins overlay the workload, later mixins
// overlay earlier ones), so a second provide vouches for shadowed content
// — and every consumer of "the provider of X" (ordering, locks, minimum
// audits) would otherwise have to invent its own tie-break. Equal
// versions are no better: one name, one owner.
func checkOneProviderPerName(provides map[string][]provider, addProblem func(string, ...any)) {
	// The MUST is about owners, not entries: a single kit repeating a
	// name (bare plus its qualified spelling) is redundancy within one
	// owner, not two owners, so the judgment counts distinct kits.
	names := make([]string, 0, len(provides))
	for name, ps := range provides {
		owners := map[*Unit]bool{}
		for _, p := range ps {
			owners[p.unit] = true
		}
		if len(owners) > 1 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var offers []string
		seen := map[*Unit]bool{}
		for _, p := range provides[name] {
			if seen[p.unit] {
				continue
			}
			seen[p.unit] = true
			v := p.provide.Version
			if v == "" {
				v = "unversioned"
			}
			offers = append(offers, fmt.Sprintf("%s (%s)", p.unit.Reference, v))
		}
		addProblem("capability %q is provided by more than one kit: %s; one name has one owner, so drop the redundant kit", name, strings.Join(offers, ", "))
	}
}

// checkOneCredentialOwner enforces the credential page's composition
// rule across the set: two kits declaring the same (service, phase) is a
// composition conflict — one credential, one owner. Descriptor validation
// catches duplicates inside one kit; only the resolver sees the set.
func checkOneCredentialOwner(units []*Unit, addProblem func(string, ...any)) {
	type key struct{ service, phase string }
	owners := map[key][]string{}
	for _, u := range units {
		creds, err := spec.CredentialsOf(u.Descriptor.Capabilities)
		if err != nil {
			continue // the per-descriptor validator owns malformed configs
		}
		for _, c := range creds {
			for _, phase := range c.Phase {
				k := key{c.Service, phase}
				owners[k] = append(owners[k], u.Reference)
			}
		}
	}
	keys := make([]key, 0, len(owners))
	for k, refs := range owners {
		if len(refs) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].service != keys[j].service {
			return keys[i].service < keys[j].service
		}
		return keys[i].phase < keys[j].phase
	})
	for _, k := range keys {
		addProblem("credential (%s, %s) is declared by more than one kit: %s; one credential, one owner", k.service, k.phase, strings.Join(owners[k], ", "))
	}
}

// providesIndex maps capability name to every (unit, provide) offering it.
type provider struct {
	unit    *Unit
	provide spec.Provide
}

func providesIndex(units []*Unit, addProblem func(string, ...any)) map[string][]provider {
	index := map[string][]provider{}
	for _, u := range units {
		fallback := spec.EffectiveProvideVersion(u.Reference, u.Descriptor)
		for _, s := range u.Descriptor.Provides {
			p, err := spec.ParseProvide(s)
			if err != nil {
				addProblem("%s: %v", u.Reference, err)
				continue
			}
			// An explicit @version on the provide always wins.
			if p.Version == "" && fallback != "" {
				p.Version = fallback
			}
			index[p.Name] = append(index[p.Name], provider{unit: u, provide: p})
		}
	}
	return index
}

// EffectiveProvideVersion delegates to the shared spec rule for the version
// inherited by unversioned provides from a consumed Kit.
func EffectiveProvideVersion(reference string, d *spec.Descriptor) string {
	return spec.EffectiveProvideVersion(reference, d)
}

// VersionFromReference delegates to spec's consumption-reference version rule.
func VersionFromReference(reference string) string {
	return spec.VersionFromReference(reference)
}

func checkRequires(units []*Unit, provides map[string][]provider, addProblem func(string, ...any)) {
	for _, u := range units {
		for _, s := range u.Descriptor.Requires {
			r, err := spec.ParseRequire(s)
			if err != nil {
				addProblem("%s: %v", u.Reference, err)
				continue
			}
			if satisfiedBy(r, provides, u) == nil {
				candidates := provides[r.Name]
				switch {
				case len(candidates) == 0:
					addProblem("%s requires %q but nothing in the set provides it; add a kit that provides %s", u.Reference, s, r.Name)
				default:
					offered := make([]string, 0, len(candidates))
					for _, c := range candidates {
						offered = append(offered, describeProvide(c))
					}
					addProblem("%s requires %q but the set only offers %s", u.Reference, s, strings.Join(offered, ", "))
				}
			}
		}
	}
}

// checkIntegrates judges the entries a provider is PRESENT for: an
// absent provider is the feature working (the kit functions without it),
// but a present one that violates the stated constraints is incoherence —
// the integration the entry names would break.
func checkIntegrates(units []*Unit, provides map[string][]provider, addProblem func(string, ...any)) {
	for _, u := range units {
		for _, s := range u.Descriptor.Integrates {
			r, err := spec.ParseRequire(s)
			if err != nil {
				addProblem("%s: %v", u.Reference, err)
				continue
			}
			candidates := othersProviding(r.Name, provides, u)
			if len(candidates) == 0 || satisfiedBy(r, provides, u) != nil {
				continue
			}
			offered := make([]string, 0, len(candidates))
			for _, c := range candidates {
				offered = append(offered, describeProvide(c))
			}
			addProblem("%s integrates with %q but the set only offers %s; upgrade the provider or drop it from the set", u.Reference, s, strings.Join(offered, ", "))
		}
	}
}

// othersProviding lists the providers of a capability excluding self.
func othersProviding(name string, provides map[string][]provider, self *Unit) []provider {
	var out []provider
	for _, c := range provides[name] {
		if c.unit != self {
			out = append(out, c)
		}
	}
	return out
}

// satisfiedBy returns a unit (other than self) whose provide satisfies r,
// or nil. A kit cannot satisfy its own requirement: requires expresses a
// dependency on the rest of the set.
func satisfiedBy(r spec.Require, provides map[string][]provider, self *Unit) *Unit {
	for _, c := range provides[r.Name] {
		if c.unit == self {
			continue
		}
		if spec.Satisfies(c.provide, r) {
			return c.unit
		}
	}
	return nil
}

func describeProvide(c provider) string {
	name := spec.DisplayCapabilityName(c.provide.Name)
	if c.provide.Version == "" {
		return fmt.Sprintf("%s (unversioned, from %s)", name, c.unit.Reference)
	}
	return fmt.Sprintf("%s@%s (from %s)", name, c.provide.Version, c.unit.Reference)
}

func checkConflicts(units []*Unit, provides map[string][]provider, addProblem func(string, ...any)) {
	for _, u := range units {
		for _, name := range u.Descriptor.Conflicts {
			// The provides index is keyed by normalized names; conflicts
			// entries are authored and may be bare.
			for _, c := range provides[spec.NormalizeCapabilityName(name)] {
				if c.unit == u {
					continue
				}
				addProblem("%s conflicts with %q, which %s provides; the two cannot compose", u.Reference, name, c.unit.Reference)
			}
		}
	}
}

// order topologically sorts the units along capability edges: a provider
// composes before each of its requirers. Among simultaneously-ready units
// the lexicographically smallest reference goes first, so the order is a
// pure function of the set.
func order(units []*Unit, provides map[string][]provider) ([]*Unit, error) {
	indegree := map[*Unit]int{}
	dependents := map[*Unit][]*Unit{}
	for _, u := range units {
		indegree[u] = 0
	}
	for _, u := range units {
		deps := map[*Unit]bool{}
		// A met integrates entry orders like requires: the provider
		// composes before the kit that integrates with it.
		for _, s := range append(append([]string{}, u.Descriptor.Requires...), u.Descriptor.Integrates...) {
			r, err := spec.ParseRequire(s)
			if err != nil {
				continue
			}
			if p := satisfiedBy(r, provides, u); p != nil && !deps[p] {
				deps[p] = true
				dependents[p] = append(dependents[p], u)
				indegree[u]++
			}
		}
	}

	ready := make([]*Unit, 0, len(units))
	for _, u := range units {
		if indegree[u] == 0 {
			ready = append(ready, u)
		}
	}

	ordered := make([]*Unit, 0, len(units))
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return ready[i].Reference < ready[j].Reference })
		next := ready[0]
		ready = ready[1:]
		ordered = append(ordered, next)
		for _, dep := range dependents[next] {
			indegree[dep]--
			if indegree[dep] == 0 {
				ready = append(ready, dep)
			}
		}
	}

	if len(ordered) != len(units) {
		var cyclic []string
		for _, u := range units {
			if indegree[u] > 0 {
				cyclic = append(cyclic, u.Reference)
			}
		}
		sort.Strings(cyclic)
		return nil, fmt.Errorf("resolve: dependency cycle among %s", strings.Join(cyclic, ", "))
	}
	return ordered, nil
}
