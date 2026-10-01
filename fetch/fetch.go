package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// ErrNotAKit is returned when a reference resolves to an image whose
// manifest carries no kit descriptor annotation.
var ErrNotAKit = fmt.Errorf("manifest carries no %s annotation", spec.AnnotationDescriptor)

// Kit is one published kit with its consumption reference.
type Kit struct {
	// Reference is the reference the caller named.
	Reference string

	// Digest is the digest the reference resolved to. A multi-platform
	// kit pins the index; a single-platform kit pins its manifest.
	Digest string

	// Image is the runtime-resolvable reference for this Kit's content.
	// When empty, Units derives a digest-pinned image from Reference and Digest.
	Image string

	// Descriptor is the published descriptor.
	Descriptor *spec.Descriptor

	// Raw is the annotation bytes Descriptor was decoded from, which
	// ValidatePublished needs alongside the struct.
	Raw []byte
}

// Option configures a Client. Later options win.
type Option func(*config) error

type config struct {
	credential auth.CredentialFunc
	transport  http.RoundTripper
	plainHTTP  bool
	platform   ocispec.Platform
}

// WithCredential supplies registry credentials. Without it, requests are
// anonymous, which is enough for a public registry.
//
// The function receives the registry host (host:port) the request is
// about to authenticate to. Return auth.EmptyCredential for a host that
// should stay anonymous.
func WithCredential(fn auth.CredentialFunc) Option {
	return func(c *config) error {
		c.credential = fn
		return nil
	}
}

// DockerCredential reads the credential store `docker login` writes.
// Compose it inside WithCredential when only some registries should
// come from that store.
func DockerCredential() (auth.CredentialFunc, error) {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if err != nil {
		return nil, fmt.Errorf("open docker credential store: %w", err)
	}
	return credentials.Credential(store), nil
}

// WithDockerCredentials authenticates with DockerCredential.
func WithDockerCredentials() Option {
	return func(c *config) error {
		fn, err := DockerCredential()
		if err != nil {
			return err
		}
		c.credential = fn
		return nil
	}
}

// WithTransport sets the HTTP transport under the registry client.
// The default retry policy still wraps it. Nil uses http.DefaultTransport.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *config) error {
		c.transport = rt
		return nil
	}
}

// WithPlainHTTP reaches every registry over HTTP.
//
// A loopback registry does this without the option: the request never
// leaves the machine. Anywhere else, plain HTTP is a choice about a
// network someone else can be on, so it has to be asked for.
func WithPlainHTTP() Option {
	return func(c *config) error {
		c.plainHTTP = true
		return nil
	}
}

// WithPlatform selects the platform for descriptor and image reads.
// Descriptor resolution may use an index annotation without opening its
// platform manifest; LoadImage always reads the selected image and config.
// The default is linux on this process's architecture.
func WithPlatform(p ocispec.Platform) Option {
	return func(c *config) error {
		c.platform = p
		return nil
	}
}

// Client fetches Kit descriptors and image metadata. It is safe for concurrent use.
type Client struct {
	credential auth.CredentialFunc
	transport  http.RoundTripper
	plainHTTP  bool
	platform   ocispec.Platform
	cache      auth.Cache
}

// New returns a client. With no options it reads public registries anonymously,
// over HTTPS except for a loopback registry.
func New(opts ...Option) (*Client, error) {
	cfg := config{platform: defaultPlatform()}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}
	return &Client{
		credential: cfg.credential,
		transport:  cfg.transport,
		plainHTTP:  cfg.plainHTTP,
		platform:   cfg.platform,
		cache:      auth.NewCache(),
	}, nil
}

// Fetch reads one kit's published descriptor. It fetches manifests only.
func (c *Client) Fetch(ctx context.Context, ref string) (*Kit, error) {
	k, err := c.fetch(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", ref, err)
	}
	return k, nil
}

// Request is one kit to resolve. Args are that kit's create-phase
// values, keyed by its own arg names, so two kits that declare the
// same name still receive their own values.
type Request struct {
	// Reference is the caller's Kit identity. Client.Resolve requires an OCI
	// reference; Assemble can use source references with a custom KitLoader.
	Reference string
	Args      map[string]string
}

// Resolved is a runtime composition. Keep Kits alongside Descriptor so
// capability handlers can inspect each contributor's declarations.
type Resolved struct {
	// Selections records decisions in dependency order, alongside the published
	// descriptors. Kits contains expanded, selected contributions.
	Selections []KitSelection

	// Descriptor is the reconciled, selected declaration set. It does not retain
	// per-Kit provenance; use Kits where a capability requires attribution.
	Descriptor *spec.Descriptor

	// Kits are selected, unmerged contributions in dependency order, providers before
	// requirers (including the workload wherever its dependencies put it).
	// Each retains its consumption reference, pinned image identity, and
	// resolved create arguments and their Env exports, including defaults.
	// Descriptor.Args is cleared because the values have already been applied.
	// This is declaration order; image layers start with the workload.
	// A Kit can contribute several selected lifecycle/context entries. Apply
	// lifecycle from Descriptor; use spec.AgentContextsOf for per-Kit bodies.
	Kits []*resolve.Unit

	// ContainerEnv contains resolved env: argument exports. Apply them as
	// environment overrides when creating the container, after the image's
	// environment defaults. They are not baked into the assembled image.
	// This is the union of Kits' Env maps. Two Kits exporting different
	// values for the same name is an error.
	ContainerEnv map[string]string

	// Warnings contains advisory findings from validating the merged
	// descriptor.
	Warnings []string
}

// KitSelection retains the source needed to explain or persist a decision.
type KitSelection struct {
	Reference string

	// PublishedDescriptor is the decoded published descriptor before argument
	// and environment expansion or capability selection. It retains argument
	// declarations.
	PublishedDescriptor *spec.Descriptor

	// PublishedBytes holds the exact published descriptor bytes, preserving
	// source formatting for diagnostics and persistence.
	PublishedBytes []byte

	Selection spec.Selection
}

// ResolveOption configures create-time descriptor selection.
type ResolveOption func(*resolveOptions)
type resolveOptions struct {
	selector     spec.SelectCapability
	environment  map[string]string
	envOverrides map[string]string
}

// WithCapabilitySelector lets a runtime decide each expanded request. The
// callback receives the operation context and a deeply copied value of the owning
// Kit's expanded descriptor, including DisplayName and all declarations. The
// library retains atomic groups, ordering, validation, and source records.
// Decision messages are recorded for each member and included in required
// rejection errors.
func WithCapabilitySelector(selector spec.SelectCapability) ResolveOption {
	return func(o *resolveOptions) { o.selector = selector }
}

// WithEnvironment supplies composed image defaults and runtime overrides for
// Resolve and ResolvePartial. Kit argument exports replace defaults, and
// overrides win last. Nil maps are empty; empty values are present values.
// The maps are copied when this option is constructed. Resolve does not load
// image configs or read the host environment. ContainerEnv still contains only
// Kit argument exports; callers apply the same precedence at container creation.
func WithEnvironment(defaults, overrides map[string]string) ResolveOption {
	defaults, overrides = maps.Clone(defaults), maps.Clone(overrides)
	return func(o *resolveOptions) {
		o.environment = defaults
		o.envOverrides = overrides
	}
}

// Resolve fetches the requests, resolves them as a runnable set —
// exactly one workload — and merges their descriptors. The result is
// always validated in effective form before returning. WithEnvironment supplies
// image defaults and runtime overrides for kit.env references; without it, only
// Kit argument exports are available to environment expansion.
//
// Each kit's create-phase args are resolved and expanded before the
// composition, which is what spec.Compose requires of a contribution. A
// required arg with no value is an error, and a value that is still a
// ${{ kit.args.* }} reference is refused: an assembled spec has no
// set declaration left to bound a re-export. Build-phase args are
// already baked into the annotation. An env: export is returned on
// Resolved.ContainerEnv, because clearing the declaration would otherwise drop it.
//
// Contributions are ordered by the dependency graph, providers before
// the kits that require them, which is the order declaration merge
// means by "first".
func (c *Client) Resolve(ctx context.Context, reqs []Request, opts ...ResolveOption) (*Resolved, error) {
	return c.resolve(ctx, reqs, false, opts...)
}

// ResolvePartial is Resolve for a set that does not have to be
// runnable. A set of mixins yields a mixin descriptor and the expanded Kits.
// To add a workload later, resolve the original requests with that workload;
// the result's descriptor alone does not retain per-Kit declarations.
// More than one workload is still an error.
func (c *Client) ResolvePartial(ctx context.Context, reqs []Request, opts ...ResolveOption) (*Resolved, error) {
	return c.resolve(ctx, reqs, true, opts...)
}

func (c *Client) resolve(ctx context.Context, reqs []Request, partial bool, opts ...ResolveOption) (*Resolved, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("fetch: empty kit set")
	}
	kits := make([]*Kit, 0, len(reqs))
	args := make([]map[string]string, 0, len(reqs))
	for _, req := range reqs {
		k, err := c.Fetch(ctx, req.Reference)
		if err != nil {
			return nil, err
		}
		kits = append(kits, k)
		args = append(args, req.Args)
	}
	return mergeKits(ctx, kits, args, partial, opts...)
}

// Units builds the resolver's input from fetched kits.
//
// Each descriptor is copied and its version set to the one the
// reference's tag supplies, so a merge records the same version the
// resolver judged. The fetched Kit is left as the registry served it.
// An explicit Kit.Image is used unchanged; otherwise Reference must be an
// image reference from which a digest-pinned image can be derived.
func Units(kits []*Kit) ([]*resolve.Unit, error) {
	units := make([]*resolve.Unit, 0, len(kits))
	for _, k := range kits {
		if k == nil || k.Descriptor == nil {
			return nil, fmt.Errorf("fetch: kit %s has no descriptor", refOf(k))
		}
		d := *k.Descriptor
		d.Version = resolve.EffectiveProvideVersion(k.Reference, k.Descriptor)
		image := k.Image
		if image == "" {
			var err error
			image, err = pinnedImage(k.Reference, k.Digest)
			if err != nil {
				return nil, fmt.Errorf("fetch: kit %s: %w", k.Reference, err)
			}
		}
		units = append(units, &resolve.Unit{
			Reference:  k.Reference,
			Digest:     k.Digest,
			Image:      image,
			Descriptor: &d,
		})
	}
	return units, nil
}

func mergeKits(ctx context.Context, kits []*Kit, args []map[string]string, partial bool, opts ...ResolveOption) (*Resolved, error) {
	options := resolveOptions{selector: spec.Supported(spec.KnownCapabilities()...)}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	hadEnvironment := false
	prepared := make([]*Kit, len(kits))
	exports := make([]map[string]string, len(kits))
	values := make([]map[string]string, len(kits))
	for i, k := range kits {
		var err error
		values[i], exports[i], err = resolveKitArgs(k, args[i])
		if err != nil {
			return nil, err
		}
	}
	env, err := combineExports(kits, exports)
	if err != nil {
		return nil, err
	}
	environment := map[string]string{}
	maps.Copy(environment, options.environment)
	maps.Copy(environment, env)
	maps.Copy(environment, options.envOverrides)
	for i, k := range kits {
		expanded, err := expandResolvedKit(k, values[i], exports[i], environment)
		if err != nil {
			return nil, err
		}
		cp := *k
		cp.Descriptor = expanded.descriptor
		hadEnvironment = hadEnvironment || expanded.hadEnvironment
		prepared[i] = &cp
	}
	units, err := Units(prepared)
	if err != nil {
		return nil, err
	}
	for i, unit := range units {
		unit.Args = values[i]
		unit.Env = exports[i]
	}
	resolution, err := resolve.Dependencies(units, partial)
	if err != nil {
		return nil, err
	}
	publishedByReference := make(map[string]*Kit, len(kits))
	for _, k := range kits {
		publishedByReference[k.Reference] = k
	}
	ordered := resolution.Topological()
	var selections []KitSelection
	contributions := make([]spec.Contribution, 0, len(ordered))
	for i, u := range ordered {
		published := publishedByReference[u.Reference]
		selected, err := spec.SelectCapabilities(ctx, withSelectionSources(u.Descriptor, u.Reference), options.selector)
		if err != nil {
			return nil, spec.WithSource(err, u.Reference, published.Raw)
		}
		selections = append(selections, KitSelection{
			Reference:           u.Reference,
			PublishedDescriptor: published.Descriptor,
			PublishedBytes:      published.Raw,
			Selection:           selected,
		})
		cp := *u
		d := *u.Descriptor
		d.Capabilities = selected.Capabilities
		cp.Descriptor = &d
		ordered[i] = &cp
		contributions = append(contributions, spec.Contribution{Reference: u.Reference, Descriptor: &d})
	}
	merged, err := spec.Compose(contributions)
	if err != nil {
		if hadEnvironment {
			return nil, fmt.Errorf("compose capabilities after environment expansion: incompatible declarations")
		}
		return nil, err
	}
	result := &Resolved{Descriptor: merged, Kits: ordered, ContainerEnv: env, Selections: selections}
	// Validate the bytes the descriptor serializes to: independently valid
	// inputs can exceed the document budget once combined.
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("encode merged descriptor: %w", err)
	}
	result.Warnings, err = spec.ValidateEffective(raw, merged)
	if err != nil {
		if hadEnvironment {
			return nil, fmt.Errorf("merged descriptor is invalid after environment expansion")
		}
		return nil, spec.WithSource(err, "merged descriptor", raw)
	}
	return result, nil
}

// Complete source metadata before selection so refusal errors and successful
// records use the same attribution. Copy only what normalization changes;
// SelectCapabilities isolates configs before handing entries to policy.
func withSelectionSources(d *spec.Descriptor, reference string) *spec.Descriptor {
	var copyItems func([]spec.Capability, string) []spec.Capability
	copyItems = func(items []spec.Capability, prefix string) []spec.Capability {
		out := append([]spec.Capability(nil), items...)
		for i := range out {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			source := spec.CapabilitySource{Path: path}
			if out[i].Source != nil {
				source = *out[i].Source
			}
			if source.Kit == "" {
				source.Kit = reference
			}
			out[i].Source = &source
			if out[i].Group != nil {
				group := *out[i].Group
				group.Capabilities = copyItems(group.Capabilities, path+".group.capabilities")
				out[i].Group = &group
			}
		}
		return out
	}
	cp := *d
	cp.Capabilities = copyItems(d.Capabilities, "capabilities")
	return &cp
}

type expandedKit struct {
	descriptor     *spec.Descriptor
	raw            []byte
	args           map[string]string
	env            map[string]string
	hadEnvironment bool
}

// expandKit resolves one kit's create-phase args into its published
// descriptor. env is the variables its env: args bound; the declaration
// those lived on is cleared, so the map is the only copy. The fetched
// Kit is left as the registry served it.
func expandKit(k *Kit, args map[string]string) (*expandedKit, error) {
	values, env, err := resolveKitArgs(k, args)
	if err != nil {
		return nil, err
	}
	return expandResolvedKit(k, values, env, env)
}

func resolveKitArgs(k *Kit, args map[string]string) (map[string]string, map[string]string, error) {
	if k == nil || k.Descriptor == nil {
		return nil, nil, fmt.Errorf("fetch: kit %s has no descriptor", refOf(k))
	}
	if _, err := spec.ValidatePublished(k.Raw, k.Descriptor); err != nil {
		return nil, nil, spec.WithSource(err, k.Reference+" (published descriptor)", k.Raw)
	}
	values, err := spec.KitArgValues(k.Descriptor.Args, args)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	for name, value := range values {
		if spec.ContainsKitPlaceholder(value) {
			return nil, nil, fmt.Errorf("%s: arg %q must be a literal value without Kit placeholders", k.Reference, name)
		}
	}
	env, err := argExports(k.Descriptor.Args, values)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	return values, env, nil
}

var expandedCapabilityPath = regexp.MustCompile(`^capabilities\[[0-9]+\](?:\.group\.capabilities\[[0-9]+\])?`)

func expandResolvedKit(k *Kit, values, exports, environment map[string]string) (*expandedKit, error) {
	expanded, err := spec.ExpandCreateArgs(k.Raw, k.Descriptor.Args, values)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k.Reference, err)
	}
	d, err := spec.Decode(expanded)
	if err != nil {
		return nil, spec.WithSource(err, k.Reference+" (expanded descriptor)", expanded)
	}
	hadEnvironment := spec.HasEnvReferences(d.Capabilities)
	if hadEnvironment {
		d, err = spec.ExpandEnvironment(d, environment)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k.Reference, err)
		}
		expanded, err = json.Marshal(d)
		if err != nil {
			return nil, fmt.Errorf("%s: encode environment-expanded descriptor", k.Reference)
		}
	}
	if _, err := spec.ValidateExpandedDeclarations(expanded, d); err != nil {
		if hadEnvironment {
			// Expanded values may be secrets. Neither validator details nor source
			// excerpts from the expanded document are safe to include in errors.
			at := ""
			var field *spec.FieldError
			if errors.As(err, &field) {
				// Keep only declaration indices, never config keys or values.
				if member := expandedCapabilityPath.FindString(field.Path); member != "" {
					at = member + ": "
				}
			}
			return nil, fmt.Errorf("%s: %sinvalid capability configuration after environment expansion", k.Reference, at)
		}
		return nil, spec.WithSource(err, k.Reference+" (expanded descriptor)", expanded)
	}
	d.Args = nil
	return &expandedKit{descriptor: d, raw: expanded, args: values, env: exports, hadEnvironment: hadEnvironment}, nil
}

// argExports collects the variables one kit's create-phase args bind.
// The effect lives only on the declaration, which the merge drops.
func argExports(decls map[string]spec.Arg, values map[string]string) (map[string]string, error) {
	env := map[string]string{}
	for name, decl := range decls {
		if decl.BuildArg != "" || decl.Env == "" {
			continue
		}
		value, ok := values[name]
		if !ok {
			continue
		}
		if held, exists := env[decl.Env]; exists && held != value {
			return nil, fmt.Errorf("args export different values for %s; supply values that agree", decl.Env)
		}
		env[decl.Env] = value
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

// combineExports folds each kit's exports into one map. Two kits
// binding one variable to different values is the disagreement the
// image config would otherwise have to guess through.
func combineExports(kits []*Kit, exports []map[string]string) (map[string]string, error) {
	type binding struct {
		value string
		owner string
	}
	bound := map[string]binding{}
	var out map[string]string
	for i, env := range exports {
		for name, value := range env {
			if held, ok := bound[name]; ok {
				if held.value != value {
					return nil, fmt.Errorf("%s and %s export different values for %s", held.owner, kits[i].Reference, name)
				}
				continue
			}
			bound[name] = binding{value: value, owner: kits[i].Reference}
			if out == nil {
				out = map[string]string{}
			}
			out[name] = value
		}
	}
	return out, nil
}

func refOf(k *Kit) string {
	if k == nil || k.Reference == "" {
		return "(none)"
	}
	return k.Reference
}

func pinnedImage(ref, dgst string) (string, error) {
	named, err := parseNamed(ref)
	if err != nil {
		return "", err
	}
	parsed, err := digest.Parse(dgst)
	if err != nil {
		return "", fmt.Errorf("digest %q: %w", dgst, err)
	}
	canonical, err := withDigest(named, parsed)
	if err != nil {
		return "", err
	}
	return canonical, nil
}
