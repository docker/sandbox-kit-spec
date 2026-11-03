package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestBuildSecretRejectsBuildArgs(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
args:
  token: {buildSecret: GITLAB_TOKEN}
  version: {buildArg: VERSION, default: "1.0.0"}
provides: ["private@${{ kit.args.version }}"]
`)
	d, err := spec.Decode(raw)
	require.NoError(t, err)
	for _, key := range []string{"token", "GITLAB_TOKEN"} {
		for _, value := range []string{"do-not-print-secret", ""} {
			out, err := expandBuildPhase(raw, d, map[string]string{buildArgPrefix + key: value})
			require.ErrorContains(t, err, "use --secret id=GITLAB_TOKEN")
			require.Nil(t, out)
			require.NotContains(t, err.Error(), "do-not-print-secret")
			c := &fakeGatewayClient{}
			_, err = solveDockerfile(t.Context(), c, d, map[string]string{buildArgPrefix + key: value}, &companionSource{name: "kit.dockerfile"}, nil, "")
			require.ErrorContains(t, err, "use --secret")
			require.Empty(t, c.lastReq.Frontend, "must reject before invoking dockerfile.v0")
		}
	}
	out, err := expandBuildPhase(raw, d, nil)
	require.NoError(t, err)
	published, err := spec.Decode(out)
	require.NoError(t, err)
	require.Equal(t, "GITLAB_TOKEN", published.Args["token"].BuildSecret)
	require.Equal(t, []string{"private@1.0.0"}, published.Provides)
	_, err = spec.ValidatePublished(out, published)
	require.NoError(t, err)
}

func TestPublishedBuildSecretsAreNotSetInputs(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
provides: [private@1.0.0]
args:
  token: {buildSecret: TOKEN}
  timeout: {default: "30", env: TIMEOUT}
  flavor: {default: plain}
capabilities:
  - type: com.example/feature@1
    config: {flavor: "${{ kit.args.flavor }}"}
`)
	published, err := spec.Decode(raw)
	require.NoError(t, err)
	d, env, err := kitDeclarations(published, spec.Kit{}, nil)
	require.NoError(t, err)
	require.Empty(t, d.Args, "member inputs must not become inputs of the merged set")
	require.Equal(t, map[string]string{"TIMEOUT": "30"}, env)
	mode := "spicy"
	_, _, err = kitDeclarations(published, spec.Kit{Args: map[string]string{"flavor": "${{ kit.args.mode }}"}}, map[string]spec.Arg{"mode": {Default: &mode}})
	require.NoError(t, err, "a member's build-secret declaration must not interfere with runtime argument re-export")
	for _, value := range []string{"never-print-secret", "${{ kit.args.other }}"} {
		_, _, err = kitDeclarations(published, spec.Kit{Args: map[string]string{"token": value}}, nil)
		require.ErrorContains(t, err, "build secret")
		require.NotContains(t, err.Error(), value)
	}
}
