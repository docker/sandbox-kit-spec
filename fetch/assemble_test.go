package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func assemblyFixture(t *testing.T, kind, name, file string) (*LoadedKit, []byte) {
	t.Helper()
	var plain bytes.Buffer
	tw := tar.NewWriter(&plain)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "usr/local/bin/", Typeflag: tar.TypeDir, Mode: 0755}))
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: file, Mode: 0755, Size: int64(len(name))}))
	_, err := tw.Write([]byte(name))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err = gz.Write(plain.Bytes())
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	blob := compressed.Bytes()
	descriptor := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind, Provides: []string{name + "@1.0.0"}}
	config := ocispec.Image{Platform: defaultPlatform(), RootFS: ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromBytes(plain.Bytes())}}}
	configRaw, err := json.Marshal(config)
	require.NoError(t, err)
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest,
		Config:      ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(configRaw), Size: int64(len(configRaw))},
		Layers:      []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(blob), Size: int64(len(blob))}},
		Annotations: map[string]string{spec.AnnotationDescriptor: string(kitJSON(t, descriptor))},
	}
	return &LoadedKit{Digest: digest.FromString(name), Manifest: manifest, Config: config,
		LayerLoader: func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(blob)), nil
		},
	}, blob
}

func fixtureLoader(inputs ...*LoadedKit) KitLoader {
	return func(_ context.Context, ref string) (*LoadedKit, error) {
		for i, input := range inputs {
			if ref == fmt.Sprintf("example.com/kit%d:1.0.0", i) {
				return input, nil
			}
		}
		return nil, fmt.Errorf("unexpected reference %s", ref)
	}
}

func fixtureRequests(count int) []Request {
	requests := make([]Request, count)
	for i := range requests {
		requests[i].Reference = fmt.Sprintf("example.com/kit%d:1.0.0", i)
	}
	return requests
}

func TestAssembleGroupsEnvironmentAndImage(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "usr/local/bin/base")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "usr/local/bin/tool")
	base.Config.Config.Env = []string{"HOME=/home/agent", "TEAM=image", "KEEP=image"}
	base.Config.Config.WorkingDir = "/image-workspace"
	base.Config.Config.Entrypoint = []string{"/bin/agent"}
	mixin.Config.Config.Env = []string{"TOOL_DIR=/opt/tool"}
	team := "arg-default"
	tool := "tool-default"
	base.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Requires: []string{"tool"}, Args: map[string]spec.Arg{"team": {Default: &team, Env: "TEAM"}},
	})
	mixin.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, DisplayName: "Tool Kit", Provides: []string{"tool@1.0.0"},
		Args: map[string]spec.Arg{"tool": {Default: &tool, Env: "TOOL"}},
		Capabilities: []spec.Capability{{Group: &spec.CapabilityGroup{Name: "cache", Optional: true, Capabilities: []spec.Capability{
			{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/cache"}},
			{Type: spec.CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{"path": "/cache/config", "content": "yes"}}}},
		}}}},
	})
	workdir := "/workspace"
	overrides := map[string]string{"TEAM": "caller", "EMPTY": ""}
	var events []Progress
	var decisions []string
	ctx := t.Context()
	result, err := Assemble(ctx, fixtureRequests(2), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin), Overrides: Overrides{Env: overrides, WorkingDir: &workdir},
		CapabilitySelector: func(callCtx context.Context, kit spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
			require.Same(t, ctx, callCtx)
			require.Equal(t, "Tool Kit", kit.DisplayName)
			require.Len(t, kit.Capabilities[0].Group.Capabilities, 2)
			decisions = append(decisions, c.Type)
			if c.Type == spec.CapabilityVolume {
				return spec.CapabilityDecision{Message: "storage unavailable"}
			}
			return spec.CapabilityDecision{Accepted: true}
		},
		OnProgress: func(p Progress) { events = append(events, p) },
	})
	require.NoError(t, err)
	require.Equal(t, []string{spec.CapabilityVolume, spec.CapabilityLifecycle}, decisions)
	require.Empty(t, result.Resolved.Descriptor.Capabilities)
	require.Equal(t, fixtureRequests(2)[1].Reference, result.Resolved.Kits[0].Reference)
	require.Len(t, result.Resolved.Selections[0].Selection.Skipped, 1)
	require.Equal(t, []spec.CapabilityDecision{{Message: "storage unavailable"}, {Accepted: true}}, result.Resolved.Selections[0].Selection.Skipped[0].Decisions)
	require.Equal(t, fixtureRequests(2)[1].Reference, result.Resolved.Selections[0].Selection.Skipped[0].MemberSources[0].Kit)
	require.Len(t, result.Image.Layers, 2, "skipping all capabilities does not remove a Kit's layers")
	require.Equal(t, base.Manifest.Layers[0], result.Image.Layers[0], "image order starts with the workload")
	require.Equal(t, map[string]string{"HOME": "/home/agent", "TEAM": "caller", "KEEP": "image", "TOOL_DIR": "/opt/tool", "TOOL": "tool-default", "EMPTY": ""}, result.Environment)
	require.Equal(t, "arg-default", result.Resolved.ContainerEnv["TEAM"])
	require.Equal(t, map[string]string{"TOOL": "tool-default"}, result.Resolved.Kits[0].Env, "skipped capabilities do not remove a Kit's exports")
	require.Equal(t, map[string]string{"TEAM": "arg-default"}, result.Resolved.Kits[1].Env, "runtime overrides do not replace a Kit's exports")
	require.Contains(t, result.Image.Config.Config.Env, "TEAM=image")
	require.Equal(t, "/image-workspace", result.Image.Config.Config.WorkingDir)
	require.Equal(t, "/workspace", result.WorkingDir)
	require.Equal(t, []string{"/bin/agent"}, result.Image.Config.Config.Entrypoint)
	require.Equal(t, []string{"HOME=/home/agent", "TEAM=image", "KEEP=image"}, base.Config.Config.Env)
	overrides["TEAM"] = "mutated"
	require.Equal(t, "caller", result.Environment["TEAM"])
	resolution, err := resolve.Resolve(result.Resolved.Kits)
	require.NoError(t, err)
	require.Empty(t, resolve.LockFrom(resolution).Kits[1].Permissions.StoragePaths)
	manifest, err := result.Image.Manifest()
	require.NoError(t, err)
	config, err := json.Marshal(result.Image.Config)
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(config), manifest.Config.Digest)
	require.Len(t, events, 14)
	for i := 0; i < len(events); i += 2 {
		require.Equal(t, ProgressStarted, events[i].State)
		require.Equal(t, ProgressCompleted, events[i+1].State)
		require.Equal(t, events[i].Stage, events[i+1].Stage)
	}
}

func TestAssembleDefaultSelectionAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability spec.Capability
		message    string
	}{
		{"known", spec.Capability{Type: spec.CapabilityLongRunning}, ""},
		{"unknown required", spec.Capability{Type: "com.example/missing@1"}, "required capability selection rejected"},
		{"unknown optional", spec.Capability{Type: "com.example/missing@1", Optional: true}, ""},
		{"invalid skipped group", spec.Capability{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{{Type: spec.CapabilityPort, Config: map[string]any{"container": 70000}}}}}, "70000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			input.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Capabilities: []spec.Capability{tc.capability}})
			result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(input)})
			if tc.message != "" {
				require.ErrorContains(t, err, tc.message)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Resolved.Selections, 1)
		})
	}
}

func TestAssembleRejectsCollisionsEvenForSkippedGroups(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "usr/local/bin/shared")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "usr/local/bin/shared")
	mixin.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{{Type: "com.example/missing@1", Optional: true}}})
	var events []Progress
	result, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin), OnProgress: func(p Progress) { events = append(events, p) }})
	require.Nil(t, result)
	require.ErrorContains(t, err, "/usr/local/bin/shared")
	require.ErrorContains(t, err, fixtureRequests(2)[0].Reference)
	require.Equal(t, Progress{Stage: StageCollisions, State: ProgressFailed}, events[len(events)-1])
}

type assemblyStream struct {
	io.Reader
	closed   bool
	closeErr error
}

func (r *assemblyStream) Close() error { r.closed = true; return r.closeErr }

func TestAssembleLayerVerificationAndCleanup(t *testing.T) {
	for _, problem := range []string{"digest", "size", "truncated", "close", "cancel", "nil stream", "open"} {
		t.Run(problem, func(t *testing.T) {
			input, blob := assemblyFixture(t, spec.KindWorkload, "base", "base")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &assemblyStream{Reader: bytes.NewReader(blob)}
			switch problem {
			case "digest":
				input.Manifest.Layers[0].Digest = digest.FromString("wrong")
			case "size":
				input.Manifest.Layers[0].Size++
			case "truncated":
				reader.Reader = bytes.NewReader(blob[:len(blob)/2])
			case "close":
				reader.closeErr = errors.New("close failed")
			}
			input.LayerLoader = func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
				if problem == "nil stream" {
					return nil, nil
				}
				if problem == "open" {
					return nil, errors.New("open failed")
				}
				if problem == "cancel" {
					cancel()
				}
				return reader, nil
			}
			result, err := Assemble(ctx, fixtureRequests(1), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(input)})
			require.Error(t, err)
			require.Nil(t, result)
			if problem != "nil stream" && problem != "open" {
				require.True(t, reader.closed)
			}
			if problem == "cancel" {
				require.ErrorIs(t, err, context.Canceled)
			}
			if problem == "digest" || problem == "size" || problem == "close" || problem == "open" {
				require.ErrorContains(t, err, problem)
			}
		})
	}
}

func TestAssembleCancellationFromProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var events []Progress
	_, err := Assemble(ctx, fixtureRequests(1), Options{LayerValidator: DefaultLayerValidator,
		Loader: func(context.Context, string) (*LoadedKit, error) {
			t.Fatal("loader ran after cancellation")
			return nil, nil
		},
		OnProgress: func(p Progress) { events = append(events, p); cancel() },
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []ProgressState{ProgressStarted, ProgressFailed}, []ProgressState{events[0].State, events[1].State})
}

func TestAssembleCachesInventoriesWithoutHidingCollisions(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "shared")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	mixin.Manifest.Layers = base.Manifest.Layers
	mixin.Config.RootFS = base.Config.RootFS
	calls := 0
	open := base.LayerLoader
	base.LayerLoader = func(ctx context.Context, layer ocispec.Descriptor) (io.ReadCloser, error) {
		calls++
		return open(ctx, layer)
	}
	mixin.LayerLoader = func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
		t.Fatal("cached blob opened twice")
		return nil, nil
	}
	_, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin)})
	require.ErrorContains(t, err, "file collisions")
	require.Equal(t, 1, calls)
}

func TestAssembleDefaultRegistryLoader(t *testing.T) {
	for _, mediaType := range []string{ocispec.MediaTypeImageManifest, ""} {
		t.Run("mediaType="+mediaType, func(t *testing.T) {
			reg := newRegistry(t)
			reg.user, reg.pass = "user", "password"
			dockerConfig := t.TempDir()
			config := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, reg.Listener.Addr().String(), base64.StdEncoding.EncodeToString([]byte("user:password")))
			require.NoError(t, os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte(config), 0600))
			t.Setenv("DOCKER_CONFIG", dockerConfig)
			input, blob := assemblyFixture(t, spec.KindWorkload, "base", "base")
			input.Manifest.MediaType = mediaType
			configRaw, err := json.Marshal(input.Config)
			require.NoError(t, err)
			reg.mu.Lock()
			reg.blobs["kit/blobs/"+input.Manifest.Config.Digest.String()] = configRaw
			reg.blobs["kit/blobs/"+input.Manifest.Layers[0].Digest.String()] = blob
			reg.mu.Unlock()
			manifestRaw, err := json.Marshal(input.Manifest)
			require.NoError(t, err)
			reg.tag("kit", "1.0.0", manifestRaw)
			result, err := Assemble(t.Context(), reqs(reg.ref("kit", "1.0.0")), Options{LayerValidator: DefaultLayerValidator})
			require.NoError(t, err)
			require.Equal(t, digest.FromBytes(manifestRaw).String(), result.Resolved.Kits[0].Digest)
			require.Equal(t, 2, reg.blobReads, "config and layer are read")
			metadataOnly, err := Assemble(t.Context(), reqs(reg.ref("kit", "1.0.0")), Options{})
			require.NoError(t, err)
			require.Equal(t, result, metadataOnly)
			require.Equal(t, 3, reg.blobReads, "nil validator reads only the config blob")
		})
	}
}

func TestAssembleRejectsBadLoaderInputsAndOverrides(t *testing.T) {
	relative := "relative"
	for _, tc := range []struct {
		name      string
		mutate    func(*LoadedKit)
		overrides Overrides
	}{
		{name: "rootfs", mutate: func(k *LoadedKit) { k.Config.RootFS.DiffIDs = nil }},
		{name: "missing layer reader", mutate: func(k *LoadedKit) { k.LayerLoader = nil }},
		{name: "invalid digest", mutate: func(k *LoadedKit) { k.Digest = "invalid" }},
		{name: "negative layer size", mutate: func(k *LoadedKit) { k.Manifest.Layers[0].Size = -1 }},
		{name: "relative workdir", overrides: Overrides{WorkingDir: &relative}},
		{name: "bad env key", overrides: Overrides{Env: map[string]string{"A=B": "value"}}},
		{name: "bad env value", overrides: Overrides{Env: map[string]string{"A": "\x00"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			if tc.mutate != nil {
				tc.mutate(input)
			}
			result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(input), Overrides: tc.overrides})
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
}

func TestLoadKitKeepsIndexDescriptorAndPinnedImage(t *testing.T) {
	reg := newRegistry(t)
	input, blob := assemblyFixture(t, spec.KindWorkload, "base", "base")
	descriptor := input.Manifest.Annotations[spec.AnnotationDescriptor]
	input.Manifest.Annotations = nil
	input.Manifest.MediaType = "" // The index descriptor carries the media type.
	configRaw, err := json.Marshal(input.Config)
	require.NoError(t, err)
	reg.mu.Lock()
	reg.blobs["kit/blobs/"+input.Manifest.Config.Digest.String()] = configRaw
	reg.blobs["kit/blobs/"+input.Manifest.Layers[0].Digest.String()] = blob
	reg.mu.Unlock()
	manifestRaw, err := json.Marshal(input.Manifest)
	require.NoError(t, err)
	reg.tag("kit", "platform", manifestRaw)
	platform := input.Config.Platform
	index := reg.index(t, []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromBytes(manifestRaw), Size: int64(len(manifestRaw)), Platform: &platform}}, map[string]string{spec.AnnotationDescriptor: descriptor})
	reg.tag("kit", "1.0.0", index)
	client, err := New()
	require.NoError(t, err)
	loaded, err := client.LoadKit(t.Context(), reg.ref("kit", "1.0.0"))
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(index), loaded.Digest)
	require.Equal(t, reg.Listener.Addr().String()+"/kit@"+loaded.Digest.String(), loaded.Image)
	require.Equal(t, descriptor, string(loaded.Descriptor))
	require.Empty(t, loaded.Manifest.Annotations)
	require.Empty(t, loaded.Manifest.MediaType)
	require.Equal(t, 1, reg.blobReads, "loading leaves layer blobs unopened")
	// Subsequent tag movement cannot change the retained metadata or layer refs.
	reg.tag("kit", "1.0.0", reg.image(t, nil))
	result, err := Assemble(t.Context(), reqs(reg.ref("kit", "1.0.0")), Options{LayerValidator: DefaultLayerValidator, Loader: func(context.Context, string) (*LoadedKit, error) { return loaded, nil }})
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(index).String(), result.Resolved.Kits[0].Digest)
	require.Equal(t, input.Manifest.Layers, result.Image.Layers)
	require.Equal(t, 2, reg.blobReads)
}

func TestAssembleRejectsCrossPlatformImages(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	mixin.Config.Architecture = "other-architecture"
	_, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin)})
	require.ErrorContains(t, err, "other-architecture")
}

func TestAssembleValidatesExpandedGroupBeforeSelector(t *testing.T) {
	input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	input.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Args: map[string]spec.Arg{"port": {Required: true}},
		Capabilities: []spec.Capability{{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
			{Type: spec.CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}},
		}}}},
	})
	requests := fixtureRequests(1)
	requests[0].Args = map[string]string{"port": "70000"}
	_, err := Assemble(t.Context(), requests, Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(input), CapabilitySelector: func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
		t.Fatal("invalid expanded declaration reached selector")
		return spec.CapabilityDecision{Accepted: false}
	}})
	require.ErrorContains(t, err, "70000")
	require.ErrorContains(t, err, "group.capabilities[0]")
	require.ErrorContains(t, err, requests[0].Reference)
}

func TestAssembleRejectsWrongDiffIDEvenForCachedBlob(t *testing.T) {
	input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	// The same compressed blob appears twice. A verified first occurrence
	// cannot stand in for checking the second occurrence's claimed diff ID.
	input.Manifest.Layers = append(input.Manifest.Layers, input.Manifest.Layers[0])
	input.Config.RootFS.DiffIDs = append(input.Config.RootFS.DiffIDs, digest.FromString("wrong"))
	result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(input)})
	require.Nil(t, result)
	require.ErrorContains(t, err, "diff ID mismatch")
}

func TestAssembleExpandsFinalEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, exported, override, want string
	}{
		{"image default", "", "", "/home/image"},
		{"argument export", "/home/argument", "", "/home/argument"},
		{"runtime override", "/home/argument", "/home/runtime", "/home/runtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
			base.Config.Config.Env = []string{"HOME=/home/image"}
			bd := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload}
			if tc.exported != "" {
				bd.Args = map[string]spec.Arg{"home": {Default: &tc.exported, Env: "HOME"}}
			}
			base.Descriptor = kitJSON(t, bd)
			mixin.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
				Capabilities: []spec.Capability{{Group: &spec.CapabilityGroup{Capabilities: []spec.Capability{
					{Type: spec.CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{
						"path": "${{ kit.env.HOME }}/config", "content": "$HOME ${HOME} ~/",
					}}}},
				}}}},
			})
			original := append([]byte(nil), mixin.Descriptor...)
			overrides := map[string]string{}
			if tc.override != "" {
				overrides["HOME"] = tc.override
			}
			calls := 0
			result, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin), Overrides: Overrides{Env: overrides},
				CapabilitySelector: func(_ context.Context, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
					calls++
					lc, err := spec.LifecycleOf([]spec.Capability{c})
					require.NoError(t, err)
					require.Equal(t, tc.want+"/config", lc.Files[0].Path)
					overrides["HOME"] = "/mutated-by-selector"
					return spec.CapabilityDecision{Accepted: true}
				},
			})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, tc.want, result.Environment["HOME"])
			lc, err := spec.LifecycleOf(result.Resolved.Descriptor.Capabilities)
			require.NoError(t, err)
			require.Equal(t, tc.want+"/config", lc.Files[0].Path)
			require.Equal(t, "$HOME ${HOME} ~/", lc.Files[0].Content)
			require.Equal(t, original, mixin.Descriptor)
			require.Equal(t, []string{"HOME=/home/image"}, base.Config.Config.Env)
		})
	}
}

func TestAssembleEnvironmentErrorsBeforeSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		config map[string]any
	}{
		{"missing", nil, map[string]any{"path": "${{kit.env.HOME}}/cache"}},
		{"relative path", map[string]string{"HOME": "private-relative-value"}, map[string]any{"path": "${{kit.env.HOME}}/cache"}},
		{"string boolean", map[string]string{"HOME": "/home/user", "FLAG": "true"}, map[string]any{"path": "${{kit.env.HOME}}/cache", "tmpfs": "${{kit.env.FLAG}}"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			base.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
				Capabilities: []spec.Capability{{Type: spec.CapabilityVolume, Optional: true, Config: tc.config}},
			})
			result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base), Overrides: Overrides{Env: tc.env},
				CapabilitySelector: func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
					t.Fatal("selector called before validation")
					return spec.CapabilityDecision{Accepted: false}
				},
			})
			require.Error(t, err)
			require.Nil(t, result)
			require.NotContains(t, err.Error(), "private-relative-value")
		})
	}
}

func TestAssembleEnvironmentExpansionDetectsFileConflict(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	for i, kit := range []*LoadedKit{base, mixin} {
		kind := spec.KindMixin
		if i == 0 {
			kind = spec.KindWorkload
		}
		kit.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind,
			Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{"path": "${{kit.env.HOME}}/config", "content": "data"}}}}},
		})
	}
	_, err := Assemble(t.Context(), fixtureRequests(2), Options{LayerValidator: DefaultLayerValidator, Loader: fixtureLoader(base, mixin), Overrides: Overrides{Env: map[string]string{"HOME": "/private-value"}}})
	require.ErrorContains(t, err, "incompatible declarations")
	require.NotContains(t, err.Error(), "/private-value")
}
