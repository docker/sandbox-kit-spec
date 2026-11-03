package spec

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"regexp"
	"strings"
	"testing"
)

func dockerAuthKit(registries ...string) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityDockerAuth, Config: map[string]any{"registries": registries}}}}
}

func TestDockerAuthValidation(t *testing.T) {
	for _, registry := range []string{"docker.io", "registry-1.docker.io", "ghcr.io", "registry.example.com"} {
		_, err := Validate(dockerAuthKit(registry))
		require.NoError(t, err)
	}
	for _, registry := range []string{"", "localhost", "https://ghcr.io", "GHCR.io", "ghcr.io:443", "ghcr.io/path", "*.ghcr.io", "127.0.0.1", "a..example.com", "-a.example.com", "a-.example.com", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 126) + "ab"} {
		_, err := Validate(dockerAuthKit(registry))
		require.Error(t, err)
	}
	for _, config := range []map[string]any{nil, {}, {"registries": nil}, {"registries": []string{}}, {"registries": "ghcr.io"}, {"registries": []any{nil}}, {"registries": []string{"ghcr.io", "ghcr.io"}}, {"registries": []string{"docker.io", "registry-1.docker.io"}}, {"Registries": []string{"ghcr.io"}}, {"registries": []string{"ghcr.io"}, "tokenEndpoints": map[string]string{"ghcr.io": "https://ghcr.io/token"}}} {
		d := dockerAuthKit()
		d.Capabilities[0].Config = config
		_, err := Validate(d)
		require.Error(t, err)
	}
}

func TestDockerAuthCompositionAndSurface(t *testing.T) {
	a := dockerAuthKit("docker.io")
	a.Capabilities[0].Optional = true
	b := dockerAuthKit("ghcr.io")
	out, err := Compose([]Contribution{{Reference: "a", Descriptor: a}, {Reference: "b", Descriptor: b}})
	require.NoError(t, err)
	require.Len(t, out.Capabilities, 2)
	_, err = Validate(out)
	require.NoError(t, err)
	out, err = Compose([]Contribution{{Reference: "a", Descriptor: a}, {Reference: "required", Descriptor: dockerAuthKit("docker.io")}})
	require.NoError(t, err)
	require.Len(t, out.Capabilities, 1)
	require.False(t, out.Capabilities[0].Optional)
	original := SurfaceOf(a)
	require.Len(t, original.Services, 1)
	require.Contains(t, original.Services[0], CapabilityDockerAuth+"+")
	require.NotEmpty(t, DiffWidenings(original, SurfaceOf(b)))
}

func TestDockerAuthExpansion(t *testing.T) {
	d := mustDecode(t, `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/docker-auth@1
    config:
      registries: ["${{ kit.env.REGISTRY }}"]
`)
	_, err := Validate(d)
	require.NoError(t, err)
	for _, registry := range []string{"registry.example.com", "https://registry.example.com"} {
		effective, err := ExpandEnvironment(d, map[string]string{"REGISTRY": registry})
		require.NoError(t, err)
		raw, err := json.Marshal(effective)
		require.NoError(t, err)
		_, err = ValidateEffective(raw, effective)
		if strings.Contains(registry, "://") {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	d.Capabilities[0].Config["typo"] = true
	_, err = Validate(d)
	require.ErrorContains(t, err, "unknown field")
	d.Capabilities[0].Config = map[string]any{"registries": []string{"GHCR.io", "${{ kit.env.REGISTRY }}"}}
	_, err = Validate(d)
	require.Error(t, err)
}

func TestDockerAuthSchema(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityDockerAuth))
	require.Equal(t, []any{"registries"}, schema["required"])
	require.Len(t, at(t, schema, "properties"), 1)
	require.EqualValues(t, 1, at(t, schema, "properties", "registries")["minItems"])
	require.Equal(t, true, at(t, schema, "properties", "registries")["uniqueItems"])
	require.Equal(t, dockerAuthDNS.String(), literalPattern(t, at(t, schema, "definitions", "registry")))
	assertAcceptsKitArg(t, at(t, schema, "definitions", "registry"), "bearing")
	literal := at(t, schema, "definitions", "registry")["anyOf"].([]any)[0].(map[string]any)
	pattern := regexp.MustCompile(literal["pattern"].(string))
	ip := regexp.MustCompile(at(t, literal, "not")["pattern"].(string))
	for _, name := range []string{"123.456", "999.1.2.3", "01.2.3.4", "127.0.0.1", "255.255.255.255", "256.0.0.1", strings.Repeat("a", 63) + ".com", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "com"} {
		accepted := len(name) <= 253 && pattern.MatchString(name) && !ip.MatchString(name)
		require.Equal(t, validDockerAuthDNS(name), accepted, "schema disagrees for %q", name)
	}
}
