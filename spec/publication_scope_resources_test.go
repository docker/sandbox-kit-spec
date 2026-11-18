package spec

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicationScopeExpansionStaysWithinBudget(t *testing.T) {
	for _, field := range []string{"provides", "requires", "integrates"} {
		t.Run(field, func(t *testing.T) {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, CapabilityScopes: make([]CapabilityScope, 3000)}
			for i := range 300 {
				value := fmt.Sprintf("com.example/alias-%d", i)
				switch field {
				case "provides":
					d.Provides = append(d.Provides, value+"@1")
				case "requires":
					d.Requires = append(d.Requires, value)
				case "integrates":
					d.Integrates = append(d.Integrates, value)
				}
			}
			raw, err := json.Marshal(d)
			require.NoError(t, err)
			require.Less(t, len(raw), SizeErrorBytes)
			_, err = ValidateExpandedDeclarations(raw, d)
			require.NoError(t, err)
			// The input is small and valid. Its repeated publication metadata cannot
			// fit, so refusal must precede allocating the oversized scope expansion.
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			result, err := Merge([]Contribution{{Reference: "many-scopes", Descriptor: d}}, MergeOptions{})
			runtime.ReadMemStats(&after)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrDescriptorTooLarge)
			require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(32*1024*1024))
			again, err := json.Marshal(d)
			require.NoError(t, err)
			require.Equal(t, raw, again, "refusal must leave the caller's scope metadata unchanged")
		})
	}
}

func TestPublicationScopeBudgetIsSharedAcrossContributions(t *testing.T) {
	var contributions []Contribution
	for i := range 48 {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, CapabilityScopes: make([]CapabilityScope, 100)}
		for j := range 80 {
			d.Provides = append(d.Provides, fmt.Sprintf("com.example/alias-%d@1", j))
		}
		contributions = append(contributions, Contribution{Reference: fmt.Sprintf("kit-%d", i), Descriptor: d})
	}
	single, err := Merge(contributions[:1], MergeOptions{})
	require.NoError(t, err)
	raw, err := json.Marshal(single.Descriptor)
	require.NoError(t, err)
	require.Less(t, len(raw), SizeErrorBytes)
	// Each contribution's scope expansion fits separately. Their combined
	// publication does not, and must not retain all expansions before refusing.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := Merge(contributions, MergeOptions{})
	runtime.ReadMemStats(&after)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrDescriptorTooLarge)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(32*1024*1024))
}

func TestPublicationScopeBudgetPreservesBindingsAndDependencyDeduplication(t *testing.T) {
	dependency := "com.example/base >=1.0.0, <2.0.0"
	optional := "com.example/optional >=1.0.0"
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
		Provides: []string{"com.example/owned@1", "com.example/aggregate@1"},
		Requires: []string{dependency}, Integrates: []string{optional},
		CapabilityScopes: []CapabilityScope{
			{Provides: []string{"com.example/owned"}, Requires: []string{dependency}},
			{Integrates: []string{optional}},
		},
	}
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	_, err = ValidateExpandedDeclarations(raw, d)
	require.NoError(t, err)
	result, err := Merge([]Contribution{{Reference: "nested", Descriptor: d}}, MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, []CapabilityScope{
		{Provides: []string{"com.example/owned", "com.example/aggregate"}, Requires: []string{dependency}, Integrates: []string{optional}},
		{Provides: []string{"com.example/aggregate"}, Requires: []string{dependency}, Integrates: []string{optional}},
	}, result.Descriptor.CapabilityScopes)
	// JSON escapes the range's comparison signs. Refusal accounting uses that
	// serialized size while preserving the original dependency expressions.
	published, err := json.Marshal(result.Descriptor)
	require.NoError(t, err)
	require.Contains(t, string(published), `\u003e`)
	_, err = ValidatePublished(published, result.Descriptor)
	require.NoError(t, err)
	again, err := json.Marshal(d)
	require.NoError(t, err)
	require.Equal(t, raw, again)
}
