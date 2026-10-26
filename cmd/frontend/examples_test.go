package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Comment-descriptor examples get the same drift guard the YAML examples
// have in spec/examples_test.go: every example dockerfile carrying a
// `# kit:` block must extract, decode, and validate against the current
// grammar. At least one example must carry the form: with none, this test
// passes having judged nothing, and the form stops being exercised by
// kits:dev either.
func TestExampleCommentDescriptorsValidate(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "examples", "*", "*.dockerfile"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "no example dockerfiles scanned")

	found := 0
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		yamlBytes, ok := extractCommentDescriptor(raw)
		if !ok {
			continue // a companion dockerfile, validated through its kit.yaml
		}
		found++
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			d, err := spec.Decode(yamlBytes)
			require.NoError(t, err)
			_, err = spec.ValidateDeclarations(yamlBytes, d)
			require.NoError(t, err)
			contexts := map[string]spec.Capability{}
			for _, c := range spec.DeclaredCapabilities(d.Capabilities) {
				if c.Type == spec.CapabilityAgentContext || c.Type == spec.CapabilityAgentContextV2 {
					require.True(t, c.Optional)
					contexts[c.Type] = c
				}
			}
			require.Len(t, contexts, 2)
			require.Equal(t, contexts[spec.CapabilityAgentContext].Config, contexts[spec.CapabilityAgentContextV2].Config)
			for _, version := range []string{spec.CapabilityAgentContext, spec.CapabilityAgentContextV2} {
				selected := selectExampleContext(t, d, version)
				require.True(t, spec.HasCapability(selected.Capabilities, version))
			}
		})
	}
	require.NotZero(t, found, "no example carries a %s comment descriptor", kitCommentMarker)
}

func selectExampleContext(t *testing.T, d *spec.Descriptor, version string) *spec.Descriptor {
	t.Helper()
	var supported []string
	for _, typ := range spec.KnownCapabilities() {
		if typ != spec.CapabilityAgentContext && typ != spec.CapabilityAgentContextV2 || typ == version {
			supported = append(supported, typ)
		}
	}
	selection, err := spec.SelectCapabilities(t.Context(), "example", d, spec.Supported(supported...))
	require.NoError(t, err)
	selected := *d
	selected.Capabilities = selection.Capabilities
	return &selected
}
