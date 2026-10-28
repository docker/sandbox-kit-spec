package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Index annotations need a guaranteed image index. Explicit attestations
// provide that guarantee; inline-only default provenance is exporter-
// dependent. The integration tests judge the resulting export shapes.
func TestAttestationsRequested(t *testing.T) {
	require.False(t, attestationsRequested(map[string]string{}))
	require.True(t, attestationsRequested(map[string]string{"attest:provenance": "mode=min"}))
	require.True(t, attestationsRequested(map[string]string{"attest:sbom": ""}))
	require.False(t, attestationsRequested(map[string]string{"attest:provenance": "disabled=true"}))
	require.True(t, attestationsRequested(map[string]string{
		"attest:provenance": "disabled=true",
		"attest:sbom":       "",
	}))
	// The selected exporter may omit inline-only provenance, leaving
	// no image index. The gateway cannot distinguish that from an
	// image exporter retaining the same provenance in an index.
	require.False(t, attestationsRequested(map[string]string{"attest:provenance": "mode=min,inline-only=true"}))
	require.True(t, attestationsRequested(map[string]string{
		"attest:provenance": "mode=min,inline-only=true",
		"attest:sbom":       "",
	}))
}
