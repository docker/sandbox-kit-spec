package fetch

import (
	"context"
	"fmt"
	"maps"
	"path"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Options configures Assemble. Its zero value loads from registries using
// Docker credentials and accepts the library's known capability types.
// Layer validation is opt-in; set LayerValidator to DefaultLayerValidator to run
// the built-in layer integrity, extraction, and collision checks.
type Options struct {
	Loader KitLoader
	// CapabilitySelector receives the operation context, expanded owning Kit
	// descriptor (including DisplayName), and capability. It decides availability
	// and policy without applying effects; callback inputs are deeply copied values.
	// Nil accepts KnownCapabilities; a runtime with fewer implementations supplies
	// spec.Supported with its actual claims. Groups remain atomic.
	CapabilitySelector spec.SelectCapability
	Overrides          Overrides
	// LayerValidator checks the complete set after metadata composition and
	// descriptor resolution. Nil skips layer checks without calling LayerLoader;
	// the caller is responsible for integrity, safe extraction, resource limits,
	// and cross-Kit collisions before using the image. Use DefaultLayerValidator for
	// the built-in checks or supply a validator backed by the runtime's store.
	LayerValidator LayerValidator
	// OnProgress receives serialized callbacks on the calling goroutine. It must
	// return promptly; cancellation uses Assemble's context. Nil disables events.
	OnProgress func(Progress)
}

// Overrides applies to container creation, not the reusable image defaults.
type Overrides struct {
	// Env adds or replaces values after image defaults and Kit argument exports.
	Env map[string]string
	// WorkingDir replaces the workload working directory when non-nil.
	// A replacement must be an absolute container path.
	WorkingDir *string
}

// Result contains the image, selected declarations, and container settings.
// Persist Resolved.Descriptor and Resolved.Selections for restart; recreate
// resolves and selects afresh. Use Resolved.Kits with resolve.Resolve and
// resolve.LockFrom for permission checks and locking.
type Result struct {
	Image    *assemble.Image
	Resolved *Resolved
	// Environment is the full image-default + arg-export + override snapshot.
	// Values are literal; kit.env references in capability configs use this snapshot.
	Environment map[string]string
	WorkingDir  string
}

// Assemble loads a closed set of Kit references, composes image defaults,
// expands and selects declarations using the final container environment, and
// invokes Options.LayerValidator when non-nil.
// It does not publish blobs, create a container, or apply capabilities.
// Image.Manifest and Image.WriteMetadata serialize consistent image metadata;
// Environment and WorkingDir are applied separately at container creation.
func Assemble(ctx context.Context, requests []Request, options Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("assemble: empty kit set")
	}
	if err := validateOverrides(options.Overrides); err != nil {
		return nil, err
	}
	options.Overrides.Env = maps.Clone(options.Overrides.Env)
	if options.Loader == nil {
		client, err := New(WithDockerCredentials())
		if err != nil {
			return nil, err
		}
		options.Loader = client.LoadKit
	}
	loaded := make(map[string]*LoadedKit, len(requests))
	kits := make([]*Kit, 0, len(requests))
	args := make([]map[string]string, 0, len(requests))
	for _, request := range requests {
		if _, exists := loaded[request.Reference]; exists {
			return nil, fmt.Errorf("assemble: duplicate reference %q", request.Reference)
		}
		var input *LoadedKit
		var kit *Kit
		err := progressStep(ctx, options.OnProgress, Progress{Stage: StageLoad, Reference: request.Reference}, func() error {
			var err error
			input, err = options.Loader(ctx, request.Reference)
			if err != nil {
				return err
			}
			kit, err = validateLoadedKit(request.Reference, input)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("load kit %s: %w", request.Reference, err)
		}
		loaded[request.Reference] = input
		kits = append(kits, kit)
		args = append(args, maps.Clone(request.Args))
	}
	var image *assemble.Image
	err := progressStep(ctx, options.OnProgress, Progress{Stage: StageCompose}, func() error {
		// Assembly asks for runtime image references, while the retained inputs are
		// indexed by consumption reference so two tags never silently share metadata.
		units, err := Units(kits)
		if err != nil {
			return err
		}
		// Image composition depends on Kit relationships, not capability
		// selection. The final image environment is needed to select requests.
		for _, unit := range units {
			unit.Descriptor.Capabilities = nil
		}
		inputs := make(map[string]assemble.Input, len(units))
		for _, kit := range units {
			input := loaded[kit.Reference]
			inputs[kit.Image] = assemble.Input{Manifest: input.Manifest, Config: input.Config}
		}
		image, err = assemble.Assemble(ctx, units, func(_ context.Context, ref string) (assemble.Input, error) {
			input, ok := inputs[ref]
			if !ok {
				return assemble.Input{}, fmt.Errorf("image %s was not loaded", ref)
			}
			return input, nil
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	environment := make(map[string]string)
	for _, entry := range image.Config.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, '\x00') {
			return nil, fmt.Errorf("assemble: malformed image environment entry")
		}
		environment[key] = value
	}
	var resolved *Resolved
	err = progressStep(ctx, options.OnProgress, Progress{Stage: StageResolve}, func() error {
		selector := options.CapabilitySelector
		if selector == nil {
			selector = spec.Supported(spec.KnownCapabilities()...)
		}
		var err error
		resolved, err = mergeKits(ctx, kits, args, false, WithCapabilitySelector(selector), WithEnvironment(environment, options.Overrides.Env))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if options.LayerValidator != nil {
		if err := options.LayerValidator(ctx, resolved.Kits, loaded, options.OnProgress); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	maps.Copy(environment, resolved.ContainerEnv)
	maps.Copy(environment, options.Overrides.Env)
	workingDir := image.Config.Config.WorkingDir
	if options.Overrides.WorkingDir != nil {
		workingDir = *options.Overrides.WorkingDir
	}
	return &Result{Image: image, Resolved: resolved, Environment: environment, WorkingDir: workingDir}, nil
}

func validateOverrides(overrides Overrides) error {
	for name, value := range overrides.Env {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("assemble: environment overrides require nonempty names without '=' or NUL and values without NUL")
		}
	}
	if overrides.WorkingDir != nil && (!path.IsAbs(*overrides.WorkingDir) || strings.ContainsRune(*overrides.WorkingDir, '\x00')) {
		return fmt.Errorf("assemble: working directory override must be an absolute container path without NUL")
	}
	return nil
}

func validateLoadedKit(ref string, input *LoadedKit) (*Kit, error) {
	if input == nil {
		return nil, fmt.Errorf("loader returned no kit")
	}
	if err := input.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("kit digest: %w", err)
	}
	// OCI permits an omitted embedded mediaType. Registry loaders already
	// checked the enclosing descriptor; custom loaders supply an image manifest
	// by contract. Default only the validation hint, preserving loaded metadata.
	mediaType := input.Manifest.MediaType
	if mediaType == "" {
		mediaType = ocispec.MediaTypeImageManifest
	}
	if err := requirePlainImageManifest(ocispec.Descriptor{MediaType: mediaType}, input.Manifest); err != nil {
		return nil, err
	}
	if len(input.Manifest.Layers) == 0 {
		return nil, fmt.Errorf("kit manifest must contain at least one layer")
	}
	if err := input.Manifest.Config.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("config digest: %w", err)
	}
	if input.Manifest.Config.Size < 0 {
		return nil, fmt.Errorf("image config has negative size")
	}
	// Merge ignores entries without '=' in mixins. Validate every input before
	// composition can discard or shadow an invalid value. Do not echo values:
	// image environment entries may contain credentials.
	for i, entry := range input.Config.Config.Env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsRune(entry, '\x00') {
			return nil, fmt.Errorf("malformed image environment entry at index %d: require a nonempty name, '=' and no NUL", i)
		}
	}
	for _, diffID := range input.Config.RootFS.DiffIDs {
		if err := diffID.Validate(); err != nil {
			return nil, fmt.Errorf("layer diff ID: %w", err)
		}
	}
	if input.Config.RootFS.Type != "layers" || len(input.Config.RootFS.DiffIDs) != len(input.Manifest.Layers) {
		return nil, fmt.Errorf("image config rootfs does not match manifest layers")
	}
	if input.Config.OS != "linux" || input.Config.Architecture == "" {
		return nil, fmt.Errorf("kit image must declare a Linux platform")
	}
	for _, layer := range input.Manifest.Layers {
		if err := layer.Digest.Validate(); err != nil {
			return nil, fmt.Errorf("layer digest: %w", err)
		}
		if layer.Size < 0 {
			return nil, fmt.Errorf("layer %s has negative size", layer.Digest)
		}
	}
	raw := input.Descriptor
	if len(raw) == 0 {
		raw = []byte(input.Manifest.Annotations[spec.AnnotationDescriptor])
	}
	if len(raw) == 0 {
		return nil, ErrNotAKit
	}
	descriptor, err := spec.Decode(raw)
	if err != nil {
		return nil, spec.WithSource(err, ref, raw)
	}
	if _, err := spec.ValidatePublished(raw, descriptor); err != nil {
		return nil, spec.WithSource(err, ref, raw)
	}
	return &Kit{Reference: ref, Digest: input.Digest.String(), Image: input.Image, Descriptor: descriptor, Raw: append([]byte(nil), raw...)}, nil
}

// Stage identifies work independently of a caller's display strings.
type Stage string

const (
	StageLoad Stage = "load"
	// StageResolve includes arg and environment expansion, validation, atomic
	// capability selection, and validation of the composed descriptor.
	StageResolve    Stage = "resolve"
	StageCompose    Stage = "compose"
	StageInventory  Stage = "inventory"
	StageCollisions Stage = "collisions"
)

type ProgressState string

const (
	ProgressStarted   ProgressState = "started"
	ProgressCompleted ProgressState = "completed"
	ProgressFailed    ProgressState = "failed"
)

// Progress describes one stage transition. Reference and Layer identify work
// when applicable. It never includes environment values or file contents;
// detailed errors are returned by Assemble. Inventory events include cache replays,
// which consume the operation budget without reopening the blob.
type Progress struct {
	Stage     Stage
	State     ProgressState
	Reference string
	Layer     digest.Digest
}

func progressStep(ctx context.Context, report func(Progress), event Progress, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if report != nil {
		event.State = ProgressStarted
		report(event)
	}
	err := ctx.Err()
	if err == nil {
		err = run()
	}
	if err == nil {
		err = ctx.Err()
	}
	if report != nil {
		event.State = ProgressCompleted
		if err != nil {
			event.State = ProgressFailed
		}
		report(event)
	}
	if err == nil {
		err = ctx.Err()
	}
	return err
}
