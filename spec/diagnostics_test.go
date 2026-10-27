package spec

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func validationPaths(t *testing.T, err error) []string {
	t.Helper()
	require.Error(t, err)
	var all ValidationErrors
	require.ErrorAs(t, err, &all)
	require.NotEmpty(t, all)
	paths := make([]string, len(all))
	for i, child := range all {
		var field *FieldError
		require.ErrorAs(t, child, &field)
		paths[i] = field.Path
	}
	return paths
}

func TestValidationCollectsIndependentFailures(t *testing.T) {
	tests := []struct {
		name, body string
		paths      []string
	}{
		{
			name: "top level and sorted args",
			body: `iconUrl: http://example.com/icon.png
provides: [BAD, ALSO_BAD]
args:
  zebra: {pattern: '['}
  alpha: {env: 9BAD, buildArg: 9BAD}
`,
			paths: []string{"iconUrl", "provides[0]", "provides[1]", "args.alpha", "args.alpha.env", "args.alpha.buildArg", "args.zebra.pattern"},
		},
		{
			name: "ssh agent errors preserve sibling diagnostics",
			body: `capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: relative}
  - type: com.docker.sandbox/ssh-agent@1
    config: {phase: null}
  - type: com.docker.sandbox/ssh-agent@1
    config: {phase: runtime}
  - type: com.docker.sandbox/ssh-agent@1
    config: {phase: runtime, unrestricted: false, sign: [git]}
  - type: com.docker.sandbox/port@1
    config: {container: 0}
`,
			// Each entry's own errors come before the repeats between entries.
			paths: []string{"capabilities[0].config.path", "capabilities[1].config.phase", "capabilities[4].config.container", "capabilities[3].config.phase"},
		},
		{
			name: "fields within and across capabilities",
			body: `capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: relative, size: nonsense, mode: '888'}
  - type: com.docker.sandbox/port@1
    config: {container: 0, transport: quic}
`,
			paths: []string{"capabilities[0].config.path", "capabilities[0].config.size", "capabilities[0].config.mode", "capabilities[1].config.container", "capabilities[1].config.transport"},
		},
		{
			name: "failed decode skips dependent values",
			body: `capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: wrong, transport: quic}
  - type: com.docker.sandbox/resources@1
    config: {cpu: -1, memory: wrong}
`,
			paths: []string{"capabilities[0].config", "capabilities[1].config.cpu", "capabilities[1].config.memory"},
		},
		{
			name: "lifecycle lists",
			body: `capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      install: [{env: [bad-name]}]
      startup: [{env: [bad-name]}]
      files:
        - {path: relative, mode: '888'}
        - {path: another, mode: '999'}
`,
			paths: []string{"capabilities[0].config.install[0]", "capabilities[0].config.install[0].env[0]", "capabilities[0].config.startup[0]", "capabilities[0].config.startup[0].env[0]", "capabilities[0].config.files[0].path", "capabilities[0].config.files[0].mode", "capabilities[0].config.files[1].path", "capabilities[0].config.files[1].mode"},
		},
		{
			name: "both network phases",
			body: `capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      install:
        allow: [{hosts: [], methods: [get], paths: [relative]}]
      runtime:
        deny: [{hosts: [example.com], methods: [post], paths: [relative]}]
`,
			paths: []string{"capabilities[0].config.install.allow[0].hosts", "capabilities[0].config.install.allow[0].methods", "capabilities[0].config.install.allow[0].paths", "capabilities[0].config.runtime.deny[0].methods", "capabilities[0].config.runtime.deny[0].paths"},
		},
		{
			name: "invalid credential does not hide valid sibling cross-checks",
			body: `capabilities:
  - type: com.docker.sandbox/credential@1
    config: {service: broken, phase: runtime, apiKey: wrong}
  - type: com.docker.sandbox/credential@1
    config:
      service: github
      phase: runtime
      apiKey:
        inject: [{domain: one.example.com}, {domain: two.example.com}]
`,
			paths: []string{"capabilities[0].config", "capabilities[1].config.apiKey.inject[0].domain", "capabilities[1].config.apiKey.inject[1].domain"},
		},
		{
			name: "duplicate malformed credential has no dependent cross-check",
			body: `capabilities:
  - type: com.docker.sandbox/credential@1
    config: {service: broken, phase: runtime, apiKey: wrong}
  - type: com.docker.sandbox/credential@1
    config: {service: broken, phase: runtime, apiKey: wrong}
`,
			paths: []string{"capabilities[0].config", "capabilities[1].config", "capabilities[1]"},
		},
		{
			name: "invalid policy does not invent missing allow entries",
			body: `capabilities:
  - type: com.docker.sandbox/network-policy@2
    config: {runtime: wrong}
  - type: com.docker.sandbox/credential@1
    config:
      service: github
      phase: runtime
      apiKey: {inject: [{domain: example.com}]}
  - type: com.docker.sandbox/port@1
    config: {container: 0}
`,
			paths: []string{"capabilities[0].config", "capabilities[2].config.container"},
		},
		{
			name: "kit entries and independent digest",
			body: `kits:
  - ref: ''
    digest: wrong
  - ref: ./local
    digest: also-wrong
`,
			paths: []string{"kits[0].ref", "kits[0].digest", "kits[1].digest", "kits[1].ref"},
		},
		{
			name: "credential nulls",
			body: `capabilities:
  - type: com.docker.sandbox/credential@1
    config:
      service: github
      phase: runtime
      apiKey: {name: TOKEN, proxyManaged: null}
      oauth:
        credentialFile:
          path: /tmp/token
          format: toml
          structure: {b: null, a: null}
`,
			paths: []string{"capabilities[0].config.apiKey.proxyManaged", "capabilities[0].config.oauth.credentialFile.structure.a", "capabilities[0].config.oauth.credentialFile.structure.b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := mustDecode(t, "schemaVersion: \"3\"\nkind: mixin\n"+tt.body)
			_, err := Validate(d)
			require.Equal(t, tt.paths, validationPaths(t, err))
			first := err.Error()
			for range 10 {
				_, err = Validate(d)
				require.EqualError(t, err, first)
			}
		})
	}
}

func TestValidationRawAndPublishedCollectFailures(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: set
iconUrl: http://example.com/icon.png
provides: [BAD, unversioned, 'tool@${{ kit.args.version }}']
args:
  version: {default: '1.0.0', buildArg: VERSION}
  bad: {pattern: '['}
kits:
  - ref: example.com/kit:1
  - ref: example.com/another:1
build: '${{ kit.args.missing }}'
description: '${{ kit.args.other }}'
` + "#" + strings.Repeat("x", SizeWarnBytes))
	d, err := Decode(raw)
	require.NoError(t, err)
	warnings, err := ValidatePublished(raw, d)
	require.Len(t, warnings, 1, "warnings survive semantic and published errors")
	require.Equal(t, []string{
		"iconUrl", "provides[0]", "args.bad.pattern", "build", "build", "description",
		"provides[2]", "provides[1]", "kind", "kits[0].digest", "kits[1].digest",
	}, validationPaths(t, err))
	require.NotContains(t, err.Error(), "args.version:", "point at uses and avoid duplicate expansion errors")

	oversized := append(raw, []byte(strings.Repeat("x", SizeErrorBytes))...)
	_, err = ValidateRaw(oversized, d)
	require.ErrorContains(t, err, "over the 524288 byte budget")
	require.ErrorContains(t, err, "args.bad.pattern")
	require.ErrorContains(t, err, "kit.args.other")
}

func TestValidationEffectiveCollectsFailures(t *testing.T) {
	raw := []byte("schemaVersion: \"3\"\nkind: mixin\niconUrl: http://example.com\ndescription: '${{ kit.args.pending }}'\nargs: {pending: {}}\n")
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateEffective(raw, d)
	require.Equal(t, []string{"", "iconUrl"}, validationPaths(t, err))
}

func TestValidationSourceLocations(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: 99999}
args:
  version:
    pattern: '['
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = Validate(d)
	located := WithSource(fmt.Errorf("validate kit: %w", err), "kit.yaml", raw)
	require.ErrorContains(t, located, "validate kit: kit.yaml:5:25: capabilities[0].config.container:")
	require.ErrorContains(t, located, "\nkit.yaml:8:14: args.version.pattern:")
	require.Equal(t, []string{"capabilities[0].config.container", "args.version.pattern"}, validationPaths(t, located))
	var first *FieldError
	require.ErrorAs(t, located, &first)
	require.Equal(t, "capabilities[0].config.container", first.Path)

	missing := fieldErrorf("capabilities[0].config.service", "service is required")
	require.EqualError(t, WithSource(missing, "kit.yaml", raw), "kit.yaml:5:13: capabilities[0].config.service: service is required\n  |\n5 |     config: {container: 99999}\n  |             ^")
	require.EqualError(t, WithSource(fieldErrorf("absent", "required"), "kit.yaml", raw), "kit.yaml:1:1: absent: required\n  |\n1 | schemaVersion: \"3\"\n  | ^")
	require.EqualError(t, WithSource(missing, "kit.yaml", []byte("[")), "kit.yaml: capabilities[0].config.service: service is required")
	require.EqualError(t, WithSource(missing, "", nil), "capabilities[0].config.service: service is required")
	require.Nil(t, WithSource(nil, "kit.yaml", raw))
}

func TestValidationSourceRawReferences(t *testing.T) {
	raw := []byte("schemaVersion: \"3\"\nkind: mixin\ndescription: '${{ kit.args.missing }}'\n")
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.Equal(t, []string{"description"}, validationPaths(t, err))
	require.ErrorContains(t, WithSource(err, "kit.yaml", raw), "kit.yaml:3:14: description:")
}

func TestValidationErrorsUnwrap(t *testing.T) {
	sentinel := errors.New("underlying failure")
	all := ValidationErrors{fieldErrorf("kind", "invalid"), fmt.Errorf("context: %w", sentinel)}
	require.Len(t, all.Unwrap(), 2)
	located := WithSource(all, "kit.yaml", nil)
	require.ErrorIs(t, located, sentinel)
	require.ErrorContains(t, located, "\nkit.yaml: context: underlying failure")
}

func TestValidationSourceExcerpts(t *testing.T) {
	tests := []struct{ name, raw, path, want string }{
		{
			name: "quoted scalar",
			raw:  "args:\n  version:\n    pattern: '['\n",
			path: "args.version.pattern",
			want: "kit.yaml:3:14: args.version.pattern: invalid\n  |\n3 |     pattern: '['\n  |              ^",
		},
		{
			name: "empty scalar",
			raw:  "kind: ''\n",
			path: "kind",
			want: "kit.yaml:1:7: kind: invalid\n  |\n1 | kind: ''\n  |       ^",
		},
		{
			name: "two digit line number and CRLF",
			raw:  strings.Repeat("# comment\r\n", 9) + "kind: bad\r\n",
			path: "kind",
			want: "kit.yaml:10:7: kind: invalid\n   |\n10 | kind: bad\n   |       ^",
		},
		{
			name: "UTF-8 columns count runes",
			raw:  "args: {é: {pattern: '['}}\n",
			path: "args.é.pattern",
			want: "kit.yaml:1:21: args.é.pattern: invalid\n  |\n1 | args: {é: {pattern: '['}}\n  |                     ^",
		},
		{
			name: "tabs use matching stops",
			raw:  "iconUrl:\thttp://example.com\n",
			path: "iconUrl",
			want: "kit.yaml:1:10: iconUrl: invalid\n  |\n1 | iconUrl:\thttp://example.com\n  |         \t^",
		},
		{
			name: "multiline values show opening line",
			raw:  "description: |\n  first\n  second\n",
			path: "description",
			want: "kit.yaml:1:14: description: invalid\n  |\n1 | description: |\n  |              ^",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := fieldErrorf(tt.path, "invalid")
			located := WithSource(original, "kit.yaml", []byte(tt.raw))
			require.EqualError(t, located, tt.want)
			require.ErrorIs(t, located, original)
		})
	}
}

func ExampleWithSource() {
	raw := []byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: 99999}
args:
  version:
    pattern: '['
`)
	d, err := Decode(raw)
	if err != nil {
		panic(err)
	}
	_, err = Validate(d)
	fmt.Println(WithSource(err, "kit.yaml", raw))
	// Output:
	// kit.yaml:5:25: capabilities[0].config.container: capabilities[0]: container port 99999 out of range
	//   |
	// 5 |     config: {container: 99999}
	//   |                         ^
	//
	// kit.yaml:8:14: args.version.pattern: args.version: invalid pattern: error parsing regexp: missing closing ]: `[`
	//   |
	// 8 |     pattern: '['
	//   |              ^
}
