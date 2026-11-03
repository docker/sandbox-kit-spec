package spec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const buildSecretDescriptor = `schemaVersion: "3"
kind: mixin
provides: [private-deps@1.0.0]
args:
  token:
    description: Private dependency credential
    buildSecret: GITLAB_TOKEN
`

func TestBuildSecretDeclaration(t *testing.T) {
	d, err := Decode([]byte(buildSecretDescriptor))
	require.NoError(t, err)
	require.Equal(t, "GITLAB_TOKEN", d.Args["token"].BuildSecret)
	require.True(t, d.Args["token"].IsBuildPhase())
	_, err = ValidateRaw([]byte(buildSecretDescriptor), d)
	require.NoError(t, err)
	_, err = ValidatePublished([]byte(buildSecretDescriptor), d)
	require.NoError(t, err)
	values, err := ResolveArgs(d.Args, nil)
	require.NoError(t, err)
	require.Empty(t, values, "published secret declarations are not runtime inputs")
	_, err = KitArgValues(d.Args, nil)
	require.NoError(t, err)
	for _, expand := range []func([]byte, map[string]Arg, map[string]string) ([]byte, error){ExpandBuildArgs, ExpandCreateArgs} {
		raw, err := expand([]byte(buildSecretDescriptor), d.Args, nil)
		require.NoError(t, err)
		got, err := Decode(raw)
		require.NoError(t, err)
		require.Equal(t, d, got, "the declaration survives without a value")
	}
}

func TestBuildSecretRejectsValueConstraints(t *testing.T) {
	for _, extra := range []string{"env: TOKEN", "buildArg: TOKEN", `default: ""`, "required: true", "enum: [a]", "pattern: .*"} {
		t.Run(extra, func(t *testing.T) {
			raw := []byte(buildSecretDescriptor + "    " + extra + "\n")
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.ErrorContains(t, err, "buildSecret excludes")
		})
	}
	for _, id := range []string{"9TOKEN", "bad-id", "../token", "${{ kit.args.name }}"} {
		t.Run(id, func(t *testing.T) {
			d, err := Decode([]byte(buildSecretDescriptor))
			require.NoError(t, err)
			d.Args["token"] = Arg{BuildSecret: id}
			_, err = Validate(d)
			require.ErrorContains(t, err, "valid secret ID")
		})
	}
	for _, other := range []Arg{{BuildSecret: "GITLAB_TOKEN"}, {BuildArg: "GITLAB_TOKEN"}, {BuildArg: "token"}} {
		d, err := Decode([]byte(buildSecretDescriptor))
		require.NoError(t, err)
		d.Args["other"] = other
		_, err = Validate(d)
		require.Error(t, err)
	}
	d, err := Decode([]byte(buildSecretDescriptor))
	require.NoError(t, err)
	d.Args["GITLAB_TOKEN"] = Arg{}
	_, err = Validate(d)
	require.ErrorContains(t, err, "overlaps ordinary")
	d.Args = map[string]Arg{"token": {BuildSecret: "GITLAB_TOKEN", Required: false}}
	_, err = Validate(d)
	require.NoError(t, err)
}

func TestBuildSecretRejectsInterpolation(t *testing.T) {
	for _, field := range []string{
		`description: "${{ kit.args.token }}"`,
		"# ${{ kit.args.token }}",
		"build: |\n  FROM scratch\n  RUN echo '${{ kit.args.token }}'",
		"capabilities:\n  - type: com.example/custom@1\n    config: {value: '${{ kit.args.token }}'}",
	} {
		t.Run(field, func(t *testing.T) {
			raw := []byte(buildSecretDescriptor + field + "\n")
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.ErrorContains(t, err, "cannot be expanded")
			// Expansion must also refuse bypassing validation, without quoting bytes.
			for _, expand := range []func([]byte, map[string]Arg, map[string]string) ([]byte, error){ExpandBuildArgs, ExpandCreateArgs} {
				// Create expansion discards YAML comments; the raw validator owns them.
				if strings.HasPrefix(field, "#") {
					continue
				}
				out, err := expand(raw, d.Args, map[string]string{"token": "secret-bytes-never-print"})
				require.ErrorContains(t, err, "cannot be expanded")
				require.Nil(t, out)
				require.NotContains(t, err.Error(), "secret-bytes-never-print")
			}
		})
	}
}

func TestBuildSecretRejectsOrdinaryValues(t *testing.T) {
	decls := map[string]Arg{"token": {BuildSecret: "GITLAB_TOKEN"}}
	for _, value := range []string{"secret:gitlab", "secret-bytes-never-print"} {
		_, err := ResolveArgs(decls, map[string]string{"token": value})
		require.ErrorContains(t, err, "BuildKit secret session")
		require.NotContains(t, err.Error(), value)
		_, err = KitArgValues(decls, map[string]string{"token": value})
		require.ErrorContains(t, err, "build secret")
		require.NotContains(t, err.Error(), value)
	}
	err := CheckReExport("token", decls["token"], "other", Arg{}, true)
	require.ErrorContains(t, err, "cannot be re-exported")
	err = CheckReExport("normal", Arg{}, "token", decls["token"], true)
	require.ErrorContains(t, err, "cannot be re-exported")
	d, err := Decode([]byte(buildSecretDescriptor))
	require.NoError(t, err)
	d.Kind = KindSet
	d.Kits = []Kit{{Ref: "example.com/kit:1.0.0"}}
	_, err = Validate(d)
	require.ErrorContains(t, err, "no recipe to consume")
}

func TestBuildSecretRejectsEmptyDeclarations(t *testing.T) {
	for _, id := range []string{`""`, "null"} {
		raw := []byte(strings.ReplaceAll(buildSecretDescriptor, "buildSecret: GITLAB_TOKEN", "buildSecret: "+id))
		d, err := Decode(raw)
		require.NoError(t, err)
		_, err = ValidateRaw(raw, d)
		require.ErrorContains(t, err, "non-empty secret ID")
	}
	for _, field := range []string{`env: ""`, `buildArg: ""`, "default: null", "enum: []", `pattern: ""`} {
		raw := []byte(buildSecretDescriptor + "    " + field + "\n")
		d, err := Decode(raw)
		require.NoError(t, err)
		_, err = ValidateRaw(raw, d)
		require.ErrorContains(t, err, "buildSecret excludes")
	}
	// Merged YAML fields must have the same verdict as direct declarations.
	raw := []byte(strings.ReplaceAll(buildSecretDescriptor, "    buildSecret: GITLAB_TOKEN", "    <<: {buildSecret: GITLAB_TOKEN, pattern: ''}"))
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.ErrorContains(t, err, "buildSecret excludes")
}
