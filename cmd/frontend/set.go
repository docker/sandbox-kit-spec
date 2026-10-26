package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"gopkg.in/yaml.v3"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

// setContextName is the staged basename of a merged kit's agent context:
// several kits' guidance becomes one body, so it cannot keep any one
// kit's filename.
const setContextName = "context.md"

// stagedSetContextPath is where a merged kit's context body lands: beside
// the set's own staged sources, which is where the conformance checks
// require a published contentFile to point.
func stagedSetContextPath(stem string) string {
	return path.Join(stagedKitRoot, stem, setContextName)
}

// resolvedKit is one of a set's kits, as the frontend resolved it.
type resolvedKit struct {
	// reference is what the descriptor named it, and the identity every
	// diagnostic uses.
	reference string

	// pinned is the digest-pinned reference the layers come from, and
	// digest is the manifest digest the published descriptor records.
	pinned string
	digest string

	// descriptor is its published descriptor, read from the sources
	// every kit stages into its own filesystem, with the arg values
	// this set supplied already expanded.
	descriptor *spec.Descriptor

	// state is its filesystem, the input to the layer merge.
	state llb.State

	// config is its image config: a workload's runtime contract, or a
	// mixin's recorded delta.
	config ocispecs.Image

	// stem is its own staged root, where its context body (and its
	// sources) live in the merged filesystem.
	stem string

	// env are the variables this kit's args bound, resolved. A kit's
	// arg declarations do not survive the merge — the set answered
	// them — but an arg with env: told the runtime to export a
	// variable, and dropping the declaration would drop that too. A
	// pinned value has a home in the image config, which is where
	// static runtime config belongs.
	env map[string]string
}

// setBuild is a merged set's result: one platform's filesystem and image
// config, plus the merged descriptor the caller publishes.
type setBuild struct {
	ref     gwclient.Reference
	config  []byte
	merged  *spec.Descriptor
	context []spec.ContextSource
	kits    []resolvedKit
}

// setPlan is a set's merged result for every requested platform, plus
// the merged descriptor all of them agreed on.
type setPlan struct {
	// published is the merged descriptor as YAML: what the annotation
	// carries, what stages as the kit's sources, and what the
	// conformance checks judge.
	published []byte

	// builds is keyed by platform id, in platformList order.
	builds map[string]*setBuild
}

// planSet merges the set for every requested platform and holds them to
// one set of declarations.
//
// A kit's descriptor describes the kit, not one of its platforms, so the
// merge has to land on the same declarations everywhere: the annotation
// is written once for every platform manifest. Kits whose per-platform
// descriptors differ would otherwise publish an artifact whose policy is
// accurate on the builder's architecture and a guess on the others.
func planSet(ctx context.Context, c gwclient.Client, d *spec.Descriptor, platformList []*ocispecs.Platform, stem string) (*setPlan, error) {
	plan := &setPlan{builds: map[string]*setBuild{}}
	kitsByPlatform := make([][]resolvedKit, 0, len(platformList))
	for _, plat := range platformList {
		kits, err := resolveKits(ctx, c, d, plat)
		if err != nil {
			return nil, err
		}
		kitsByPlatform = append(kitsByPlatform, kits)
	}
	intersectDerivedProvides(kitsByPlatform)

	var first *setBuild
	var firstID string
	for i, plat := range platformList {
		build, err := buildSet(ctx, c, d, plat, stem, kitsByPlatform[i])
		if err != nil {
			return nil, err
		}
		id := platforms.FormatAll(platformOrDefault(plat))
		plan.builds[id] = build
		if first == nil {
			first, firstID = build, id
			continue
		}
		if err := sameDeclarations(first.merged, build.merged); err != nil {
			return nil, fmt.Errorf("the set's kits declare different things on %s and %s: %w; a kit's descriptor describes the kit, not one of its platforms", firstID, id, err)
		}
	}

	published, err := publishedSetDescriptor(first.merged)
	if err != nil {
		return nil, err
	}
	plan.published = published
	return plan, nil
}

// intersectDerivedProvides applies §9.6 to kits whose platform manifests
// were published separately. Each platform was allowed to derive its own
// inventory, but the set can carry only entries all its target platforms
// agree on. Do this per listed kit and before resolution: otherwise an
// architecture-only package could satisfy a requirement that the merged
// descriptor cannot promise. Authored declarations still have to agree.
func intersectDerivedProvides(kitsByPlatform [][]resolvedKit) {
	if len(kitsByPlatform) < 2 {
		return
	}
	// resolveKits preserves the authored kits list on every platform.
	for i := range kitsByPlatform[0] {
		counts := map[string]int{}
		for _, kits := range kitsByPlatform {
			seen := map[string]bool{}
			for _, entry := range kits[i].descriptor.Provides {
				p, err := spec.ParseProvide(entry)
				if err != nil || !spec.IsDerivedProvide(p.Name) {
					continue
				}
				// Published entries may carry surrounding whitespace;
				// the grammar treats those spellings as the same fact.
				entry = strings.TrimSpace(entry)
				if !seen[entry] {
					counts[entry]++
					seen[entry] = true
				}
			}
		}
		var agreed []string
		for entry, count := range counts {
			if count == len(kitsByPlatform) {
				agreed = append(agreed, entry)
			}
		}
		sort.Strings(agreed)
		for _, kits := range kitsByPlatform {
			// Keep the original published descriptor intact; only this
			// set's contribution uses the intersection.
			d := *kits[i].descriptor
			d.Provides = nil
			for _, entry := range kits[i].descriptor.Provides {
				p, err := spec.ParseProvide(entry)
				if err != nil || !spec.IsDerivedProvide(p.Name) {
					d.Provides = append(d.Provides, entry)
				}
			}
			d.Provides = append(d.Provides, agreed...)
			kits[i].descriptor = &d
		}
	}
}

// sameDeclarations reports whether two merges produced one descriptor.
func sameDeclarations(a, b *spec.Descriptor) error {
	left, err := json.Marshal(a)
	if err != nil {
		return err
	}
	right, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if !bytes.Equal(left, right) {
		return fmt.Errorf("%s\n  vs\n%s", left, right)
	}
	return nil
}

// publishedSetDescriptor renders the derived descriptor and holds it to
// the bar any published descriptor meets. Deriving can produce shapes no
// member authored — a set of mixins derives to a mixin, carrying
// declarations only a workload may make — so the check belongs here
// rather than at the first consumer.
func publishedSetDescriptor(d *spec.Descriptor) ([]byte, error) {
	published, err := marshalDescriptorYAML(d)
	if err != nil {
		return nil, err
	}
	if _, err := spec.ValidatePublished(published, d); err != nil {
		return nil, fmt.Errorf("the set derives to a descriptor that is not publishable: %w", err)
	}
	return published, nil
}

// marshalDescriptorYAML renders the merged descriptor as the YAML a kit
// stages, so a merged kit's sources read like any other kit's.
func marshalDescriptorYAML(d *spec.Descriptor) ([]byte, error) {
	// Through JSON first: the yaml encoder honors yaml tags, but only
	// the json ones carry omitempty consistently across the grammar's
	// structs, and a descriptor full of zero values would stage fields
	// their author never wrote.
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return yaml.Marshal(generic)
}

// buildSet produces a set kit's content and declarations.
//
// It is the runtime's create-time composition moved to publish: the
// set's kits are resolved and judged as a set, their layers merged,
// their image configs folded together by the same arithmetic the
// assembler uses, and their declarations reconciled into one descriptor.
// What comes out is an ordinary kit — their roles, and the fact that
// there were several of them at all, survive only as the record in
// kits:.
func buildSet(ctx context.Context, c gwclient.Client, d *spec.Descriptor, plat *ocispecs.Platform, stem string, kits []resolvedKit) (*setBuild, error) {
	// The set's own declarations are read first: they take part in the
	// resolution, since what a set provides is provided to its kits as
	// much as to whatever composes the result.
	own, err := setOwnDescriptor(ctx, c, d)
	if err != nil {
		return nil, err
	}

	ordered, err := orderKits(kits, own)
	if err != nil {
		return nil, err
	}

	if err := checkStemCollision(stem, ordered); err != nil {
		return nil, err
	}

	kitContributions := make([]spec.Contribution, 0, len(ordered))
	for _, k := range ordered {
		kitContributions = append(kitContributions, spec.Contribution{Reference: k.reference, Descriptor: k.descriptor})
	}
	if err := checkSetRelations(own, kitContributions); err != nil {
		return nil, err
	}
	contributions, err := orderContributions(own, kitContributions)
	if err != nil {
		return nil, err
	}
	// And the content follows that same order. orderKits knows the set
	// only by what it provides — enough to accept a kit requiring it,
	// but blind to what the set itself requires — so a kit the set
	// depends on could land after a kit that depends on the set. Two
	// orderings from two graphs is what let them disagree.
	ordered = kitsInContributionOrder(contributions, ordered)

	result, err := spec.Merge(contributions, spec.MergeOptions{
		ContextPath: stagedSetContextPath(stem),
	})
	if err != nil {
		return nil, err
	}

	merged := result.Descriptor
	if err := checkAuthoredKind(d.Kind, merged.Kind); err != nil {
		return nil, err
	}
	if err := carryOverSetMetadata(d, merged, ordered); err != nil {
		return nil, err
	}

	// Read from the kits themselves, before their layers meet. A
	// merged filesystem is one namespace, and this set's whole
	// premise is that two kits' layers may write the same path — so
	// reading a context body out of the merge could hand one kit's
	// guidance the bytes another kit wrote over it, or find the path
	// whited out entirely.
	resolvedSources, err := readContextSources(ctx, c, ordered, plat, result.ContextSources)
	if err != nil {
		return nil, err
	}

	ref, configJSON, err := mergeKitContent(ctx, c, ordered, merged, plat)
	if err != nil {
		return nil, err
	}

	return &setBuild{
		ref:     ref,
		config:  configJSON,
		merged:  merged,
		context: resolvedSources,
		kits:    ordered,
	}, nil
}

// readContextSources replaces each body named by a path with the bytes
// that path holds in the kit that named it.
//
// Every source comes back as content, so the staging that follows the
// merge reads nothing from the merged filesystem — where whose file is
// whose is no longer a question that can be answered.
func readContextSources(ctx context.Context, c gwclient.Client, kits []resolvedKit, plat *ocispecs.Platform, sources []spec.ContextSource) ([]spec.ContextSource, error) {
	if len(sources) == 0 {
		return sources, nil
	}
	byReference := make(map[string]resolvedKit, len(kits))
	for _, k := range kits {
		byReference[k.reference] = k
	}

	// One solve per kit that has a body to read, not one per body.
	refs := map[string]gwclient.Reference{}
	out := make([]spec.ContextSource, 0, len(sources))
	for _, source := range sources {
		if source.Path == "" {
			out = append(out, source)
			continue
		}
		k, ok := byReference[source.Reference]
		if !ok {
			// The set's own body, which setOwnDescriptor inlined.
			return nil, fmt.Errorf("agent context from %s names a path, but that contribution has no filesystem to read it from", source.Reference)
		}
		ref, ok := refs[source.Reference]
		if !ok {
			var err error
			if ref, err = stateReference(ctx, c, k.state, platformOrDefault(plat)); err != nil {
				return nil, fmt.Errorf("read agent context from %s: %w", source.Reference, err)
			}
			refs[source.Reference] = ref
		}
		raw, err := ref.ReadFile(ctx, gwclient.ReadRequest{Filename: source.Path})
		if err != nil {
			return nil, fmt.Errorf("read agent context %s from %s: %w", source.Path, source.Reference, err)
		}
		out = append(out, spec.ContextSource{Reference: source.Reference, Content: string(raw), Target: source.Target})
	}
	return out, nil
}

// stateReference solves one kit's filesystem so its files can be read.
func stateReference(ctx context.Context, c gwclient.Client, state llb.State, p ocispecs.Platform) (gwclient.Reference, error) {
	def, err := state.Marshal(ctx, llb.Platform(p))
	if err != nil {
		return nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, err
	}
	return res.SingleRef()
}

// kitsInContributionOrder rearranges the kits to match the order their
// contributions were sorted into, leaving out the set's own.
func kitsInContributionOrder(contributions []spec.Contribution, kits []resolvedKit) []resolvedKit {
	byReference := make(map[string]resolvedKit, len(kits))
	for _, k := range kits {
		byReference[k.reference] = k
	}
	out := make([]resolvedKit, 0, len(kits))
	for _, c := range contributions {
		if k, ok := byReference[c.Reference]; ok {
			out = append(out, k)
		}
	}
	return out
}

// setOwnDescriptor is the set's own declarations as a contribution: the
// same descriptor minus what describes the set rather than the merged
// kit. The kits list is the content recipe, not a declaration, and the
// kind is what the merge derives.
//
// Its agent-context body is inlined here, because a set's contentFile
// names a file in the build context while a published kit's names one
// already inside the image. Reading it now leaves the merge with one kind of
// source it does not have to distinguish, and the staging step with one
// place to look.
func setOwnDescriptor(ctx context.Context, c gwclient.Client, d *spec.Descriptor) (*spec.Descriptor, error) {
	own := *d
	own.Kits = nil
	// The set's own declarations carry no root filesystem, so they
	// contribute as a mixin whatever the descriptor's authored kind is.
	// This is not only about kind: set, which the merge refuses as a
	// contribution: an author may state the derived kind instead, and
	// an explicit `kind: workload` entering the merge as a second
	// workload would collide with the real one among the listed kits.
	// What that explicit kind claims is checked against the derived
	// result separately (checkAuthoredKind).
	own.Kind = spec.KindMixin

	var err error
	own.Capabilities, err = spec.MapCapabilities(own.Capabilities, func(n spec.Capability) (spec.Capability, error) {
		if n.Type != spec.CapabilityAgentContext {
			return n, nil
		}
		var ac spec.AgentContext
		if err := spec.DecodeCapabilityConfig(n, &ac); err != nil {
			return n, err
		}
		if ac.ContentFile == "" {
			return n, nil
		}
		body, err := readContextFile(ctx, c, strings.TrimPrefix(ac.ContentFile, "./"))
		if err != nil {
			return n, fmt.Errorf("agent-context contentFile %s: %w", ac.ContentFile, err)
		}
		ac.ContentFile = ""
		ac.Content = string(body)
		replaced, err := spec.CapabilityWithConfig(n, &ac)
		if err != nil {
			return n, err
		}
		return *replaced, nil
	})
	return &own, err
}

// orderContributions sorts the set's own declarations together with
// its kits along the same dependency edges the resolver uses.
//
// The set is normally last — it is the author with the whole
// composition in view, so where a merge rule takes the first
// statement of something, its kits' own win. But the edges run both
// ways: a listed kit may integrate with a capability the set
// provides, which makes the set that kit's provider, and the set may
// require something a kit provides. Ordering by hand in one direction
// would silently invert the other, so both are sorted, and an edge
// each way between the same two is a cycle nothing can order.
//
// Relations naming capabilities nobody here provides are left alone:
// those are asks of the composition the merged kit lands in.
func orderContributions(own *spec.Descriptor, kits []spec.Contribution) ([]spec.Contribution, error) {
	all := append(append([]spec.Contribution{}, kits...),
		spec.Contribution{Reference: "the set descriptor", Descriptor: own})
	owned := spec.OwnedProvides(all)

	index := map[string]int{}
	for i, c := range all {
		index[c.Reference] = i
	}
	dependsOn := make([][]int, len(all))
	indegree := make([]int, len(all))
	for i, c := range all {
		seen := map[int]bool{}
		for _, entries := range [][]string{c.Descriptor.Requires, c.Descriptor.Integrates} {
			for _, entry := range entries {
				r, err := spec.ParseRequire(entry)
				if err != nil {
					continue
				}
				for _, o := range owned[r.Name] {
					if o.Owner == c.Reference || !spec.Satisfies(o.Provide, r) {
						continue
					}
					if j := index[o.Owner]; !seen[j] {
						seen[j] = true
						dependsOn[j] = append(dependsOn[j], i)
						indegree[i]++
					}
				}
			}
		}
	}

	// Ties keep the order they arrived in, which for the kits is the
	// one the resolver derived and for the set is last.
	var ready []int
	for i := range all {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	out := make([]spec.Contribution, 0, len(all))
	for len(ready) > 0 {
		sort.Ints(ready)
		next := ready[0]
		ready = ready[1:]
		out = append(out, all[next])
		for _, dep := range dependsOn[next] {
			if indegree[dep]--; indegree[dep] == 0 {
				ready = append(ready, dep)
			}
		}
	}
	if len(out) != len(all) {
		var stuck []string
		for i, c := range all {
			if indegree[i] > 0 {
				stuck = append(stuck, c.Reference)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("the set and the kits it lists depend on each other in a circle (%s); one of those relations has to go", strings.Join(stuck, ", "))
	}
	return out, nil
}

// checkSetRelations judges the kit-to-kit relations that flattening
// would turn into self-relations, in both directions.
//
// The resolver sees only the listed kits: the set's declarations join
// afterwards as one more contribution to the merge. So a relation
// either side states about the other goes unjudged, and after the
// merge it names the merged kit itself — which every consumer skips,
// because a kit cannot satisfy, conflict with, or integrate against
// its own capabilities. The incompatibility does not resolve; it
// disappears.
//
// Both directions therefore have to be checked here. A set listing
// shell@1 while requiring shell >= 2 would publish a kit that offers
// shell@1 and demands more. A listed kit declaring conflicts: [team]
// while the set provides team@1 would publish a kit that excludes
// itself. Neither survives as anything a runtime could act on.
//
// A relation about a name NEITHER side provides is left alone: that is
// an ask of the composition the merged kit lands in, which is exactly
// what survives the merge and the only kind still capable of being met.
func checkSetRelations(own *spec.Descriptor, kits []spec.Contribution) error {
	setSide := []spec.Contribution{{Reference: "the set descriptor", Descriptor: own}}
	byKits, kitOwner := providersOf(kits)
	bySet, setOwner := providersOf(setSide)

	// The set against its kits.
	for _, s := range own.Provides {
		p, err := spec.ParseProvide(s)
		if err != nil {
			continue
		}
		if ref, taken := kitOwner[p.Name]; taken {
			return fmt.Errorf("the set declares provides %q, which %s already provides; one name has one owner, so drop it from the set", s, ref)
		}
	}
	if err := checkRelationsAgainst(own, byKits, "the set", "the kits it lists"); err != nil {
		return err
	}
	for _, name := range own.Conflicts {
		if ref, taken := kitOwner[spec.NormalizeCapabilityName(name)]; taken {
			return fmt.Errorf("the set conflicts with %q, which the %s it lists provides; the merged kit would exclude itself", name, ref)
		}
	}

	// The set against itself. A requirement only its own provide could
	// answer is one nothing can: Resolve skips self, so the merged kit
	// would demand a capability it is the sole owner of. The merge
	// carries it through rather than dropping it, which is honest and
	// unresolvable; saying so here is what makes it fixable.
	//
	// Requires only. An integration names something the kit works with
	// when present and functions without, and the resolver judges just
	// the present other providers — so a self-provided one is absent
	// as far as the rule is concerned, which is a working kit rather
	// than a stuck one.
	for _, entries := range [][]string{own.Requires} {
		for _, s := range entries {
			r, err := spec.ParseRequire(s)
			if err != nil {
				continue
			}
			if _, mine := setOwner[r.Name]; mine {
				if _, theirs := kitOwner[r.Name]; !theirs {
					return fmt.Errorf("the set states %q and is also the only thing providing %s; a kit cannot satisfy its own requirement, so drop one of the two",
						s, spec.DisplayCapabilityName(r.Name))
				}
			}
		}
	}

	// Each kit against the set.
	for _, c := range kits {
		if err := checkRelationsAgainst(c.Descriptor, bySet, c.Reference, "the set"); err != nil {
			return err
		}
		for _, name := range c.Descriptor.Conflicts {
			if _, taken := setOwner[spec.NormalizeCapabilityName(name)]; taken {
				return fmt.Errorf("%s conflicts with %q, which the set itself provides; the merged kit would exclude itself", c.Reference, name)
			}
		}
	}
	return nil
}

// checkRelationsAgainst holds one side's requires and integrates to
// what the other side offers. A name the other side does not offer is
// somebody else's to answer and passes.
func checkRelationsAgainst(d *spec.Descriptor, provided map[string][]spec.Provide, who, other string) error {
	for _, entries := range [][]string{d.Requires, d.Integrates} {
		for _, s := range entries {
			r, err := spec.ParseRequire(s)
			if err != nil {
				continue
			}
			offers, known := provided[r.Name]
			if !known || spec.SatisfiedBy(provided, r) {
				continue
			}
			return fmt.Errorf("%s states %q, but %s offers %s; a merged kit cannot satisfy its own requirement, so this could never resolve",
				who, s, other, describeOffers(offers))
		}
	}
	return nil
}

// providersOf indexes what a side offers, and who offers each name.
func providersOf(contributions []spec.Contribution) (map[string][]spec.Provide, map[string]string) {
	owner := map[string]string{}
	for _, c := range contributions {
		for _, s := range c.Descriptor.Provides {
			p, err := spec.ParseProvide(s)
			if err != nil {
				continue
			}
			if _, seen := owner[p.Name]; !seen {
				owner[p.Name] = c.Reference
			}
		}
	}
	return spec.ProvidesIndex(contributions), owner
}

func describeOffers(offers []spec.Provide) string {
	rendered := make([]string, 0, len(offers))
	for _, p := range offers {
		rendered = append(rendered, spec.DisplayCapabilityName(p.Name)+"@"+p.Version)
	}
	return strings.Join(rendered, ", ")
}

// checkAuthoredKind holds an explicitly stated kind to what the listed
// kits make the result.
//
// `kind: set` asks for the kind to be derived and so claims nothing.
// Stating `workload` or `mixin` beside kits: is equally legal and says
// the same thing explicitly — but only when it is true, or the
// descriptor would name a role its content does not play.
func checkAuthoredKind(authored, derived string) error {
	if authored == spec.KindSet || authored == derived {
		return nil
	}
	return fmt.Errorf("descriptor declares kind: %s, but the kits it lists make it a %s; state %s, or kind: %s to derive it",
		authored, derived, derived, spec.KindSet)
}

// checkStemCollision keeps the set's own staged sources off its kits'.
// Both land under the staged root by filename stem, so a set named after
// one of its kits would overwrite that kit's descriptor with its own —
// leaving the merged filesystem describing that kit as the set.
func checkStemCollision(stem string, kits []resolvedKit) error {
	for _, k := range kits {
		if k.stem == stem {
			return fmt.Errorf("%s stages its sources at %s, which is where this set would stage its own; rename the descriptor",
				k.reference, path.Join(tckkit.StagedKitRoot, stem))
		}
	}
	return nil
}

// carryOverSetMetadata puts the set author's own descriptive fields onto
// the merged descriptor and records the kits it was built from.
//
// The display fields are the set's, not any of its kits': the artifact
// is a new thing with its own name, publisher, and documentation. The
// kits list is the published record of how the content was produced,
// the way an inline build: block is, with every reference pinned to the
// manifest this build actually resolved.
func carryOverSetMetadata(d *spec.Descriptor, merged *spec.Descriptor, kits []resolvedKit) error {
	merged.DisplayName = d.DisplayName
	merged.Author = d.Author
	merged.Description = d.Description
	merged.SourceURL = d.SourceURL
	merged.IconURL = d.IconURL
	merged.Version = d.Version
	merged.Args = d.Args

	byReference := make(map[string]resolvedKit, len(kits))
	for _, k := range kits {
		byReference[k.reference] = k
	}
	for _, k := range d.Kits {
		resolved, ok := byReference[k.Ref]
		if !ok {
			return fmt.Errorf("%s was not resolved", k.Ref)
		}
		merged.Kits = append(merged.Kits, spec.Kit{
			Ref:    k.Ref,
			Digest: resolved.digest,
			Args:   k.Args,
		})
	}
	return nil
}

// resolveKits pins every kit a set lists and reads its declarations.
func resolveKits(ctx context.Context, c gwclient.Client, d *spec.Descriptor, plat *ocispecs.Platform) ([]resolvedKit, error) {
	p := platformOrDefault(plat)
	out := make([]resolvedKit, 0, len(d.Kits))
	for _, k := range d.Kits {
		resolved, err := resolveKit(ctx, c, k, p, d.Args)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k.Ref, err)
		}
		out = append(out, *resolved)
	}
	return out, nil
}

func resolveKit(ctx context.Context, c gwclient.Client, k spec.Kit, p ocispecs.Platform, setArgs map[string]spec.Arg) (*resolvedKit, error) {
	named, err := reference.ParseNormalizedNamed(k.Ref)
	if err != nil {
		return nil, err
	}
	// An authored digest is a pin the author chose; honoring it here is
	// what makes a set reproducible against a moving tag. A reference
	// that already carries one is pinned twice, and two pins that
	// disagree cannot both be what the author meant — the build would
	// resolve one of them and the descriptor would record the other.
	if canonical, ok := named.(reference.Canonical); ok && k.Digest != "" {
		if canonical.Digest().String() != k.Digest {
			return nil, fmt.Errorf("ref pins %s but digest: says %s; one kit has one pin",
				canonical.Digest(), k.Digest)
		}
	}
	ref := reference.TagNameOnly(named).String()
	if k.Digest != "" && !strings.Contains(ref, "@") {
		ref = ref + "@" + k.Digest
	}

	pinned, dgst, configRaw, err := c.ResolveImageConfig(ctx, ref, sourceresolver.Opt{
		ImageOpt: &sourceresolver.ResolveImageOpt{
			Platform: &p,
			// --pull reaches a frontend as image-resolve-mode; honoring
			// it is what lets a set re-pin against a tag that moved.
			ResolveMode: c.BuildOpts().Opts[keyImageResolveMode],
		},
	})
	if err != nil {
		return nil, err
	}
	if pinned == "" {
		pinned = ref
	}
	if dgst != "" && !strings.Contains(pinned, "@") {
		pinned = pinned + "@" + dgst.String()
	}

	var config ocispecs.Image
	if len(configRaw) > 0 {
		if err := json.Unmarshal(configRaw, &config); err != nil {
			return nil, fmt.Errorf("parse image config: %w", err)
		}
	}

	state := llb.Image(pinned, llb.Platform(p), llb.WithCustomName("[kit] "+k.Ref))
	published, stem, err := readKitDescriptor(ctx, c, state, p)
	if err != nil {
		return nil, err
	}

	descriptor, env, err := kitDeclarations(published, k, setArgs)
	if err != nil {
		return nil, err
	}
	// The version this kit's unversioned provides take is the one the
	// resolver will judge it by, which a version-shaped tag supplies
	// over a stale descriptor (§4). Materializing it here is what keeps
	// the merge's output agreeing with the resolution it is handed:
	// otherwise a kit consumed as base:2.0.0 whose descriptor says
	// 1.0.0 satisfies an internal `base >= 2.0.0` during ordering, and
	// then the merge emits base@1.0.0 and keeps that requirement.
	descriptor.Version = resolve.EffectiveProvideVersion(k.Ref, descriptor)

	return &resolvedKit{
		reference:  k.Ref,
		env:        env,
		pinned:     pinned,
		digest:     dgst.String(),
		descriptor: descriptor,
		state:      state,
		config:     config,
		stem:       stem,
	}, nil
}

// readKitDescriptor reads one listed kit's published descriptor out of
// the sources every kit stages into its own filesystem.
//
// The staged copy is the only place a frontend can read it: BuildKit
// resolves an image's config, never its manifest annotations. It is the
// same document — the staged-sources conformance check asserts the two
// agree — so a kit that went through any conforming publisher carries
// it, and one that does not is not a kit this set can compose.
//
// Several staged roots mean the kit was itself built from kits, and
// only one of them describes what this reference names. Nothing in the
// filesystem says which: a merged set's own root is the one carrying
// kits:, but a kit built FROM a merged set has exactly the same shape
// with the answer being the other root. Telling them apart needs the
// artifact's own identity, which lives in the manifest annotation a
// frontend cannot read. So the ambiguity is refused rather than
// guessed — merging against the wrong kit's declarations would publish
// policy nobody wrote, and the remedy (list the underlying kits) is
// one the author can act on.
func readKitDescriptor(ctx context.Context, c gwclient.Client, state llb.State, p ocispecs.Platform) (*spec.Descriptor, string, error) {
	def, err := state.Marshal(ctx, llb.Platform(p))
	if err != nil {
		return nil, "", err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, "", err
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, "", err
	}

	entries, err := ref.ReadDir(ctx, gwclient.ReadDirRequest{Path: stagedKitRoot})
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w; a set composes kits, and every kit stages its own sources", stagedKitRoot, err)
	}
	var stems []string
	for _, entry := range entries {
		if os.FileMode(entry.Mode).IsDir() {
			stems = append(stems, path.Base(entry.Path))
		}
	}

	staged := map[string][]byte{}
	var found []string
	for _, candidate := range stems {
		body, err := ref.ReadFile(ctx, gwclient.ReadRequest{
			Filename: path.Join(stagedKitRoot, candidate, stagedDescriptorName),
		})
		if err != nil {
			continue
		}
		found = append(found, candidate)
		staged[candidate] = body
	}
	if len(found) == 0 {
		return nil, "", fmt.Errorf("no kit sources staged under %s; a set composes kits", stagedKitRoot)
	}

	if len(found) > 1 {
		return nil, "", fmt.Errorf("carries staged sources for %d kits (%s), so which of them this reference names cannot be read from the image — list those kits directly instead of the kit that merged them",
			len(found), strings.Join(found, ", "))
	}

	stem := found[0]
	raw := staged[stem]
	d, err := spec.Decode(raw)
	if err != nil {
		return nil, "", fmt.Errorf("staged descriptor does not decode: %w", err)
	}
	if _, err := spec.ValidatePublished(raw, d); err != nil {
		return nil, "", fmt.Errorf("staged descriptor is not a valid published descriptor: %w", err)
	}
	return d, stem, nil
}

// kitDeclarations resolves one listed kit's create-phase args from the
// values its set supplied and expands them into its declarations, so
// what reaches the merge is that kit as this set configures it.
func kitDeclarations(published *spec.Descriptor, k spec.Kit, setArgs map[string]spec.Arg) (*spec.Descriptor, map[string]string, error) {
	values, err := spec.KitArgValues(published.Args, k.Args)
	if err != nil {
		return nil, nil, err
	}

	// A re-exported arg is answered by the set's declaration, and the
	// kit's own is gone by the time an installer reads anything. The
	// set's therefore has to say at least as much as the kit's did.
	for name, value := range values {
		if !spec.IsWholeArgRef(value) {
			continue
		}
		referenced := spec.ReferencedArgs([]byte(value))
		if len(referenced) != 1 {
			continue
		}
		setArg, declared := setArgs[referenced[0]]
		if err := spec.CheckReExport(name, published.Args[name], referenced[0], setArg, declared); err != nil {
			return nil, nil, err
		}
	}

	// An arg with env: asks the runtime to export a variable, and
	// that instruction lives in the declaration the merge is about to
	// drop. A resolved value keeps it as static image config; a
	// re-exported one has no value yet and no slot to wait in, so it
	// is refused rather than silently lost.
	env := map[string]string{}
	for name, decl := range published.Args {
		if decl.BuildArg != "" || decl.Env == "" {
			continue
		}
		value, resolved := values[name]
		if !resolved {
			continue
		}
		if spec.ContainsArgRef(value) {
			return nil, nil, fmt.Errorf("arg %q exports %s and is re-exported rather than pinned; the merged kit has nowhere to hold a variable whose value arrives at create, so give it a literal in kits[].args", name, decl.Env)
		}
		// Two of a kit's args may name one variable, and args are a
		// map: taking whichever was read last would publish a
		// different image for the same inputs.
		if held, ok := env[decl.Env]; ok && held != value {
			return nil, nil, fmt.Errorf("two of that kit's args export %s, resolving to %q and %q; supply values that agree, or the merged image would depend on which was read first", decl.Env, held, value)
		}
		env[decl.Env] = value
	}

	raw, err := json.Marshal(published)
	if err != nil {
		return nil, nil, err
	}
	expanded, err := spec.ExpandCreateArgs(raw, published.Args, values)
	if err != nil {
		return nil, nil, err
	}
	d, err := spec.Decode(expanded)
	if err != nil {
		return nil, nil, fmt.Errorf("declarations do not decode after expanding its args: %w", err)
	}
	// A capability config that referenced one of this kit's args passed
	// its own publish leniently, typed checks deferred until the
	// placeholders resolved. They have resolved here, so this is where
	// the deferral is collected — otherwise an invalid concrete request
	// reaches the merge, which may normalize it into something valid
	// and publish a set that direct consumption of the same kit would
	// refuse.
	//
	// A re-exported arg or a final-environment reference leaves a
	// placeholder behind, and the strict
	// form refuses any reference at all — but only the entries that
	// still carry one are genuinely unjudgeable. Validate deferring
	// those, so a field that did resolve is held to its type even when
	// a sibling did not: otherwise one re-export anywhere switched off
	// every check, and the merge could normalize away an invalid pair
	// before anything saw it.
	if len(spec.ReferencedArgs(expanded)) == 0 && !spec.HasEnvReferences(d.Capabilities) {
		if _, err := spec.ValidateExpandedDeclarations(expanded, d); err != nil {
			return nil, nil, fmt.Errorf("declarations are invalid once its args resolve: %w", err)
		}
	} else {
		// Remaining arg references now belong to the set. Validate them
		// against its declarations, while leaving version alternatives
		// and other selection-dependent checks for create time.
		pending := *d
		pending.Args = setArgs
		if _, err := spec.ValidateDeclarations(expanded, &pending); err != nil {
			return nil, nil, fmt.Errorf("declarations are invalid once the set's args are applied: %w", err)
		}
	}
	// Its own arg declarations are answered now: a literal value is
	// baked in, and a re-exported one is the set's arg to declare.
	// Either way the merged kit has no input of this one's left to
	// offer.
	d.Args = nil
	return d, env, nil
}

// orderKits judges the listed kits as a set and returns them in the
// order their dependency graph derives — providers first, the
// workload wherever its own relations put it.
//
// That order is what reconciles declarations: a workload requiring a
// mixin's capability should see that mixin's hooks before its own.
// Layers are a separate question, answered where they are merged: a
// workload's filesystem is the base whatever it requires.
//
// This is the resolver the runtime runs at create, run at publish
// instead: every requirement satisfied inside the set, no conflicts, one
// provider per name, one credential owner, providers before requirers. A
// set with no workload resolves partially — it is an overlay that
// composes onto one later, exactly as its own kits would have.
func orderKits(kits []resolvedKit, own *spec.Descriptor) ([]resolvedKit, error) {
	units := make([]*resolve.Unit, 0, len(kits)+1)
	byUnit := make(map[*resolve.Unit]resolvedKit, len(kits))
	for _, k := range kits {
		u := &resolve.Unit{
			Reference:  k.reference,
			Digest:     k.digest,
			Image:      k.pinned,
			Descriptor: k.descriptor,
		}
		units = append(units, u)
		byUnit[u] = k
	}

	// The set's own declarations answer for themselves here. What a
	// set provides is provided to its kits as much as to whatever
	// composes it, so leaving it out would reject a kit requiring a
	// capability the set states — the same edge §9.5 puts in the one
	// graph every contribution is ordered by. Its provides are all
	// the resolver needs from it; the ordering among contributions is
	// settled separately, once its declarations are contributions.
	setUnit := &resolve.Unit{Reference: "the set descriptor", Descriptor: &spec.Descriptor{
		SchemaVersion: own.SchemaVersion,
		Kind:          spec.KindMixin,
		Version:       own.Version,
		Provides:      own.Provides,
	}}
	units = append(units, setUnit)

	resolution, err := resolve.Dependencies(units, true)
	if err != nil {
		return nil, err
	}
	ordered := make([]resolvedKit, 0, len(kits))
	for _, u := range resolution.Topological() {
		if u == setUnit {
			continue
		}
		ordered = append(ordered, byUnit[u])
	}
	return ordered, nil
}

// mergeKitContent merges the listed kits' layers and image configs.
//
// llb.Merge is the layer arithmetic: the merged filesystem keeps their
// own layers rather than repacking their content, which is what makes a
// set cheap to build and its blobs shared with the kits it was built
// from. Later ones land over earlier ones, which is the order
// composition already means.
func mergeKitContent(ctx context.Context, c gwclient.Client, kits []resolvedKit, merged *spec.Descriptor, plat *ocispecs.Platform) (gwclient.Reference, []byte, error) {
	if err := checkKitCollisions(kits); err != nil {
		return nil, nil, err
	}

	// Layers answer a different question from declarations, so they
	// are ordered separately: a workload's filesystem is the base the
	// overlays land on, whatever the dependency graph says about what
	// it requires. The kits arrive in graph order, which can put a
	// provider ahead of the workload that requires it — correct for
	// hooks, and upside down for a root filesystem.
	p := platformOrDefault(plat)
	states := make([]llb.State, 0, len(kits))
	for _, k := range kits {
		if k.descriptor.Kind == spec.KindWorkload {
			states = append(states, k.state)
		}
	}
	for _, k := range kits {
		if k.descriptor.Kind != spec.KindWorkload {
			states = append(states, k.state)
		}
	}

	var st llb.State
	if len(states) == 1 {
		st = states[0]
	} else {
		st = llb.Merge(states, llb.WithCustomName("[kit] merge the set's kits"))
	}
	def, err := st.Marshal(ctx, llb.Platform(p))
	if err != nil {
		return nil, nil, err
	}
	res, err := c.Solve(ctx, gwclient.SolveRequest{Definition: def.ToPB(), Evaluate: true})
	if err != nil {
		return nil, nil, fmt.Errorf("merge layers: %w", err)
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, nil, err
	}

	configJSON, err := mergedImageConfig(kits, merged, plat)
	if err != nil {
		return nil, nil, err
	}
	return ref, configJSON, nil
}

// checkKitCollisions rejects two kits staging their sources under one
// name, which would leave the merged filesystem describing one of them
// with the other's declarations.
//
// It is deliberately narrower than assemble.CheckCollisions, which
// refuses a create-time composition where any two kits contribute the
// same file. That check reads every kit's layer inventory, and a
// frontend cannot: it has no access to layer blobs, and the filesystem
// it can reach is only walkable one directory per round trip
// (buildkit's ReadDir skips into no subdirectory, whatever include
// pattern it is given), so inventorying a workload's rootfs would cost
// thousands of calls per platform.
//
// So a merged set can carry a file collision that composing the same
// kits at create would have refused, resolved by merge order the way
// any overlay is. §9.5 records the difference; judging it belongs
// where the layers can actually be read, which is the conformance
// suite against the published artifact.
func checkKitCollisions(kits []resolvedKit) error {
	owners := map[string]string{}
	for _, k := range kits {
		if k.stem == "" {
			continue
		}
		if prev, dup := owners[k.stem]; dup {
			return fmt.Errorf("%s and %s both stage their sources at %s; one of them would describe the other",
				prev, k.reference, path.Join(tckkit.StagedKitRoot, k.stem))
		}
		owners[k.stem] = k.reference
	}
	return nil
}

// mergedImageConfig folds the listed kits' image configs into the
// merged kit's, by the arithmetic the assembler uses at create.
//
// A workload among them anchors the runtime contract — entrypoint, cmd,
// user, working dir travel unchanged, and the same fields in a mixin are
// ignored, because they exist for a standalone docker run of it. A set
// with no workload has no anchor, so the merged kit is itself a mixin:
// the additive fields still merge, and a contract field survives only
// when exactly one of them states it, which keeps docker run on the
// merged overlay behaving as its one author wrote.
func mergedImageConfig(kits []resolvedKit, merged *spec.Descriptor, plat *ocispecs.Platform) ([]byte, error) {
	p := platformOrDefault(plat)
	platform := ocispecs.Platform{OS: p.OS, Architecture: p.Architecture, Variant: p.Variant}

	inputs := make([]assemble.Input, 0, len(kits))
	for _, k := range kits {
		config := k.config.Config
		inputs = append(inputs, assemble.Input{
			Name: k.reference,
			// The manifest is only read for its layer count, which has
			// to agree with the config's diff_ids; the layers are not
			// assembled here, so both sides are stated empty.
			Manifest: ocispecs.Manifest{},
			Config:   ocispecs.Image{Platform: platform, Config: config, RootFS: ocispecs.RootFS{}},
		})
	}

	var base assemble.Input
	var overlays []assemble.Input
	if merged.Kind == spec.KindWorkload {
		// The workload anchors the contract; the kits around it, in
		// composition order, are the overlays.
		for i, k := range kits {
			if k.descriptor.Kind == spec.KindWorkload {
				base = inputs[i]
				continue
			}
			overlays = append(overlays, inputs[i])
		}
	} else {
		// Nothing anchors a workload-free set, so the merge starts from
		// an empty config and every one of them is additive.
		base = assemble.Input{Name: "the merged overlay", Config: ocispecs.Image{Platform: platform}}
		overlays = inputs
	}

	result, err := assemble.Merge(base, overlays)
	if err != nil {
		return nil, err
	}
	config := result.Config
	if err := applyArgExports(&config.Config, kits); err != nil {
		return nil, err
	}
	config.Platform = platform
	config.RootFS = ocispecs.RootFS{}
	config.History = nil

	if merged.Kind != spec.KindWorkload {
		contract := soleContract(kits)
		config.Config.Entrypoint = contract.Entrypoint
		config.Config.Cmd = contract.Cmd
		config.Config.User = contract.User
		config.Config.WorkingDir = contract.WorkingDir
	}
	return json.Marshal(config)
}

// applyArgExports sets the variables the kits' args bound, over
// whatever the merged config already says.
//
// An override rather than another contribution to the merge: the arg
// said this variable holds this value, and appending would leave the
// image's own entry beside it — a duplicate key for a workload, a
// conflict the assembler reports for a mixin, and for PATH the
// additive treatment a mixin's PATH gets, which an exact value is not.
//
// Two kits binding one variable to different values is refused here,
// because that is the disagreement the assembler would have caught had
// these been ordinary contributions, and nothing downstream can see it
// once the values are written in.
func applyArgExports(config *ocispecs.ImageConfig, kits []resolvedKit) error {
	type binding struct {
		value string
		owner string
	}
	bound := map[string]binding{}
	var names []string
	for _, k := range kits {
		for name, value := range k.env {
			if held, ok := bound[name]; ok {
				if held.value != value {
					return fmt.Errorf("%s and %s both export %s, as %q and %q; one value would have to win and neither is more right, so pin them to the same value or drop one",
						held.owner, k.reference, name, held.value, value)
				}
				continue
			}
			bound[name] = binding{value: value, owner: k.reference}
			names = append(names, name)
		}
	}
	// Sorted, so the same inputs write the same config: a map's
	// iteration order would otherwise move these between builds and
	// change the config digest for nothing.
	sort.Strings(names)

	// Every entry for an overridden name goes, not just the first: an
	// OCI config may carry a key twice, and a runtime reading the last
	// one would see the value this override was meant to replace. The
	// first position is kept, so the rest of the environment does not
	// shift around for nothing.
	written := map[string]bool{}
	out := make([]string, 0, len(config.Env)+len(names))
	for _, existing := range config.Env {
		name, _, found := strings.Cut(existing, "=")
		binding, override := bound[name]
		switch {
		case !found || !override:
			out = append(out, existing)
		case written[name]:
			// A duplicate the config carried; the override is written.
		default:
			out = append(out, name+"="+binding.value)
			written[name] = true
		}
	}
	for _, name := range names {
		if !written[name] {
			out = append(out, name+"="+bound[name].value)
		}
	}
	config.Env = out
	return nil
}

// soleContract picks the runtime-contract fields of a workload-free
// set: the one kit that states them, or none.
//
// Assembly ignores a mixin's contract fields, so nothing composed
// depends on this; it decides only what a standalone docker run of the
// merged overlay does. One author's statement carries through, and two
// disagreeing ones leave the merged overlay with neither rather than
// with whichever kit happened to be ordered last.
func soleContract(kits []resolvedKit) ocispecs.ImageConfig {
	var stated []ocispecs.ImageConfig
	for _, k := range kits {
		cfg := k.config.Config
		// All four fields, not just the launch argv: the merged config
		// takes user and working directory from here too, so a kit
		// stating only USER would have it read as no contract at all
		// and then overwritten with nothing — and a second such kit
		// would not count as the disagreement it is.
		if len(cfg.Entrypoint) == 0 && len(cfg.Cmd) == 0 && cfg.User == "" && cfg.WorkingDir == "" {
			continue
		}
		stated = append(stated, ocispecs.ImageConfig{
			Entrypoint: cfg.Entrypoint,
			Cmd:        cfg.Cmd,
			User:       cfg.User,
			WorkingDir: cfg.WorkingDir,
		})
	}
	if len(stated) != 1 {
		return ocispecs.ImageConfig{}
	}
	return stated[0]
}

// stageSetContext stages conditional bodies separately, or concatenates
// unconditional bodies with headings identifying their contributing Kits.
func stageSetContext(ctx context.Context, c gwclient.Client, ref gwclient.Reference, plat *ocispecs.Platform, staged string, sources []spec.ContextSource) (gwclient.Reference, error) {
	// Create-time expansion only reaches the descriptor, never staged layers.
	// Validate every body before writing either separate or combined content.
	for _, source := range sources {
		if names := spec.ReferencedArgs([]byte(source.Content)); len(names) > 0 {
			return nil, fmt.Errorf("the agent context from %s references %v, and a set stages its kits' guidance into files, which create-phase expansion never reaches; pin the value in that kit's args, or drop the reference from the body",
				source.Reference, names)
		}
	}
	if len(sources) > 0 && sources[0].Target != "" {
		for _, source := range sources {
			var err error
			ref, err = stageGuidance(ctx, c, ref, plat, source.Target, []byte(source.Content))
			if err != nil {
				return nil, err
			}
		}
		return ref, nil
	}

	if len(sources) == 0 {
		return ref, nil
	}
	var body strings.Builder
	for _, source := range sources {
		// Already read, from the kit that named it: readContextSources
		// resolves every path before the layers merge, because
		// afterwards there is no telling whose file a path holds.
		content := source.Content
		if strings.TrimSpace(content) == "" {
			continue
		}
		if body.Len() > 0 {
			body.WriteString("\n")
		}
		fmt.Fprintf(&body, "<!-- from %s -->\n%s", source.Reference, content)
		if !strings.HasSuffix(content, "\n") {
			body.WriteString("\n")
		}
	}
	// Staged even when every body turned out to be empty. The merged
	// descriptor already points contentFile at this path — the entry
	// exists because contributions declared the type — so skipping the
	// write would leave it naming a file the image does not carry,
	// which is exactly what the conformance checks refuse.
	return stageGuidance(ctx, c, ref, plat, staged, []byte(body.String()))
}
