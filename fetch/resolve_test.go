package fetch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// The inputs are each valid; only their combined content crosses a budget.
func sizedComposition(t *testing.T, partial bool, size int) (*Client, []Request) {
	t.Helper()
	reg := newRegistry(t)
	var requests []Request
	for i := range 2 {
		kind := spec.KindMixin
		if i == 0 && !partial {
			kind = spec.KindWorkload
		}
		d := &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: kind,
			Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
				"files": []any{map[string]any{"path": fmt.Sprintf("/etc/kit-%d", i), "content": strings.Repeat("x", size)}},
			}}},
		}
		raw := kitJSON(t, d)
		_, err := spec.ValidatePublished(raw, d)
		require.NoError(t, err)
		name := fmt.Sprintf("kits/kit-%d", i)
		reg.tag(name, "1.0.0", reg.image(t, raw))
		requests = append(requests, Request{Reference: reg.ref(name, "1.0.0")})
	}
	client, err := New()
	require.NoError(t, err)
	return client, requests
}

func TestResolveAlwaysValidatesMergedDescriptor(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			client, requests := sizedComposition(t, partial, spec.SizeErrorBytes/2+1024)
			resolve := client.Resolve
			if partial {
				resolve = client.ResolvePartial
			}

			result, err := resolve(t.Context(), requests)
			require.Nil(t, result)
			require.ErrorContains(t, err, "merged descriptor:1:1:")
			require.ErrorContains(t, err, "over the 524288 byte budget")
			var all spec.ValidationErrors
			require.ErrorAs(t, err, &all)
			require.Len(t, all, 1)

		})
	}
}

func TestResolveReturnsFinalValidationWarnings(t *testing.T) {
	client, requests := sizedComposition(t, false, spec.SizeWarnBytes/2+1024)
	result, err := client.Resolve(t.Context(), requests)
	require.NoError(t, err)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "the advisory budget is 65536")
}

func TestResolvePreservesExpandedInputsAndIdentity(t *testing.T) {
	var kits []*Kit
	for i, name := range []string{"workload", "base"} {
		defaultMessage := "default " + name
		d := &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Version: "1.0.0",
			Provides: []string{name},
			Args:     map[string]spec.Arg{"message": {Default: &defaultMessage, Env: fmt.Sprintf("MESSAGE_%d", i)}},
			Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Name: name + " setup", Config: map[string]any{
				"files": []any{map[string]any{"path": "/etc/" + name, "content": "${{ kit.args.message }}"}},
			}}},
		}
		if i == 0 {
			d.Kind = spec.KindWorkload
			d.Requires = []string{"base"}
		}
		raw := kitJSON(t, d)
		kits = append(kits, &Kit{Reference: "example.com/" + name + ":2.0.0", Digest: digest.FromString(name).String(), Descriptor: d, Raw: raw})
	}
	args := []map[string]string{{"message": "custom workload"}, nil}
	before, err := json.Marshal(kits)
	require.NoError(t, err)
	result, err := mergeKits(t.Context(), kits, args, false)
	require.NoError(t, err)
	require.Len(t, result.Kits, 2)
	for i, inputIndex := range []int{1, 0} {
		kit := result.Kits[i]
		input := kits[inputIndex]
		message := []string{"default base", "custom workload"}[i]
		require.Equal(t, input.Reference, kit.Reference)
		require.Equal(t, input.Digest, kit.Digest)
		require.Equal(t, strings.TrimSuffix(input.Reference, ":2.0.0")+"@"+input.Digest, kit.Image)
		require.Equal(t, "2.0.0", kit.Descriptor.Version)
		require.Equal(t, map[string]string{"message": message}, kit.Args)
		require.Equal(t, map[string]string{fmt.Sprintf("MESSAGE_%d", inputIndex): message}, kit.Env)
		require.Empty(t, kit.Descriptor.Args)
		require.Equal(t, input.Descriptor, result.Selections[i].PublishedDescriptor)
		require.Equal(t, input.Raw, result.Selections[i].PublishedBytes)
		published, err := spec.Decode(result.Selections[i].PublishedBytes)
		require.NoError(t, err)
		require.Equal(t, input.Descriptor.Args, published.Args)
		require.JSONEq(t, string(input.Raw), string(kitJSON(t, result.Selections[i].PublishedDescriptor)))
		require.Equal(t, input.Descriptor.Requires, kit.Descriptor.Requires)
		require.Equal(t, input.Descriptor.Capabilities[0].Name, kit.Descriptor.Capabilities[0].Name)
		lifecycle, err := spec.LifecycleOf(kit.Descriptor.Capabilities)
		require.NoError(t, err)
		require.Len(t, lifecycle.Files, 1)
		require.Equal(t, message, lifecycle.Files[0].Content)
	}
	require.Empty(t, result.Descriptor.Requires, "the merged view drops satisfied relations; individual Kits retain them")
	lifecycle, err := spec.LifecycleOf(result.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Len(t, lifecycle.Files, 2)
	require.Equal(t, map[string]string{"MESSAGE_0": "custom workload", "MESSAGE_1": "default base"}, result.ContainerEnv)
	after, err := json.Marshal(kits)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "assembly leaves the fetched descriptors and raw bytes intact")
	result.Kits[1].Args["message"] = "changed"
	require.Equal(t, "custom workload", args[0]["message"], "resolved values do not alias caller arguments")
	result.Kits[1].Env["MESSAGE_0"] = "changed"
	require.Equal(t, "custom workload", result.ContainerEnv["MESSAGE_0"], "per-Kit exports do not alias the union")
}

func TestResolvePerKitEnvExportRules(t *testing.T) {
	value, empty := "default", ""
	for _, tc := range []struct {
		name  string
		decls map[string]spec.Arg
		args  map[string]string
		env   map[string]string
	}{
		{name: "no args"},
		{name: "private and build args", decls: map[string]spec.Arg{
			"private": {Default: &value}, "build": {Default: &value, BuildArg: "BUILD_VALUE"},
		}},
		{name: "default export", decls: map[string]spec.Arg{"value": {Default: &value, Env: "VALUE"}}, env: map[string]string{"VALUE": value}},
		{name: "supplied export", decls: map[string]spec.Arg{"value": {Default: &value, Env: "VALUE"}}, args: map[string]string{"value": "supplied"}, env: map[string]string{"VALUE": "supplied"}},
		{name: "empty default", decls: map[string]spec.Arg{"value": {Default: &empty, Env: "VALUE"}}, env: map[string]string{"VALUE": ""}},
		{name: "empty supplied value", decls: map[string]spec.Arg{"value": {Required: true, Env: "VALUE"}}, args: map[string]string{"value": ""}, env: map[string]string{"VALUE": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Args: tc.decls}
			raw := kitJSON(t, d)
			kit := &Kit{Reference: "example.com/tool:1.0.0", Digest: digest.FromBytes(raw).String(), Descriptor: d, Raw: raw}
			result, err := mergeKits(t.Context(), []*Kit{kit}, []map[string]string{tc.args}, true,
				WithEnvironment(map[string]string{"IMAGE_DEFAULT": "image"}, map[string]string{"VALUE": "runtime"}))
			require.NoError(t, err)
			require.Len(t, result.Kits, 1)
			require.Equal(t, tc.env, result.Kits[0].Env)
			require.Equal(t, tc.env, result.ContainerEnv)
		})
	}
}

func TestResolvePartialPreservesContextWithoutStaging(t *testing.T) {
	reg := newRegistry(t)
	d := &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
		Capabilities: []spec.Capability{{Type: spec.CapabilityAgentContext, Name: "Tool guidance", Optional: true, Config: map[string]any{"content": "Kit guidance"}}},
	}
	reg.tag("kits/context", "1.0.0", reg.image(t, kitJSON(t, d)))
	client, err := New()
	require.NoError(t, err)
	ref := reg.ref("kits/context", "1.0.0")
	result, err := client.ResolvePartial(t.Context(), reqs(ref))
	require.NoError(t, err)
	guidance, err := spec.AgentContextOf(result.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &spec.AgentContext{}, guidance)
	require.Equal(t, "Tool guidance", result.Descriptor.Capabilities[0].Name)
	require.True(t, result.Descriptor.Capabilities[0].Optional)
	require.Len(t, result.Kits, 1)
	require.Equal(t, ref, result.Kits[0].Reference)
	guidance, err = spec.AgentContextOf(result.Kits[0].Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &spec.AgentContext{Content: "Kit guidance"}, guidance)
}

func TestResolvePreservesPerKitContextInDependencyOrder(t *testing.T) {
	reg := newRegistry(t)
	descriptors := []*spec.Descriptor{
		{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
			Provides: []string{"agent"}, Requires: []string{"tool"},
			Capabilities: []spec.Capability{{Type: spec.CapabilityAgentContext, Config: map[string]any{
				"filename": "AGENTS.md", "contentFile": "/usr/share/sandbox/kit/agent/context.md",
			}}},
		},
		{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
			Provides: []string{"tool"}, Requires: []string{"base"},
			Args: map[string]spec.Arg{"team": {Required: true, Env: "TEAM"}},
			Capabilities: []spec.Capability{{Type: spec.CapabilityAgentContext, Config: map[string]any{
				"content": "Team: ${{ kit.args.team }}",
			}}},
		},
		{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Provides: []string{"base"},
			Capabilities: []spec.Capability{{Type: spec.CapabilityAgentContext, Name: "Base guidance", Optional: true, Config: map[string]any{
				"contentFile": "/usr/share/sandbox/kit/base/context.md",
			}}},
		},
	}
	var requests []Request
	for i, d := range descriptors {
		d.Version = "1.0.0"
		name := fmt.Sprintf("kits/kit-%d", i)
		reg.tag(name, "1.0.0", reg.image(t, kitJSON(t, d)))
		requests = append(requests, Request{Reference: reg.ref(name, "1.0.0")})
	}
	requests[1].Args = map[string]string{"team": "alpha"}
	client, err := New()
	require.NoError(t, err)
	result, err := client.Resolve(t.Context(), requests)
	require.NoError(t, err)
	guidance, err := spec.AgentContextOf(result.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &spec.AgentContext{Filename: "AGENTS.md"}, guidance)
	require.Len(t, result.Kits, 3)
	for i, expected := range []struct {
		reference string
		context   spec.AgentContext
	}{
		{requests[2].Reference, spec.AgentContext{ContentFile: "/usr/share/sandbox/kit/base/context.md"}},
		{requests[1].Reference, spec.AgentContext{Content: "Team: alpha"}},
		{requests[0].Reference, spec.AgentContext{Filename: "AGENTS.md", ContentFile: "/usr/share/sandbox/kit/agent/context.md"}},
	} {
		require.Equal(t, expected.reference, result.Kits[i].Reference)
		guidance, err := spec.AgentContextOf(result.Kits[i].Descriptor.Capabilities)
		require.NoError(t, err)
		require.Equal(t, &expected.context, guidance)
	}
	require.Equal(t, "Base guidance", result.Descriptor.Capabilities[0].Name)
	require.False(t, result.Descriptor.Capabilities[0].Optional)
	require.Equal(t, map[string]string{"TEAM": "alpha"}, result.ContainerEnv)
	require.Zero(t, reg.blobReads, "context paths stay in-image; assembly does not read bodies")
}

func TestResolveChecksPrerequisites(t *testing.T) {
	defaultPort := "99999"
	tests := []struct {
		name        string
		descriptors []*spec.Descriptor
		partial     bool
		message     string
	}{
		{name: "published input", partial: true, message: "published descriptor", descriptors: []*spec.Descriptor{{Kind: spec.KindMixin, IconURL: "http://example.com"}}},
		{name: "expanded input", partial: true, message: "expanded descriptor", descriptors: []*spec.Descriptor{{
			Kind:         spec.KindMixin,
			Args:         map[string]spec.Arg{"port": {Default: &defaultPort}},
			Capabilities: []spec.Capability{{Type: spec.CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}}},
		}}},
		{name: "set coherence", message: "no workload kit", descriptors: []*spec.Descriptor{{Kind: spec.KindMixin}}},
		{name: "merge conflict", partial: true, message: "both declare resources", descriptors: []*spec.Descriptor{
			{Kind: spec.KindMixin, Capabilities: []spec.Capability{{Type: spec.CapabilityResources, Config: map[string]any{"cpu": 1}}}},
			{Kind: spec.KindMixin, Capabilities: []spec.Capability{{Type: spec.CapabilityResources, Config: map[string]any{"cpu": 2}}}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newRegistry(t)
			var requests []Request
			for i, d := range tt.descriptors {
				d.SchemaVersion = spec.SchemaVersion
				name := fmt.Sprintf("kits/kit-%d", i)
				reg.tag(name, "1.0.0", reg.image(t, kitJSON(t, d)))
				requests = append(requests, Request{Reference: reg.ref(name, "1.0.0")})
			}
			client, err := New()
			require.NoError(t, err)
			resolve := client.Resolve
			if tt.partial {
				resolve = client.ResolvePartial
			}
			result, err := resolve(t.Context(), requests)
			require.Nil(t, result)
			require.ErrorContains(t, err, tt.message)
		})
	}
}
