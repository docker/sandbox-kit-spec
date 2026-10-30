package fetch

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestAssembleWithoutLayerValidatorPreservesResult(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	base.Config.Config.Env = []string{"HOME=/home/agent"}
	base.Config.Config.WorkingDir = "/image-workspace"
	mixin.Config.Config.Env = []string{"TOOL_DIR=/opt/tool"}
	base.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Args: map[string]spec.Arg{"team": {Required: true, Env: "TEAM"}},
		Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{
			"path": "${{ kit.env.HOME }}/config", "content": "${{ kit.args.team }}",
		}}}}},
	})
	mixin.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
		Capabilities: []spec.Capability{{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
			{Type: spec.CapabilityLongRunning}, {Type: "com.example/missing@1"},
		}}}},
	})
	requests := fixtureRequests(2)
	requests[0].Args = map[string]string{"team": "alpha"}
	workdir := "/workspace"
	options := Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin), Overrides: Overrides{
		Env: map[string]string{"HOME": "/home/runtime"}, WorkingDir: &workdir,
	}}
	want, err := Assemble(t.Context(), requests, options)
	require.NoError(t, err)
	lifecycle, err := spec.LifecycleOf(want.Resolved.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "/home/runtime/config", lifecycle.Files[0].Path)
	require.Equal(t, "alpha", lifecycle.Files[0].Content)
	require.Len(t, want.Resolved.Selections[1].Selection.Skipped, 1)
	for _, input := range []*LoadedKit{base, mixin} {
		input.LayerLoader = func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
			t.Fatal("LayerLoader called without a LayerValidator")
			return nil, nil
		}
	}
	var stages []Stage
	options.LayerValidator = nil
	options.OnProgress = func(p Progress) {
		if p.State == ProgressStarted {
			stages = append(stages, p.Stage)
		}
	}
	got, err := Assemble(t.Context(), requests, options)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, []Stage{StageLoad, StageLoad, StageCompose, StageResolve}, stages)
}

func TestAssembleWithoutLayerValidatorSkipsCollisions(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "shared")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "shared")
	options := Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin)}
	_, err := Assemble(t.Context(), fixtureRequests(2), options)
	require.ErrorContains(t, err, "file collisions")
	options.LayerValidator = nil
	result, err := Assemble(t.Context(), fixtureRequests(2), options)
	require.NoError(t, err)
	require.Len(t, result.Image.Layers, 2)
}

func TestAssembleWithoutLayerValidatorHonorsResolveCallbackCancellation(t *testing.T) {
	input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	input.LayerLoader = nil
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var events []Progress
	result, err := Assemble(ctx, fixtureRequests(1), Options{
		Loader: fixtureLoader(input),
		OnProgress: func(p Progress) {
			events = append(events, p)
			if p.Stage == StageResolve && p.State == ProgressCompleted {
				cancel()
			}
		},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	require.Equal(t, Progress{Stage: StageResolve, State: ProgressCompleted}, events[len(events)-1])
}

func TestAssembleWithoutLayerValidatorStillValidatesMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*LoadedKit)
		message string
	}{
		{"metadata", func(k *LoadedKit) { k.Config.RootFS.DiffIDs = nil }, "rootfs"},
		{"descriptor", func(k *LoadedKit) { k.Descriptor = []byte(`{"schemaVersion":3,"kind":"invalid"}`) }, "kind"},
		{"composition", func(k *LoadedKit) {
			k.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin})
		}, "workload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			tc.mutate(input)
			result, err := Assemble(t.Context(), fixtureRequests(1), Options{Loader: fixtureLoader(input)})
			require.ErrorContains(t, err, tc.message)
			require.Nil(t, result)
		})
	}
}

func TestAssembleWithoutLayerReaders(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	for _, input := range []*LoadedKit{base, mixin} {
		input.LayerLoader = nil
	}
	options := Options{Loader: fixtureLoader(base, mixin)}
	result, err := Assemble(t.Context(), fixtureRequests(2), options)
	require.NoError(t, err)
	require.Len(t, result.Image.Layers, 2)
	options.LayerValidator = DefaultLayerValidator
	result, err = Assemble(t.Context(), fixtureRequests(2), options)
	require.ErrorContains(t, err, "LayerLoader")
	require.Nil(t, result)
}

func TestAssembleCustomLayerValidator(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
			// A store-backed validator may not need to read any layer bytes.
			base.LayerLoader, mixin.LayerLoader = nil, nil
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			validationErr := errors.New("store validation failed")
			calls := 0
			var events []Progress
			result, err := Assemble(ctx, fixtureRequests(2), Options{
				Loader:     fixtureLoader(base, mixin),
				OnProgress: func(p Progress) { events = append(events, p) },
				LayerValidator: func(gotCtx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress)) error {
					calls++
					require.Equal(t, ctx, gotCtx)
					require.Len(t, kits, 2)
					require.Len(t, loaded, 2)
					require.Same(t, base, loaded[fixtureRequests(2)[0].Reference])
					require.Same(t, mixin, loaded[fixtureRequests(2)[1].Reference])
					require.Equal(t, Progress{Stage: StageResolve, State: ProgressCompleted}, events[len(events)-1])
					require.NotNil(t, report)
					switch outcome {
					case "failure":
						return validationErr
					case "canceled":
						cancel()
					}
					return nil
				},
			})
			require.Equal(t, 1, calls)
			switch outcome {
			case "success":
				require.NoError(t, err)
				require.NotNil(t, result)
			case "failure":
				require.ErrorIs(t, err, validationErr)
				require.Nil(t, result)
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, result)
			}
		})
	}
}

func TestLayerValidatorCustomInventoryLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits InventoryLimits
	}{
		{"entries", InventoryLimits{Entries: 1}},
		{"path bytes", InventoryLimits{PathBytes: 3}},
		{"components", InventoryLimits{Components: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := layerFixture(t, spec.KindWorkload, "base", []tar.Header{{Name: "one/file", Mode: 0644}, {Name: "two/file", Mode: 0644}})
			opts := Options{Loader: fixtureLoader(input), LayerValidator: LayerValidatorWithLimits(tc.limits)}
			_, err := Assemble(t.Context(), fixtureRequests(1), opts)
			require.ErrorContains(t, err, "assembly inventory exceeds")
			opts.LayerValidator = LayerValidatorWithLimits(InventoryLimits{Entries: 2, PathBytes: 16, Components: 4})
			for range 2 { // The same validator starts with a fresh budget each time.
				_, err = Assemble(t.Context(), fixtureRequests(1), opts)
				require.NoError(t, err)
			}
		})
	}
	for _, limits := range []InventoryLimits{{Layers: -1}, {Entries: -1}, {PathBytes: -1}, {Components: -1}} {
		input, _ := assemblyFixture(t, spec.KindWorkload, "base", "file")
		_, err := Assemble(t.Context(), fixtureRequests(1), Options{Loader: fixtureLoader(input), LayerValidator: LayerValidatorWithLimits(limits)})
		require.ErrorContains(t, err, "nonnegative")
	}
}

func TestCustomInventoryLimitsStillCountCacheAndCheckCollisions(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "same")
	base.Manifest.Layers = append(base.Manifest.Layers, base.Manifest.Layers[0])
	base.Config.RootFS.DiffIDs = append(base.Config.RootFS.DiffIDs, base.Config.RootFS.DiffIDs[0])
	for _, limits := range []InventoryLimits{{Layers: 1}, {Entries: 1}, {Components: 1}} {
		_, err := Assemble(t.Context(), fixtureRequests(1), Options{Loader: fixtureLoader(base), LayerValidator: LayerValidatorWithLimits(limits)})
		require.ErrorContains(t, err, "assembly inventory exceeds")
	}
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "same")
	_, err := Assemble(t.Context(), fixtureRequests(2), Options{Loader: fixtureLoader(base, mixin), LayerValidator: LayerValidatorWithLimits(InventoryLimits{Components: 1_000_000})})
	require.Error(t, err, "larger budgets must not bypass collision checks")
}
