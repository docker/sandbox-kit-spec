package main

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/frontend/dockerui"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	gwpb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	fstypes "github.com/tonistiigi/fsutil/types"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// dockerfileFrontendOpts forwarded only target/label/build-arg options, so
// --no-cache, --pull, cache imports, network mode, and named contexts were
// silently dropped on the way into the companion's dockerfile.v0 sub-solve.
// The forwarding is a denylist now: everything passes except filename and
// platform, which this frontend sets itself, and the keys screened by
// reservedSubSolveOpt — that function's doc comment is the authoritative
// list and the reasons; the cases here exercise each screened category.
func TestDockerfileFrontendOptsForwarding(t *testing.T) {
	amd64 := ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	def := "def"

	for _, tt := range []struct {
		name string
		d    *spec.Descriptor
		opts map[string]string
		plat *ocispecs.Platform
		want map[string]string
	}{
		{
			name: "no-cache for all stages survives with its empty value",
			d:    &spec.Descriptor{},
			opts: map[string]string{"no-cache": ""},
			want: map[string]string{"no-cache": "", "filename": "kit.build.dockerfile"},
		},
		{
			name: "cache, network, resolve-mode, hosts, and named contexts pass through",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"no-cache":           "install,test",
				"cache-imports":      `[{"Type":"registry","Attrs":{"ref":"example.com/cache"}}]`,
				"image-resolve-mode": "pull",
				"force-network-mode": "none",
				"add-hosts":          "example.com=203.0.113.7",
				"context:deps":       "docker-image://alpine:3.20",
			},
			want: map[string]string{
				"no-cache":           "install,test",
				"cache-imports":      `[{"Type":"registry","Attrs":{"ref":"example.com/cache"}}]`,
				"image-resolve-mode": "pull",
				"force-network-mode": "none",
				"add-hosts":          "example.com=203.0.113.7",
				"context:deps":       "docker-image://alpine:3.20",
				"filename":           "kit.build.dockerfile",
			},
		},
		{
			name: "previously allowlisted keys keep passing",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"target":         "runtime",
				"label:org.team": "kits",
				"build-arg:FOO":  "bar",
			},
			want: map[string]string{
				"target":         "runtime",
				"label:org.team": "kits",
				"build-arg:FOO":  "bar",
				"filename":       "kit.build.dockerfile",
			},
		},
		{
			name: "filename and platform are this frontend's, not the caller's",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"filename": "kit.yaml",
				"platform": "linux/amd64,linux/arm64",
			},
			plat: &amd64,
			want: map[string]string{
				"filename": "kit.build.dockerfile",
				"platform": "linux/amd64",
			},
		},
		{
			name: "BUILDKIT_SYNTAX never re-dispatches the companion into this frontend",
			d:    &spec.Descriptor{},
			opts: map[string]string{"build-arg:BUILDKIT_SYNTAX": "docker/sandbox-kit"},
			want: map[string]string{"filename": "kit.build.dockerfile"},
		},
		{
			name: "multi-platform and attestation requests stay out of the sub-solve",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"multi-platform":                    "true",
				"build-arg:BUILDKIT_MULTI_PLATFORM": "true",
				"attest:sbom":                       "",
				"attest:provenance":                 "mode=max",
				"build-arg:BUILDKIT_ATTEST_SBOM":    "",
			},
			want: map[string]string{"filename": "kit.build.dockerfile"},
		},
		{
			name: "gateway dispatch attributes never suppress the companion's own syntax line",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"cmdline":       "docker/sandbox-kit:latest",
				"source":        "docker/sandbox-kit:latest",
				"frontend.caps": "moby.buildkit.frontend.inputs",
			},
			want: map[string]string{"filename": "kit.build.dockerfile"},
		},
		{
			name: "dockerfile.v0 control attributes stay out: subrequests and dockerfile-local renames",
			d:    &spec.Descriptor{},
			opts: map[string]string{
				"requestid":     "frontend.outline",
				"dockerfilekey": "dockerfile2",
			},
			want: map[string]string{"filename": "kit.build.dockerfile"},
		},
		{
			name: "kit args cannot smuggle reserved keys back in as buildArg destinations",
			d: &spec.Descriptor{Args: map[string]spec.Arg{
				"syntax":  {BuildArg: "BUILDKIT_SYNTAX", Default: &def},
				"multi":   {BuildArg: "BUILDKIT_MULTI_PLATFORM", Default: &def},
				"sbom":    {BuildArg: "BUILDKIT_ATTEST_SBOM", Default: &def},
				"version": {BuildArg: "APP_VERSION", Default: &def},
			}},
			opts: map[string]string{"build-arg:syntax": "docker/sandbox-kit"},
			want: map[string]string{
				"build-arg:syntax":      "docker/sandbox-kit",
				"build-arg:APP_VERSION": "def",
				"filename":              "kit.build.dockerfile",
			},
		},
		{
			name: "kit args map onto their declared build-arg names",
			d: &spec.Descriptor{Args: map[string]spec.Arg{
				"version": {BuildArg: "APP_VERSION"},
				"channel": {BuildArg: "APP_CHANNEL", Default: &def},
			}},
			opts: map[string]string{"build-arg:version": "1.2.3"},
			want: map[string]string{
				"build-arg:version":     "1.2.3",
				"build-arg:APP_VERSION": "1.2.3",
				"build-arg:APP_CHANNEL": "def",
				"filename":              "kit.build.dockerfile",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := dockerfileFrontendOpts(tt.d, tt.opts, "kit.build.dockerfile", tt.plat)
			require.Equal(t, tt.want, got)
		})
	}
}

// fakeGatewayClient records the sub-solve request solveDockerfile builds.
// Only the methods that path touches are functional.
type fakeGatewayClient struct {
	bopts        gwclient.BuildOpts
	inputs       map[string]llb.State
	inputsCalled bool
	lastReq      gwclient.SolveRequest
}

func (f *fakeGatewayClient) Solve(_ context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	f.lastReq = req
	res := gwclient.NewResult()
	res.SetRef(fakeReference{})
	return res, nil
}

// fakeReference satisfies solve-result reads with an empty state.
type fakeReference struct{}

func (fakeReference) ToState() (llb.State, error)    { return llb.Scratch(), nil }
func (fakeReference) Evaluate(context.Context) error { return nil }
func (fakeReference) ReadFile(context.Context, gwclient.ReadRequest) ([]byte, error) {
	return nil, nil
}
func (fakeReference) StatFile(context.Context, gwclient.StatRequest) (*fstypes.Stat, error) {
	return nil, nil
}
func (fakeReference) ReadDir(context.Context, gwclient.ReadDirRequest) ([]*fstypes.Stat, error) {
	return nil, nil
}

func (f *fakeGatewayClient) BuildOpts() gwclient.BuildOpts { return f.bopts }

func (f *fakeGatewayClient) Inputs(context.Context) (map[string]llb.State, error) {
	f.inputsCalled = true
	return f.inputs, nil
}

func (f *fakeGatewayClient) ResolveImageConfig(context.Context, string, sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	return "", "", nil, nil
}

func (f *fakeGatewayClient) ResolveSourceMetadata(context.Context, *pb.SourceOp, sourceresolver.Opt) (*sourceresolver.MetaResponse, error) {
	return nil, nil
}

func (f *fakeGatewayClient) NewContainer(context.Context, gwclient.NewContainerRequest) (gwclient.Container, error) {
	return nil, nil
}

func (f *fakeGatewayClient) Warn(context.Context, digest.Digest, string, gwclient.WarnOpts) error {
	return nil
}

// The parent's frontend inputs (bake wiring one target's output into
// another) reach the companion sub-solve only if solveDockerfile hands
// them on. Covered here: named inputs are forwarded, the parent's
// dockerfile input carries the tree containing descriptor and companion,
// inline recipes claim the dockerfile input slot with their synthesized
// content, and a bridge without CapFrontendInputs is never asked for
// inputs at all — the Inputs call itself errors on such bridges even when
// no inputs exist.
func TestSolveDockerfileForwardsFrontendInputs(t *testing.T) {
	ctx := context.Background()
	withInputsCap := gwpb.Caps.CapSet(gwpb.Caps.All())

	parentDockerfile := llb.Scratch().File(llb.Mkfile("kit.yaml", 0o644, []byte("name: demo")))
	parentDockerfileDef, err := parentDockerfile.Marshal(ctx)
	require.NoError(t, err)

	fileRecipe := &companionSource{name: "kit.build.dockerfile", bytes: []byte("FROM scratch\n")}
	inlineRecipe := &companionSource{name: "kit.build.dockerfile", bytes: []byte("FROM scratch\n"), inline: true}

	t.Run("named inputs and the parent recipe tree are forwarded", func(t *testing.T) {
		c := &fakeGatewayClient{
			bopts: gwclient.BuildOpts{Caps: withInputsCap},
			inputs: map[string]llb.State{
				"deps":                              llb.Scratch(),
				dockerui.DefaultLocalNameDockerfile: parentDockerfile,
			},
		}
		_, err := solveDockerfile(ctx, c, &spec.Descriptor{}, map[string]string{}, fileRecipe, nil, "")
		require.NoError(t, err)
		require.True(t, c.inputsCalled)
		require.Contains(t, c.lastReq.FrontendInputs, "deps")
		require.Contains(t, c.lastReq.FrontendInputs, dockerui.DefaultLocalNameDockerfile)
		require.Equal(t, parentDockerfileDef.ToPB().Def,
			c.lastReq.FrontendInputs[dockerui.DefaultLocalNameDockerfile].Def,
			"a file companion must remain readable from the parent source tree")
	})

	t.Run("an inline recipe claims the dockerfile input slot", func(t *testing.T) {
		c := &fakeGatewayClient{
			bopts: gwclient.BuildOpts{Caps: withInputsCap},
			inputs: map[string]llb.State{
				"deps":                              llb.Scratch(),
				dockerui.DefaultLocalNameDockerfile: parentDockerfile,
			},
		}
		_, err := solveDockerfile(ctx, c, &spec.Descriptor{}, map[string]string{}, inlineRecipe, nil, "")
		require.NoError(t, err)
		require.Contains(t, c.lastReq.FrontendInputs, "deps")
		require.Contains(t, c.lastReq.FrontendInputs, dockerui.DefaultLocalNameDockerfile)
		require.NotEqual(t, parentDockerfileDef.ToPB().Def,
			c.lastReq.FrontendInputs[dockerui.DefaultLocalNameDockerfile].Def,
			"the forwarded dockerfile input has to be the synthesized inline recipe, never the parent's")
	})

	t.Run("a bridge without the inputs capability is never asked for inputs", func(t *testing.T) {
		c := &fakeGatewayClient{
			// A bridge that advertises no capabilities, like one
			// predating CapFrontendInputs.
			bopts:  gwclient.BuildOpts{Caps: gwpb.Caps.CapSet(nil)},
			inputs: map[string]llb.State{"deps": llb.Scratch()},
		}
		_, err := solveDockerfile(ctx, c, &spec.Descriptor{}, map[string]string{}, fileRecipe, nil, "")
		require.NoError(t, err)
		require.False(t, c.inputsCalled)
		require.Empty(t, c.lastReq.FrontendInputs)
	})
}
