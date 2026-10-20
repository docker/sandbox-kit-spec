package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func inlineContextDescriptor(kind, body string) *spec.Descriptor {
	return &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind, Capabilities: []spec.Capability{
		{Type: spec.CapabilityAgentContextV2, Config: map[string]any{"content": body}},
	}}
}

func TestResolveAgentContextV2WithoutLayers(t *testing.T) {
	reg := newRegistry(t)
	base := inlineContextDescriptor(spec.KindWorkload, "Base guidance.")
	base.Requires = []string{"tool"}
	base.Capabilities[0].Config["filename"] = "AGENTS.md"
	tool := inlineContextDescriptor(spec.KindMixin, "Tool guidance.")
	tool.Provides = []string{"tool@1.0.0"}
	reg.tag("kits/base", "1.0.0", reg.image(t, kitJSON(t, base)))
	reg.tag("kits/tool", "1.0.0", reg.image(t, kitJSON(t, tool)))
	client, err := New()
	require.NoError(t, err)
	result, err := client.Resolve(t.Context(), reqs(reg.ref("kits/base", "1.0.0"), reg.ref("kits/tool", "1.0.0")))
	require.NoError(t, err)
	contexts, err := spec.AgentContextsV2Of(result.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []spec.AgentContextV2{{Filename: "AGENTS.md", Content: "Tool guidance.\n\nBase guidance."}}, contexts)
	require.Zero(t, reg.blobReads, "all guidance is available from metadata")
}

func TestAssembleAgentContextV2WithoutReadingLayers(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	tool, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
	d := inlineContextDescriptor(spec.KindWorkload, "Base: ${{ kit.env.HOME }}; $HOME ${HOME}")
	d.Requires = []string{"tool"}
	d.Capabilities[0].Config["filename"] = "AGENTS.md"
	base.Descriptor = kitJSON(t, d)
	base.Config.Config.Env = []string{"HOME=/home/final"}
	d = inlineContextDescriptor(spec.KindMixin, "Tool: ${{ kit.args.team }}")
	d.Provides = []string{"tool@1.0.0"}
	d.Args = map[string]spec.Arg{"team": {Required: true}}
	tool.Descriptor = kitJSON(t, d)
	reads := 0
	for _, input := range []*LoadedKit{base, tool} {
		input.LayerLoader = func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
			reads++
			return nil, errors.New("guidance must not need layer access")
		}
	}
	requests := fixtureRequests(2)
	requests[1].Args = map[string]string{"team": "platform"}
	result, err := Assemble(t.Context(), requests, Options{Loader: fixtureLoader(base, tool)})
	require.NoError(t, err)
	require.Zero(t, reads)
	contexts, err := spec.AgentContextsV2Of(result.Resolved.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []spec.AgentContextV2{{Filename: "AGENTS.md", Content: "Tool: platform\n\nBase: /home/final; $HOME ${HOME}"}}, contexts)
}

func TestResolveAgentContextV2ExpandedBudgetDiagnostics(t *testing.T) {
	for _, phase := range []string{"args", "environment", "composition", "args-and-environment"} {
		t.Run(phase, func(t *testing.T) {
			reg := newRegistry(t)
			d := inlineContextDescriptor(spec.KindWorkload, "Guidance: ${{ kit.args.body }}")
			d.Args = map[string]spec.Arg{"body": {Required: true}}
			requests := []Request{}
			option := WithEnvironment(nil, nil)
			hasArgs := phase == "args" || phase == "args-and-environment"
			if !hasArgs {
				d.Args = nil
				d.Capabilities[0].Config["content"] = "Guidance: ${{ kit.env.BODY }}"
			}
			if phase == "args-and-environment" {
				d.Capabilities[0].Config["content"] = "Guidance: ${{ kit.args.body }} ${{ kit.env.BODY }}"
				option = WithEnvironment(map[string]string{"BODY": "suffix"}, nil)
			}
			count := 1
			size := spec.SizeErrorBytes + 100
			if phase == "composition" {
				count = 2
				size = spec.SizeErrorBytes/2 + 100
			}
			if !hasArgs {
				option = WithEnvironment(map[string]string{"BODY": strings.Repeat("private-guidance", size/16+100)}, nil)
			}
			for i := range count {
				if i > 0 {
					d.Kind = spec.KindMixin
				}
				name := fmt.Sprintf("kits/context%d", i)
				reg.tag(name, "1.0.0", reg.image(t, kitJSON(t, d)))
				request := Request{Reference: reg.ref(name, "1.0.0")}
				if hasArgs {
					request.Args = map[string]string{"body": strings.Repeat("x", size)}
					if phase == "args-and-environment" {
						request.Args["body"] = "private-guidance" + request.Args["body"]
					}
				}
				requests = append(requests, request)
			}
			client, err := New()
			require.NoError(t, err)
			result, err := client.Resolve(t.Context(), requests, option)
			require.Nil(t, result)
			require.Error(t, err)
			require.ErrorIs(t, err, spec.ErrDescriptorTooLarge)
			require.NotContains(t, err.Error(), "private-guidance")
		})
	}
}
