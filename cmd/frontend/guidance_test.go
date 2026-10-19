package main

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

const guidanceDescriptor = `schemaVersion: "3"
kind: mixin
args:
  file:
    default: notes-en.md
    buildArg: GUIDANCE_FILE
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./docs/${{ kit.args.file }}
`

const groupedGuidanceDescriptor = `schemaVersion: "3"
kind: mixin
args:
  file:
    default: kit.yaml
    buildArg: GUIDANCE_FILE
capabilities:
  - group:
      optional: true
      capabilities:
        - type: com.docker.sandbox/agent-context@1
          config:
            contentFile: ./docs/${{ kit.args.file }}
`

func TestBuildAllowsReservedBasenamesInGroupedGuidance(t *testing.T) {
	for _, file := range []string{"kit.yaml", "kit.dockerfile"} {
		t.Run(file, func(t *testing.T) {
			c := newGuidanceClient(groupedGuidanceDescriptor, map[string]string{"build-arg:file": file})
			c.files["docs/"+file] = []byte("Guidance body.")
			_, err := Build(t.Context(), c)
			require.ErrorIs(t, err, errGuidanceContentBuild)
			require.Equal(t, []string{"demo.yaml", "demo.dockerfile", "docs/" + file}, c.reads)
		})
	}
}

func TestBuildLoadsExpandedGuidance(t *testing.T) {
	for _, tt := range []struct {
		name string
		file string
		opts map[string]string
	}{
		{name: "default", file: "notes-en.md"},
		{name: "override", file: "notes-fr.md", opts: map[string]string{"build-arg:file": "notes-fr.md"}},
		{name: "nested path", file: "fr/notes.md", opts: map[string]string{"build-arg:file": "fr/notes.md"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newGuidanceClient(guidanceDescriptor, tt.opts)
			c.files["docs/"+tt.file] = []byte("Guidance body.")

			_, err := Build(t.Context(), c)
			require.ErrorIs(t, err, errGuidanceContentBuild,
				"guidance must be read, rewritten, and validated before building content")
			require.Equal(t, []string{"demo.yaml", "demo.dockerfile", "docs/" + tt.file}, c.reads)
		})
	}
}

func TestBuildRejectsExpandedGuidanceDescriptorCollision(t *testing.T) {
	c := newGuidanceClient(guidanceDescriptor, map[string]string{"build-arg:file": "kit.yaml"})

	_, err := Build(t.Context(), c)
	require.ErrorContains(t, err, "contentFile ./docs/kit.yaml stages as kit.yaml")
	require.Equal(t, []string{"demo.yaml", "demo.dockerfile"}, c.reads)
}

func TestBuildRejectsExpandedGuidanceRecipeCollision(t *testing.T) {
	c := newGuidanceClient(guidanceDescriptor, map[string]string{"build-arg:file": "kit.dockerfile"})

	_, err := Build(t.Context(), c)
	require.ErrorContains(t, err, "contentFile ./docs/kit.dockerfile stages as kit.dockerfile")
	require.Equal(t, []string{"demo.yaml", "demo.dockerfile"}, c.reads)
}

func TestBuildReportsMissingExpandedGuidance(t *testing.T) {
	c := newGuidanceClient(guidanceDescriptor, map[string]string{"build-arg:file": "missing.md"})

	_, err := Build(t.Context(), c)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.ErrorContains(t, err, "contentFile ./docs/missing.md")
	require.Equal(t, []string{"demo.yaml", "demo.dockerfile", "docs/missing.md"}, c.reads)
}

func TestBuildLeavesInlineGuidanceOutOfFileLoading(t *testing.T) {
	c := newGuidanceClient(`schemaVersion: "3"
kind: mixin
args:
  language:
    default: en
    buildArg: LANGUAGE
  team:
    default: platform
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      content: "Use ${{ kit.args.language }} for ${{ kit.args.team }}."
`, nil)

	_, err := Build(t.Context(), c)
	require.ErrorIs(t, err, errGuidanceContentBuild)
	require.Equal(t, []string{"demo.yaml", "demo.dockerfile"}, c.reads)
}

var errGuidanceContentBuild = errors.New("stop before building image content")

type guidanceClient struct {
	fakeGatewayClient
	files map[string][]byte
	reads []string
}

func newGuidanceClient(descriptor string, opts map[string]string) *guidanceClient {
	c := &guidanceClient{files: map[string][]byte{"demo.yaml": []byte(descriptor)}}
	c.bopts.Opts = map[string]string{keyFilename: "demo.yaml"}
	for k, v := range opts {
		c.bopts.Opts[k] = v
	}
	return c
}

func (c *guidanceClient) Solve(_ context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	for _, data := range req.Definition.Def {
		var op pb.Op
		if err := op.UnmarshalVT(data); err != nil {
			return nil, err
		}
		if src := op.GetSource(); src != nil && (src.Identifier == "local://dockerfile" || src.Identifier == "local://context") {
			res := gwclient.NewResult()
			res.SetRef(guidanceReference{client: c})
			return res, nil
		}
	}
	return nil, errGuidanceContentBuild
}

type guidanceReference struct {
	fakeReference
	client *guidanceClient
}

func (r guidanceReference) ReadFile(_ context.Context, req gwclient.ReadRequest) ([]byte, error) {
	r.client.reads = append(r.client.reads, req.Filename)
	body, ok := r.client.files[req.Filename]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return body, nil
}

func TestBuildLeavesV2InlineGuidanceOutOfFileLoading(t *testing.T) {
	c := newGuidanceClient(`schemaVersion: "3"
kind: mixin
args:
  build: {default: built, buildArg: BUILD}
  team: {default: platform}
capabilities:
- type: com.docker.sandbox/agent-context@2
  config:
    content: 'Build ${{ kit.args.build }} for ${{ kit.args.team }} in ${{ kit.env.HOME }}; $HOME ${HOME}'
`, nil)
	_, err := Build(t.Context(), c)
	require.ErrorIs(t, err, errGuidanceContentBuild)
	require.Equal(t, []string{"demo.yaml", "demo.dockerfile"}, c.reads)
}

func TestBuildRejectsV2ContentFileBeforeReadingIt(t *testing.T) {
	c := newGuidanceClient(`schemaVersion: "3"
kind: mixin
capabilities:
- type: com.docker.sandbox/agent-context@2
  config: {contentFile: ./forbidden.md}
`, nil)
	_, err := Build(t.Context(), c)
	require.ErrorContains(t, err, `unknown field "contentFile"`)
	require.NotContains(t, c.reads, "forbidden.md")
}
