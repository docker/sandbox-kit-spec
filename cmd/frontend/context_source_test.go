package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/moby/buildkit/client/llb"
	"strings"
	"testing"

	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	gwpb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

// GitHub's trusted builder sends a Git URL and no local file session.
// Reject local sources here so resolving a descriptor through llb.Local
// cannot accidentally pass against a fake that accepts every source.
func TestReadDescriptorSourcesGitContext(t *testing.T) {
	c := &remoteReadClient{guidanceClient: newGuidanceClient("schemaVersion: \"3\"\nkind: mixin\n", nil)}
	c.bopts.Opts["context"] = "https://github.com/example/kits.git?checksum=0123456789012345678901234567890123456789"
	c.bopts.Caps = gwpb.Caps.CapSet(gwpb.Caps.All())
	c.bopts.LLBCaps = pb.Caps.CapSet(pb.Caps.All())
	c.files["demo.dockerfile"] = []byte("FROM scratch\nCOPY payload /payload\n")
	got, err := readDescriptorSources(t.Context(), c, "demo.yaml", "demo.dockerfile")
	require.NoError(t, err)
	require.Equal(t, c.files["demo.yaml"], got.descriptor)
	require.Equal(t, c.files["demo.dockerfile"], got.companion)
}

type remoteReadClient struct {
	*guidanceClient
}

func (c *remoteReadClient) Solve(_ context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	for _, data := range req.Definition.Def {
		var op pb.Op
		if err := op.UnmarshalVT(data); err != nil {
			return nil, err
		}
		if src := op.GetSource(); src != nil {
			if strings.HasPrefix(src.Identifier, "local://") {
				return nil, fmt.Errorf("no local sources enabled")
			}
			if strings.HasPrefix(src.Identifier, "git://") {
				res := gwclient.NewResult()
				res.SetRef(guidanceReference{client: c.guidanceClient})
				return res, nil
			}
		}
	}
	return nil, fmt.Errorf("expected a Git source")
}

// Keep a compile-time assertion for the fake's input boundary.
var _ gwclient.Client = (*remoteReadClient)(nil)

func TestReadRecipeAndGuidanceFromGitContext(t *testing.T) {
	c := &remoteReadClient{guidanceClient: newGuidanceClient("schemaVersion: \"3\"\nkind: mixin\n", nil)}
	c.bopts.Opts["context"] = "https://github.com/example/kits.git?subdir=kit"
	c.files["nested/recipes/custom.dockerfile"] = []byte("FROM scratch\nCOPY payload /payload\n")
	c.files["docs/guidance.md"] = []byte("Use the committed instructions.\n")
	recipe, err := readDockerfileFile(t.Context(), c, "nested/recipes/custom.dockerfile")
	require.NoError(t, err)
	require.Equal(t, c.files["nested/recipes/custom.dockerfile"], recipe)
	guidance, err := readContextFile(t.Context(), c, "docs/guidance.md")
	require.NoError(t, err)
	require.Equal(t, c.files["docs/guidance.md"], guidance)
	require.Equal(t, "demo.yaml", c.bopts.Opts[keyFilename], "recipe reads must not rename the shared descriptor options")
}

func TestInlineRecipePreservesGitContext(t *testing.T) {
	c := &fakeGatewayClient{bopts: gwclient.BuildOpts{
		Opts:    map[string]string{"context": "https://github.com/example/kits.git?subdir=kit", "contextsubdir": "nested"},
		Caps:    gwpb.Caps.CapSet(gwpb.Caps.All()),
		LLBCaps: pb.Caps.CapSet(pb.Caps.All()),
	}}
	_, err := solveDockerfile(t.Context(), c, &spec.Descriptor{}, c.bopts.Opts,
		&companionSource{name: "demo.build.dockerfile", bytes: []byte("FROM scratch\nCOPY payload /payload\n"), inline: true}, nil, "")
	require.NoError(t, err)
	require.NotContains(t, c.lastReq.FrontendOpt, "context")
	require.NotContains(t, c.lastReq.FrontendOpt, "contextsubdir")
	require.Contains(t, c.lastReq.FrontendInputs, "dockerfile")
	require.Contains(t, c.lastReq.FrontendInputs, "context")
	var gitSource bool
	for _, data := range c.lastReq.FrontendInputs["context"].Def {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(data))
		if source := op.GetSource(); source != nil {
			require.NotContains(t, source.Identifier, "local://")
			gitSource = gitSource || strings.HasPrefix(source.Identifier, "git://")
		}
	}
	require.True(t, gitSource, "COPY must still read the committed Git source after replacing the recipe")
	require.Contains(t, c.bopts.Opts, "context", "normalizing a nested solve must not modify the parent options")
}

// Input-only callers such as Bake can hand the two source trees in
// separately. The same filename in each tree makes crossing that boundary
// visible instead of letting a fake global filesystem hide the mistake.
func TestReadSourcesFromFrontendInputs(t *testing.T) {
	ctx := t.Context()
	c := &inputReadClient{guidanceClient: newGuidanceClient("unused", nil)}
	c.inputs = map[string]llb.State{}
	c.filesByDefinition = map[string]map[string][]byte{}
	for name, files := range map[string]map[string][]byte{
		"dockerfile": {"demo.yaml": []byte("schemaVersion: \"3\"\nkind: mixin\n"), "demo.dockerfile": []byte("FROM scratch\n"), "note.md": []byte("wrong source")},
		"context":    {"note.md": []byte("context guidance")},
	} {
		state := llb.Scratch().File(llb.Mkfile(name, 0o644, []byte(name)))
		def, err := state.Marshal(ctx)
		require.NoError(t, err)
		c.inputs[name] = state
		c.filesByDefinition[definitionKey(def.ToPB())] = files
	}
	got, err := readDescriptorSources(ctx, c, "demo.yaml", "demo.dockerfile")
	require.NoError(t, err)
	require.Contains(t, string(got.descriptor), "schemaVersion")
	require.Equal(t, "FROM scratch\n", string(got.companion))
	gotRecipe, err := readDockerfileFile(ctx, c, "demo.dockerfile")
	require.NoError(t, err)
	require.Equal(t, got.companion, gotRecipe)
	guidance, err := readContextFile(ctx, c, "note.md")
	require.NoError(t, err)
	require.Equal(t, "context guidance", string(guidance))
}

type inputReadClient struct {
	*guidanceClient
	filesByDefinition map[string]map[string][]byte
}

func (c *inputReadClient) Solve(_ context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	files, ok := c.filesByDefinition[definitionKey(req.Definition)]
	if !ok {
		return nil, fmt.Errorf("unexpected source, no local sources enabled")
	}
	res := gwclient.NewResult()
	res.SetRef(guidanceReference{client: &guidanceClient{files: files}})
	return res, nil
}

func definitionKey(def *pb.Definition) string {
	return string(bytes.Join(def.Def, nil))
}
