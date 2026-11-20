package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// The same string can legitimately occur before the field — a description
// mentioning the file — and a textual first-occurrence replace rewrote
// the prose while the field kept pointing at the build context.
func TestRewriteContentFileTouchesTheFieldNotTheProse(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: mixin
displayName: Guidance
description: reads ./context.md at build time
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./context.md
`)

	out, err := rewriteContentFile(published, "./context.md", "/usr/share/sandbox/kit/demo/context.md")
	require.NoError(t, err)

	d, err := spec.Decode(out)
	require.NoError(t, err)
	require.Contains(t, d.Description, "./context.md", "prose must keep the authored path")

	ac, err := spec.AgentContextOf(d.Capabilities)
	require.NoError(t, err)
	require.NotNil(t, ac)
	require.Equal(t, "/usr/share/sandbox/kit/demo/context.md", ac.ContentFile)
}

func TestRewriteContentFileFailsWhenTheFieldIsAbsent(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: mixin
displayName: No guidance
`)
	_, err := rewriteContentFile(published, "./context.md", "/staged")
	require.ErrorContains(t, err, "could not rewrite")
}

// The annotation carries the JSON and the layers stage the YAML, and the
// staged-sources check compares them as documents — so an amendment that
// reached only one of them would fail the build it just assembled.
func TestDerivedProvidesLandInBothTheBytesAndTheDocument(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: workload
displayName: Shell
version: "1.0.0"
# What the author claims, which the derivation leaves alone.
provides:
  - shell
`)

	out, pd, err := withDerivedProvides(published, []string{"deb/bash@5.2.37", "deb/jq@1.8.2"})
	require.NoError(t, err)
	require.Equal(t, []string{"shell", "deb/bash@5.2.37", "deb/jq@1.8.2"}, pd.Provides,
		"authored entries keep their place and their order")

	fromBytes, err := spec.Decode(out)
	require.NoError(t, err)
	require.Equal(t, pd.Provides, fromBytes.Provides, "the staged copy must say what the annotation says")
	require.Contains(t, string(out), "# What the author claims",
		"the staged copy is what a reader finds in the image, so comments survive")
}

// A kit that declared nothing still carries whatever its base image put
// there, so the field is created rather than skipped.
func TestDerivedProvidesCreateTheFieldWhenThereIsNone(t *testing.T) {
	for _, published := range []string{
		"schemaVersion: \"3\"\nkind: workload\ndisplayName: Bare\nversion: \"1.0.0\"\n",
		"schemaVersion: \"3\"\nkind: workload\ndisplayName: Bare\nversion: \"1.0.0\"\nprovides:\n",
		"schemaVersion: \"3\"\nkind: workload\ndisplayName: Bare\nversion: \"1.0.0\"\nprovides: []\n",
	} {
		out, pd, err := withDerivedProvides([]byte(published), []string{"apk/musl@1.2.5"})
		require.NoError(t, err, published)
		require.Equal(t, []string{"apk/musl@1.2.5"}, pd.Provides, published)

		fromBytes, err := spec.Decode(out)
		require.NoError(t, err, published)
		require.Equal(t, pd.Provides, fromBytes.Provides, published)
	}
}

// The entries come out of a base image nobody here controls, so the
// assembled document is held to the published rules rather than trusted.
func TestDerivedProvidesAreValidatedNotTrusted(t *testing.T) {
	published := []byte("schemaVersion: \"3\"\nkind: workload\ndisplayName: Shell\nversion: \"1.0.0\"\n")

	_, _, err := withDerivedProvides(published, []string{"deb/Not A Name@1.0.0"})
	require.Error(t, err)
	require.ErrorContains(t, err, "derived from image content")
}

// `provides: *shared` decodes into a perfectly good list, so a build must
// not fail on it — and appending through the alias would extend the
// anchor, so a derived package would turn up in whatever else uses it.
func TestDerivedProvidesCopyASequenceAlias(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: workload
displayName: Shell
version: "1.0.0"
conflicts: &shared
  - shell
provides: *shared
`)

	out, pd, err := withDerivedProvides(published, []string{"deb/bash@5.2.37"})
	require.NoError(t, err)
	require.Equal(t, []string{"shell", "deb/bash@5.2.37"}, pd.Provides)

	fromBytes, err := spec.Decode(out)
	require.NoError(t, err)
	require.Equal(t, pd.Provides, fromBytes.Provides)
	require.Equal(t, []string{"shell"}, fromBytes.Conflicts,
		"the anchor's other users must not inherit the derived entries")
}

// The other direction, where provides defines the anchor: appending would
// add a derived package to the field that aliases it, and the anchor
// cannot move without leaving that alias pointing at nothing. Saying so
// beats corrupting the other field quietly.
func TestDerivedProvidesRefuseAnAnchorSomethingElseUses(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: workload
displayName: Shell
version: "1.0.0"
provides: &shared
  - shell
conflicts: *shared
`)

	_, _, err := withDerivedProvides(published, []string{"deb/bash@5.2.37"})
	require.ErrorContains(t, err, "&shared")
	require.ErrorContains(t, err, "write the shared list out")

	// An anchor nobody references is inert, so it is appended to as any
	// other list would be.
	alone := []byte(`schemaVersion: "3"
kind: workload
displayName: Shell
version: "1.0.0"
provides: &shared
  - shell
`)
	_, pd, err := withDerivedProvides(alone, []string{"deb/bash@5.2.37"})
	require.NoError(t, err)
	require.Equal(t, []string{"shell", "deb/bash@5.2.37"}, pd.Provides)
}

func TestRewriteGroupedContextDoesNotRewriteItsSiblings(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config: {contentFile: ./always.md}
  - group:
      optional: true
      capabilities:
        - type: com.docker.sandbox/agent-context@1
          config: {contentFile: ./maybe.md}
`)
	out, err := rewriteContentFile(raw, "./maybe.md", "/staged/maybe.md")
	require.NoError(t, err)
	d, err := spec.Decode(out)
	require.NoError(t, err)
	require.Equal(t, "./always.md", d.Capabilities[0].Config["contentFile"])
	require.Equal(t, "/staged/maybe.md", d.Capabilities[1].Group.Capabilities[0].Config["contentFile"])
}

// A merge key can supply provides, and then the root mapping has no
// provides key of its own. Creating one for the derived entries would
// override the inherited list, so the author's entries are carried into
// the list that is created.
func TestDerivedProvidesKeepWhatAMergeKeyProvides(t *testing.T) {
	published := []byte(`schemaVersion: "3"
kind: workload
version: "1.0.0"
<<: {provides: ["agent@1.0.0"], displayName: Merged}
`)

	out, pd, err := withDerivedProvides(published, []string{"python@3.14.10", "deb/bash@5.2.37"})
	require.NoError(t, err)
	require.Equal(t, []string{"agent@1.0.0", "python@3.14.10", "deb/bash@5.2.37"}, pd.Provides)
	require.Equal(t, "Merged", pd.DisplayName, "the rest of what was merged is untouched")

	fromBytes, err := spec.Decode(out)
	require.NoError(t, err)
	require.Equal(t, pd.Provides, fromBytes.Provides)

	// An anchored mapping merged by alias is not edited: whatever else
	// refers to it still sees the list it had.
	shared := []byte(`schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.example/thing@1
    config: &shared
      provides: ["agent@1.0.0"]
<<: *shared
`)
	_, pd, err = withDerivedProvides(shared, []string{"deb/bash@5.2.37"})
	require.NoError(t, err)
	require.Equal(t, []string{"agent@1.0.0", "deb/bash@5.2.37"}, pd.Provides)
	require.Equal(t, map[string]any{"provides": []any{"agent@1.0.0"}}, pd.Capabilities[0].Config)
}
