package spec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every descriptor this repository ships must decode and validate against
// the current grammar; one the spec has drifted away from is worse than
// none.
//
// Two globs, because a kit that has to live next to the content it ships
// sits outside examples/ and would otherwise have no coverage at all — a
// grammar change could then break a published kit with every test green.
// skills/ is the only such kit today and is named literally rather than
// discovered: a wildcard over the repository root would silently stop
// covering a kit that moved, which is the failure this exists to prevent.
// Another one means another glob here, deliberately.
func TestExamplesDecodeAndValidate(t *testing.T) {
	// Each glob is asserted non-empty on its own, before they are joined.
	// A single check on the combined list would be satisfied by either one,
	// so a renamed examples/ would leave this green while covering only the
	// skills kit — the same silent loss of coverage the second glob exists
	// to prevent.
	matches, err := filepath.Glob(filepath.Join("..", "examples", "*", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "the examples/ descriptors must be covered")

	rootKits, err := filepath.Glob(filepath.Join("..", "skills", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, rootKits, "the skills kit descriptor must be covered")

	matches = append(matches, rootKits...)
	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateDeclarations(raw, d)
			require.NoError(t, err)
			contexts := map[string]Capability{}
			for _, c := range DeclaredCapabilities(d.Capabilities) {
				if c.Type == CapabilityAgentContext || c.Type == CapabilityAgentContextV2 {
					require.True(t, c.Optional, "context versions must remain optional during rollout")
					contexts[c.Type] = c
				}
			}
			if len(contexts) > 0 {
				require.Len(t, contexts, 2, "offer legacy and modern context")
				require.Equal(t, contexts[CapabilityAgentContext].Config, contexts[CapabilityAgentContextV2].Config)
				values, err := ResolveArgs(d.Args, nil)
				require.NoError(t, err)
				expanded, err := ExpandBuildArgs(raw, d.Args, values)
				require.NoError(t, err)
				expanded, err = ExpandCreateArgs(expanded, d.Args, values)
				require.NoError(t, err)
				d, err = Decode(expanded)
				require.NoError(t, err)
				for _, version := range []string{CapabilityAgentContext, CapabilityAgentContextV2} {
					selected := selectExampleContext(t, d, version)
					require.True(t, HasCapability(selected.Capabilities, version))
				}
			}
		})
	}
}

// Model a runtime that supports one context version and the other example
// capabilities. Selection must precede composition, even for a single Kit.
func selectExampleContext(t *testing.T, d *Descriptor, version string) *Descriptor {
	t.Helper()
	var supported []string
	for _, typ := range KnownCapabilities() {
		if typ != CapabilityAgentContext && typ != CapabilityAgentContextV2 || typ == version {
			supported = append(supported, typ)
		}
	}
	selection, err := SelectCapabilities(t.Context(), "example", d, Supported(supported...))
	require.NoError(t, err)
	selected := *d
	selected.Capabilities = selection.Capabilities
	return &selected
}
