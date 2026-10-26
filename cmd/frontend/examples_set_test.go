package main

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/distribution/reference"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// A set example names published kits, so it cannot be built here the
// way every other example can — its content lives in registries. That
// leaves its coherence unchecked by anything: the grammar test decodes
// the descriptor, and the end-to-end test merges kits it synthesizes
// itself, so a set example could list kits that conflict and nothing
// would say so until someone pushed all of them.
//
// This runs the two steps a build would: resolve the listed kits as a
// set, then merge their declarations. The kits are read from the
// examples directory rather than a registry — the references follow the
// sbx-kit-<example> convention the Taskfile pushes under, so the local
// file is the same descriptor the build would resolve, modulo the tag.
func TestExampleSetsResolveAndMerge(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "examples", "*", "*.yaml"))
	require.NoError(t, err)

	sets := 0
	for _, descriptor := range matches {
		d := publishedForm(t, descriptor)
		if len(d.Kits) == 0 {
			continue
		}
		sets++
		t.Run(filepath.Base(descriptor), func(t *testing.T) {
			var units []*resolve.Unit
			byUnit := map[*resolve.Unit]spec.Contribution{}
			for _, k := range d.Kits {
				ref, err := reference.ParseNormalizedNamed(k.Ref)
				require.NoError(t, err)
				tagged, ok := ref.(reference.Tagged)
				require.True(t, ok, "set examples must pin a tagged kit: %s", k.Ref)
				// A tracked example's current default can move beyond the
				// version the set pins. Expand its version arg from that pin.
				listed := publishedFormWithArgs(t, exampleFor(t, k.Ref), map[string]string{"version": tagged.Tag()})
				// The build resolves each member's create-phase args from
				// the set's kits[].args before its declarations reach the
				// merge, defaults answering whatever the set leaves unsaid
				// (§9.5). The same step here, through the same function,
				// or a member declaring one would leave a `${{ kit.args.* }}`
				// reference in the merged descriptor that nothing declares.
				listed, _, err = kitDeclarations(listed, k, d.Args)
				require.NoError(t, err, "%s: create-phase args of %s do not resolve from the set's kits[].args", filepath.Base(descriptor), k.Ref)
				listed.Version = resolve.EffectiveProvideVersion(k.Ref, listed)
				u := &resolve.Unit{Reference: k.Ref, Descriptor: listed}
				units = append(units, u)
				byUnit[u] = spec.Contribution{Reference: k.Ref, Descriptor: listed}
			}

			// The same judgment buildSet makes: one workload at most,
			// every requirement answered inside the set, one provider
			// per name, one credential owner, providers ordered first.
			resolution, err := resolve.ResolvePartial(units)
			require.NoError(t, err, "the kits this set lists do not compose")

			var contributions []spec.Contribution
			for _, u := range resolution.Ordered() {
				contributions = append(contributions, byUnit[u])
			}
			own := *d
			own.Kits = nil
			own.Kind = spec.KindMixin
			require.NoError(t, checkSetRelations(&own, contributions))
			contributions = append(contributions, spec.Contribution{Reference: descriptor, Descriptor: &own})

			result, err := spec.Merge(contributions, spec.MergeOptions{ContextPath: "/staged/context.md"})
			require.NoError(t, err, "the kits this set lists do not merge")

			merged := result.Descriptor
			merged.Version = d.Version
			raw, err := marshalDescriptorYAML(merged)
			require.NoError(t, err)
			_, err = spec.ValidatePublished(raw, merged)
			require.NoError(t, err, "the merged descriptor is not a valid published kit")

			for _, version := range []string{spec.CapabilityAgentContext, spec.CapabilityAgentContextV2} {
				selected := selectExampleContext(t, merged, version)
				effective, err := spec.Compose([]spec.Contribution{{Reference: descriptor, Descriptor: selected}})
				require.NoError(t, err, "published set must compose after selecting %s", version)
				require.True(t, spec.HasCapability(effective.Capabilities, version))
			}

			require.Empty(t, merged.Requires,
				"a set example should answer its own requirements; %s leaves %v open", descriptor, merged.Requires)
		})
	}
	require.NotZero(t, sets, "no set examples found; the guard would pass vacuously")
}

func TestCodexACPExampleRequiresCompatibleCodex(t *testing.T) {
	for _, tc := range []struct {
		version    string
		compatible bool
	}{
		{version: "0.159.2"},
		{version: "0.160.0"},
		{version: "0.160.1", compatible: true},
		{version: "0.161.0", compatible: true},
		{version: "0.162.0", compatible: true},
		{version: "0.163.0", compatible: true},
		{version: "0.164.0", compatible: true},
		{version: "0.165.0"},
		{version: "1.0.0"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			adapter := publishedForm(t, filepath.Join("..", "..", "examples", "codex-acp", "codex-acp.yaml"))
			codex := publishedForm(t, filepath.Join("..", "..", "examples", "codex-mixin", "codex-mixin.yaml"))
			codex.Provides = []string{"codex@" + tc.version}

			_, err := resolve.ResolvePartial([]*resolve.Unit{
				{Reference: "codex-acp", Descriptor: adapter},
				{Reference: "codex-mixin", Descriptor: codex},
			})
			if tc.compatible {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "the set only offers codex@"+tc.version)
			}
		})
	}
}

// publishedForm reads an example descriptor and resolves its
// build-phase args from their defaults, which is the form a consumer
// sees: provides are literal, and a set's kit references name a
// registry rather than an arg.
func publishedForm(t *testing.T, descriptor string) *spec.Descriptor {
	t.Helper()
	return publishedFormWithArgs(t, descriptor, nil)
}

func publishedFormWithArgs(t *testing.T, descriptor string, overrides map[string]string) *spec.Descriptor {
	t.Helper()
	raw, err := os.ReadFile(descriptor)
	require.NoError(t, err)
	d, err := spec.Decode(raw)
	require.NoError(t, err)

	build := map[string]spec.Arg{}
	for name, decl := range d.Args {
		if decl.BuildArg != "" {
			build[name] = decl
		}
	}
	supplied := map[string]string{}
	for name, value := range overrides {
		if _, ok := build[name]; ok {
			supplied[name] = value
		}
	}
	values, err := spec.ResolveArgs(build, supplied)
	require.NoError(t, err, "%s: a build-phase arg has no default to expand", descriptor)
	expanded, err := spec.ExpandBuildArgs(raw, d.Args, values)
	require.NoError(t, err)
	out, err := spec.Decode(expanded)
	require.NoError(t, err)
	return out
}

// exampleFor maps a listed kit's reference back to the example that
// publishes it, by the sbx-kit-<example> convention kit:push uses.
func exampleFor(t *testing.T, ref string) string {
	t.Helper()
	name := path.Base(ref)
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimPrefix(name, "sbx-kit-")
	descriptor := filepath.Join("..", "..", "examples", name, name+".yaml")
	_, err := os.Stat(descriptor)
	require.NoError(t, err, "%s names no example under examples/%s", ref, name)
	return descriptor
}

func TestShellComposesWithBundledSkills(t *testing.T) {
	shell := publishedForm(t, filepath.Join("..", "..", "examples", "shell", "shell.yaml"))
	skills := publishedForm(t, filepath.Join("..", "..", "skills", "skills.yaml"))
	for _, version := range []string{spec.CapabilityAgentContext, spec.CapabilityAgentContextV2} {
		t.Run(version, func(t *testing.T) {
			selectedShell := selectExampleContext(t, shell, version)
			selectedSkills := selectExampleContext(t, skills, version)
			contributions := []spec.Contribution{
				{Reference: "shell", Descriptor: selectedShell},
				{Reference: "skills", Descriptor: selectedSkills},
			}
			composed, err := spec.Compose(contributions)
			require.NoError(t, err)
			if version == spec.CapabilityAgentContext {
				profiles, err := spec.AgentContextsOf(composed.Capabilities)
				require.NoError(t, err)
				require.Len(t, profiles, 1)
				require.Equal(t, "AGENTS.md", profiles[0].Filename)
				merged, err := spec.Merge(contributions, spec.MergeOptions{ContextPath: "/kit/context.md"})
				require.NoError(t, err)
				require.Len(t, merged.ContextSources, 2)
				require.Contains(t, merged.ContextSources[1].Content, "Kit authoring skills")
				require.Contains(t, merged.ContextSources[1].Content, "/usr/share/kit-skills/create-kit-v3/SKILL.md")
				return
			}
			profiles, err := spec.AgentContextsV2Of(composed.Capabilities)
			require.NoError(t, err)
			require.Len(t, profiles, 1)
			require.Equal(t, "AGENTS.md", profiles[0].Filename)
			require.Contains(t, profiles[0].Content, "Kit authoring skills")
			require.Contains(t, profiles[0].Content, "/usr/share/kit-skills/create-kit-v3/SKILL.md")
			shellProfiles, err := spec.AgentContextsV2Of(selectedShell.Capabilities)
			require.NoError(t, err)
			require.Contains(t, profiles[0].Content, shellProfiles[0].Content)
		})
	}
}
