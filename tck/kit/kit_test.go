package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// fake is an artifact assembled in memory, so each check can be shown to
// catch the thing it exists for.
type fake struct {
	annotations map[string]string
	config      ocispec.Image
	layers      []ocispec.Descriptor
	layersKnown bool
	files       map[string][]byte
	// stats overrides the permission metadata a path reports; a file
	// absent from it reads as root-owned and world-executable, which is
	// what the shells an image ships are.
	stats map[string]FileStat
	// readErrs makes a path unreadable rather than absent, which is how
	// a source reports what it will not buffer.
	readErrs map[string]error
	indexAnn map[string]string
	hasIndex bool
	// kits are the artifacts a merged set lists, keyed by reference,
	// so the declaration check has something to compare against.
	kits map[string]Artifact
	// dirs are the directory entries the layers carry, and dangling the
	// links they do not resolve.
	dirs     map[string]FileStat
	dangling []Symlink
}

func (f *fake) DirStat(_ context.Context, name string) (FileStat, bool, error) {
	st, ok := f.dirs[name]
	return st, ok, nil
}

func (f *fake) DanglingSymlinks(context.Context) ([]Symlink, error) { return f.dangling, nil }

func (f *fake) ResolveKit(_ context.Context, ref, _ string) (Artifact, error) {
	if a, ok := f.kits[ref]; ok {
		return a, nil
	}
	return nil, errNoRegistry
}

func (f *fake) Annotations() map[string]string { return f.annotations }

func (f *fake) Config(context.Context) (*ocispec.Image, error) { return &f.config, nil }

func (f *fake) Layers(context.Context) ([]ocispec.Descriptor, bool, error) {
	return f.layers, f.layersKnown, nil
}

func (f *fake) ReadFile(_ context.Context, name string) ([]byte, bool, error) {
	if err, ok := f.readErrs[name]; ok {
		return nil, false, err
	}
	body, ok := f.files[name]
	return body, ok, nil
}

func (f *fake) FileStat(_ context.Context, name string) (FileStat, bool, error) {
	if _, ok := f.files[name]; !ok {
		return FileStat{}, false, nil
	}
	if st, ok := f.stats[name]; ok {
		return st, true, nil
	}
	return FileStat{Mode: 0o755, Regular: true}, true, nil
}

func (f *fake) StagedStems(context.Context) ([]string, error) {
	var names []string
	for p := range f.files {
		if stems := stemsFromPaths([]string{p[1:]}); len(stems) == 1 {
			names = append(names, stems[0])
		}
	}
	return names, nil
}

func (f *fake) IndexAnnotations() (map[string]string, bool) { return f.indexAnn, f.hasIndex }

const stem = "demo"

// conforming builds a mixin that passes every check, for tests to break
// one thing at a time.
func conforming(t *testing.T) *fake {
	t.Helper()
	authored := "schemaVersion: \"3\"\nkind: mixin\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	ann := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: "3",
	}
	for k, v := range spec.OCIAnnotations(d) {
		ann[k] = v
	}
	return &fake{
		annotations: ann,
		layers:      []ocispec.Descriptor{{Digest: "sha256:aaaa"}},
		layersKnown: true,
		files: map[string][]byte{
			path.Join(StagedKitRoot, stem, stagedDescriptorName): []byte(authored),
		},
	}
}

func findings(t *testing.T, a Artifact) map[string]report.Finding {
	t.Helper()
	rep, err := Run(context.Background(), a)
	require.NoError(t, err)
	out := map[string]report.Finding{}
	for _, f := range rep.Findings {
		out[f.Check] = f
	}
	return out
}

func TestConformingKitHasNoFindings(t *testing.T) {
	rep, err := Run(context.Background(), conforming(t))
	require.NoError(t, err)
	require.Empty(t, rep.Findings, "a conforming kit must produce nothing to report")
	require.False(t, rep.Failed())
	require.NoError(t, rep.Err())
}

func TestAnArtifactWithoutTheDescriptorAnnotationIsNotAKit(t *testing.T) {
	a := conforming(t)
	delete(a.annotations, spec.AnnotationDescriptor)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["descriptor-annotation"].Severity)
	require.Contains(t, got["descriptor-annotation"].Detail, "not a kit")
}

func TestSchemaVersionAnnotationMustMatchTheDescriptor(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.AnnotationSchemaVersion] = "2"

	require.Equal(t, report.Fail, findings(t, a)["schema-version-annotation"].Severity)
}

// A kit published before the build stamp existed carries none, and there
// is nothing wrong with it — the check judges shape when there is shape
// to judge and is otherwise silent.
func TestAKitWithoutABuildStampIsStillConforming(t *testing.T) {
	a := conforming(t)
	require.NotContains(t, a.annotations, spec.AnnotationBuiltBy)

	require.NotContains(t, findings(t, a), "built-by-annotation")
}

// No reader can verify which frontend built an image, so the stamp is
// judged only on being readable: a consumer that cannot decode it learns
// less than one that found nothing there.
func TestAnUnreadableBuildStampFails(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.AnnotationBuiltBy] = "docker/sandbox-kit 3.0.0"

	got := findings(t, a)
	require.Equal(t, report.Fail, got["built-by-annotation"].Severity)
	require.Contains(t, got["built-by-annotation"].Detail, "does not decode")
}

// An empty field is a stamp that names nothing: a frontend that has no
// release to report says "dev", which is a claim, where "" is silence
// wearing the shape of one.
func TestABuildStampMustNameItsFrontendAndVersion(t *testing.T) {
	for field, want := range map[string]string{
		"name":    "names no frontend",
		"version": "names no version",
	} {
		stamp := spec.BuiltBy{Name: "docker/sandbox-kit", Version: "3.0.0"}
		switch field {
		case "name":
			stamp.Name = ""
		case "version":
			stamp.Version = ""
		}
		raw, err := stamp.Marshal()
		require.NoError(t, err)

		a := conforming(t)
		a.annotations[spec.AnnotationBuiltBy] = raw

		got := findings(t, a)
		require.Equal(t, report.Fail, got["built-by-annotation"].Severity, "for an empty %s", field)
		require.Contains(t, got["built-by-annotation"].Detail, want)
	}
}

// The stamp the frontend actually writes has to pass the suite that runs
// inside that same frontend, or every build would fail its own self-check.
func TestAWellFormedBuildStampPasses(t *testing.T) {
	raw, err := spec.BuiltBy{
		Name:     "docker/sandbox-kit",
		Version:  "3.0.0-m.5",
		Revision: "2f9a1c4e8b7d6a5c4b3e2d1f0a9b8c7d6e5f4a3b",
	}.Marshal()
	require.NoError(t, err)

	a := conforming(t)
	a.annotations[spec.AnnotationBuiltBy] = raw

	require.NotContains(t, findings(t, a), "built-by-annotation")
}

// The capabilities annotation is an index, never a second source, so it
// has to agree with the descriptor exactly — including being absent when
// the kit asks for nothing.
func TestCapabilitiesAnnotationMustMatchTheDescriptor(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.AnnotationCapabilities] = "com.docker.sandbox/port@1"

	got := findings(t, a)
	require.Equal(t, report.Fail, got["capabilities-annotation"].Severity)
	require.Contains(t, got["capabilities-annotation"].Detail, "requests no capabilities")
}

func TestOCIAnnotationsMustBeDerivedFromTheDescriptor(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.OCIAnnotationTitle] = "Something Else"

	require.Equal(t, report.Fail, findings(t, a)["oci-annotations"].Severity)
}

// SOURCE_DATE_EPOCH can produce a reproducible timestamp. The artifact
// exposes its format, not the builder input it came from.
func TestCreatedAnnotationAcceptsRFC3339(t *testing.T) {
	for _, created := range []string{
		"2025-10-01T12:00:00Z",
		"1970-01-01T00:00:00Z",
		"2025-10-01T12:00:00.123456789Z",
		"2025-10-01T14:00:00+02:00",
		"2025-10-01t12:00:00z",
		"2025-10-01t12:00:00Z",
		"2025-10-01T12:00:00z",
		"2025-10-01T12:00:00-00:00",
		"2024-02-29T12:00:00Z",
		"1990-12-31T23:59:60Z",
		"1990-12-31t23:59:60.5z",
		"1991-01-01T00:59:60+01:00",
	} {
		t.Run(created, func(t *testing.T) {
			a := conforming(t)
			a.annotations[ocispec.AnnotationCreated] = created

			rep, err := Run(context.Background(), a)
			require.NoError(t, err)
			require.Empty(t, rep.Findings)
		})
	}
}

func TestCreatedAnnotationRejectsInvalidTimestamps(t *testing.T) {
	for _, created := range []string{
		"",
		"1759320000",
		"not-a-timestamp",
		"2025-10-01",
		"2025-10-01T12:00:00",
		"2025-02-30T12:00:00Z",
		"2025-02-29T12:00:00Z",
		"2025-10-01T24:00:00Z",
		"2025-10-01T12:60:00Z",
		"1990-12-31T23:59:61Z",
		"1990-12-32T23:59:60Z",
		"2025-10-01T1:00:00Z",
		"2025-10-01T12:00:00,5Z",
		"2025-10-01T12:00:00.Z",
		"2025-10-01T12:00:00+24:00",
		"2025-10-01T12:00:00+00:60",
		"2025-10-01 12:00:00Z",
		"2025-10-01T12:00:00Z\n",
	} {
		t.Run(created, func(t *testing.T) {
			a := conforming(t)
			a.annotations[ocispec.AnnotationCreated] = created

			got := findings(t, a)
			require.Contains(t, got, "oci-annotations")
			require.Equal(t, report.Fail, got["oci-annotations"].Severity)
			require.Contains(t, got["oci-annotations"].Detail, ocispec.AnnotationCreated)
			require.Contains(t, got["oci-annotations"].Detail, "must be an RFC 3339 timestamp")
		})
	}
}

// VCS and base-image state remain the builder's knowledge, recorded in
// provenance, never the descriptor's.
func TestBuilderSourceAnnotationsMustNotBeEmitted(t *testing.T) {
	for _, key := range []string{
		"org.opencontainers.image.revision",
		"org.opencontainers.image.base.name",
		"org.opencontainers.image.base.digest",
	} {
		a := conforming(t)
		a.annotations[key] = "docker.io/library/debian:trixie"

		got := findings(t, a)
		require.Equal(t, report.Fail, got["oci-annotations"].Severity, key)
		require.Contains(t, got["oci-annotations"].Detail, "must not be emitted", key)
	}
}

// The OCI image-manifest schema requires a layer, and staging the kit's
// own sources is what guarantees one.
func TestAZeroLayerManifestFails(t *testing.T) {
	a := conforming(t)
	a.layers = nil

	require.Equal(t, report.Fail, findings(t, a)["at-least-one-layer"].Severity)
}

// A build has not assembled layers yet; the check is skipped there rather
// than reported as an empty manifest.
func TestLayerCountIsSkippedWhenTheSourceCannotSeeLayers(t *testing.T) {
	a := conforming(t)
	a.layers, a.layersKnown = nil, false

	require.Equal(t, report.Skip, findings(t, a)["at-least-one-layer"].Severity)
}

// An overlay's directory entries replace the base's, so the two home
// levels are judged from both directions: /home handed to the agent, and
// /home/agent taken from it.
func TestAnOverlayMustKeepTheHomeOwners(t *testing.T) {
	a := conforming(t)
	a.dirs = map[string]FileStat{
		"/home":       {Mode: 0o755, Uid: 1000, Gid: 1000},
		"/home/agent": {Mode: 0o755, Uid: 0, Gid: 0},
	}

	rep, err := Run(context.Background(), a)
	require.NoError(t, err)
	var details []string
	for _, f := range rep.Findings {
		if f.Check == "overlay-home-ownership" {
			require.Equal(t, report.Fail, f.Severity)
			details = append(details, f.Detail)
		}
	}
	require.Len(t, details, 2)
	require.Contains(t, details[0], "/home owned by uid 1000")
	require.Contains(t, details[1], "/home/agent owned by uid 0")
}

func TestAnOverlayOwningTheHomeLevelsCorrectlyReportsNothing(t *testing.T) {
	a := conforming(t)
	a.dirs = map[string]FileStat{
		"/home":       {Mode: 0o755},
		"/home/agent": {Mode: 0o755, Uid: 1000, Gid: 1000},
	}

	require.Empty(t, findings(t, a))
}

// A workload's layers are the whole root filesystem, not an overlay on
// someone else's home, so neither overlay check judges it.
func TestAWorkloadIsNotJudgedAsAnOverlay(t *testing.T) {
	a := sbxWorkload(t)
	a.dirs = map[string]FileStat{"/home/agent": {Mode: 0o755}}
	a.dangling = []Symlink{{Path: "/etc/mtab", Target: "/proc/mounts"}}

	require.Empty(t, findings(t, a))
}

// A link the overlay cannot resolve may still find its target on the
// base, so it is a warning, not a verdict.
func TestADanglingOverlaySymlinkWarns(t *testing.T) {
	a := conforming(t)
	a.dangling = []Symlink{{Path: "/usr/local/bin/tool", Target: "/root/.local/share/tool/bin/tool"}}

	rep, err := Run(context.Background(), a)
	require.NoError(t, err)
	require.False(t, rep.Failed())
	got := findings(t, a)["overlay-links-resolve"]
	require.Equal(t, report.Warn, got.Severity)
	require.Contains(t, got.Detail, "/usr/local/bin/tool links to /root/.local/share/tool/bin/tool")
}

func TestOverlayChecksAreSkippedBeforeLayersExist(t *testing.T) {
	a := conforming(t)
	a.layers, a.layersKnown = nil, false

	got := findings(t, a)
	require.Equal(t, report.Skip, got["overlay-home-ownership"].Severity)
	require.Equal(t, report.Skip, got["overlay-links-resolve"].Severity)
}

// The regression this suite exists for: staging silently not running.
func TestMissingStagedSourcesFail(t *testing.T) {
	a := conforming(t)
	a.files = map[string][]byte{}

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-sources"].Severity)
	require.Contains(t, got["staged-sources"].Detail, "self-describing")
}

// The annotation carries compact JSON and the staged file the expanded
// YAML, so agreement is about the declarations rather than the bytes.
func TestStagedDescriptorMustDescribeTheSameKit(t *testing.T) {
	a := conforming(t)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] =
		[]byte("schemaVersion: \"3\"\nkind: workload\n")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-sources"].Severity)
	require.Contains(t, got["staged-sources"].Detail, "different kits")
}

func TestADeclaredRecipeMustBeStaged(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: mixin\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\nbuild: |\n  FROM scratch\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-recipe"].Severity)
	require.Contains(t, got["staged-recipe"].Detail, stagedRecipeName)
}

// Publishing rewrites contentFile to the staged path; an authored path
// surviving into the published descriptor means the body never shipped.
func TestAgentContextMustPointAtAStagedFile(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n" +
		"  - type: com.docker.sandbox/agent-context@1\n    config:\n      contentFile: ./context.md\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.annotations[spec.AnnotationCapabilities] = spec.CapabilityTypes(d.Capabilities)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["agent-context-staged"].Severity)
	require.Contains(t, got["agent-context-staged"].Detail, "rewrites it")
}

// A workload runs under a bare docker run, minus what the descriptor
// declares; with no entrypoint or cmd there is nothing to degrade to.
func TestAWorkloadNeedsALaunchConfig(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: workload\nprovides: [\"demo@1.0.0\"]\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["workload-launch-config"].Severity)

	a.config.Config.Entrypoint = []string{"bash"}
	require.NotContains(t, findings(t, a), "workload-launch-config")
}

// Index annotations are an optimization a multi-node builder legitimately
// dissolves, and consumers must fall back to the platform manifest — so
// their absence is worth saying, not worth failing.
func TestMissingIndexAnnotationsWarnRatherThanFail(t *testing.T) {
	a := conforming(t)
	a.hasIndex, a.indexAnn = true, map[string]string{}

	rep, err := Run(context.Background(), a)
	require.NoError(t, err)
	require.False(t, rep.Failed())
	require.Equal(t, report.Warn, findings(t, a)["index-annotations"].Severity)
}

// Pointing at another kit's staged directory would make the body
// something this artifact does not carry, so being under the shared root
// is not enough.
func TestAgentContextMustSitBesideThisKitsSources(t *testing.T) {
	a := conforming(t)
	authored := "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n" +
		"  - type: com.docker.sandbox/agent-context@1\n    config:\n" +
		"      contentFile: /usr/share/sandbox/kit/other/context.md\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.annotations[spec.AnnotationCapabilities] = spec.CapabilityTypes(d.Capabilities)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)
	a.files["/usr/share/sandbox/kit/other/context.md"] = []byte("someone else's body")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["agent-context-staged"].Severity)
}

// An index that carries annotations must carry the same ones: a consumer
// reading it would otherwise judge a different kit than it runs.
func TestIndexAnnotationsMustAgreeWithTheManifest(t *testing.T) {
	a := conforming(t)
	a.hasIndex = true
	a.indexAnn = map[string]string{
		spec.AnnotationDescriptor:    `{"schemaVersion":"3","kind":"workload"}`,
		spec.AnnotationSchemaVersion: "3",
	}

	got := findings(t, a)
	require.Equal(t, report.Fail, got["index-annotations"].Severity)
	require.Contains(t, got["index-annotations"].Detail, "disagrees")
}

// Presence is meaning for promoted annotations: an index saying
// capabilities="" claims "none requested", which it must not invent when
// the manifest carries no such key at all.
func TestAnIndexCannotInventAPromotedAnnotation(t *testing.T) {
	a := conforming(t)
	require.NotContains(t, a.annotations, spec.AnnotationCapabilities,
		"the fixture must not request capabilities for this test to mean anything")
	a.hasIndex = true
	a.indexAnn = map[string]string{
		spec.AnnotationDescriptor:    a.annotations[spec.AnnotationDescriptor],
		spec.AnnotationSchemaVersion: a.annotations[spec.AnnotationSchemaVersion],
		spec.AnnotationCapabilities:  "",
	}

	got := findings(t, a)
	require.Equal(t, report.Fail, got["index-annotations"].Severity)
	require.Contains(t, got["index-annotations"].Detail, "manifest does not")
}

// An index repeating the manifest's annotations is the normal case.
func TestAgreeingIndexAnnotationsPass(t *testing.T) {
	a := conforming(t)
	a.hasIndex = true
	a.indexAnn = map[string]string{
		spec.AnnotationDescriptor:    a.annotations[spec.AnnotationDescriptor],
		spec.AnnotationSchemaVersion: a.annotations[spec.AnnotationSchemaVersion],
	}

	require.NotContains(t, findings(t, a), "index-annotations")
}

// Publishing emits no key for an empty field, so an annotation the
// descriptor cannot account for is as wrong as a mismatched one.
func TestAnUnaccountedOCIAnnotationFails(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.OCIAnnotationDescription] = "invented out of nowhere"

	got := findings(t, a)
	require.Equal(t, report.Fail, got["oci-annotations"].Severity)
	require.Contains(t, got["oci-annotations"].Detail, "is empty")
}

// Comparing a hand-picked subset would let the staged descriptor differ in
// everything unlisted, so agreement is over the whole document.
func TestStagedDescriptorMustAgreeInEveryField(t *testing.T) {
	a := conforming(t)
	// Same kind, provides, and capabilities; different display metadata.
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] =
		[]byte("schemaVersion: \"3\"\nkind: mixin\ndisplayName: Something Else\nprovides: [\"demo@1.0.0\"]\n")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["staged-sources"].Severity)
	require.Contains(t, got["staged-sources"].Detail, "different kits")
}

// mergedSet builds a conforming merged set: a kit whose descriptor
// records the kits it was merged from, carrying their staged sources
// beside its own.
func mergedSet(t *testing.T, kits ...string) *fake {
	t.Helper()
	authored := "schemaVersion: \"3\"\nkind: workload\ndisplayName: Demo Set\nversion: \"1.0.0\"\nprovides: [\"demo@1.0.0\""
	for _, name := range kits {
		authored += ", \"" + name + "@1.0.0\""
	}
	authored += "]\nkits:\n"
	for _, name := range kits {
		authored += "  - ref: reg.example.com/sbx-kit-" + name + ":1.0.0\n    digest: sha256:" +
			"1111111111111111111111111111111111111111111111111111111111111111\n"
	}
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	ann := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: "3",
	}
	for k, v := range spec.OCIAnnotations(d) {
		ann[k] = v
	}
	f := &fake{
		annotations: ann,
		config:      ocispec.Image{Config: ocispec.ImageConfig{Entrypoint: []string{"/bin/sh"}}},
		layers:      []ocispec.Descriptor{{Digest: "sha256:aaaa"}},
		layersKnown: true,
		files: map[string][]byte{
			path.Join(StagedKitRoot, stem, stagedDescriptorName): []byte(authored),
		},
	}
	// Each merged kit's own sources ride along, which is what the
	// staged-roots check counts, and each is reachable as an artifact
	// of its own, which is what the declaration check compares against.
	f.kits = map[string]Artifact{}
	for _, name := range kits {
		listed := "schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\nprovides: [\"" + name + "@1.0.0\"]\n"
		f.files[path.Join(StagedKitRoot, name, stagedDescriptorName)] = []byte(listed)
		ld, err := spec.Decode([]byte(listed))
		require.NoError(t, err)
		lp, err := json.Marshal(ld)
		require.NoError(t, err)
		f.kits["reg.example.com/sbx-kit-"+name+":1.0.0"] = &fake{
			annotations: map[string]string{spec.AnnotationDescriptor: string(lp)},
		}
	}
	return f
}

// restate rewrites one listed kit's published descriptor in both
// places a merged set carries it: the artifact the set pins by digest,
// and the sources staged into the merged filesystem. They are one
// document in reality, so a fixture that moved only one would be
// testing a kit that cannot exist.
func restate(t *testing.T, a *fake, name string, mutate func(*spec.Descriptor)) {
	t.Helper()
	listed := a.kits["reg.example.com/sbx-kit-"+name+":1.0.0"].(*fake)
	d, err := spec.Decode([]byte(listed.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	mutate(d)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	listed.annotations[spec.AnnotationDescriptor] = string(published)
	a.files[path.Join(StagedKitRoot, name, stagedDescriptorName)] = published
}

func TestAMergedSetConforms(t *testing.T) {
	rep, err := Run(context.Background(), mergedSet(t, "shell", "gh"))
	require.NoError(t, err)
	require.Empty(t, rep.Findings, "a conforming merged set must produce nothing to report")
}

// The staged sources of the kits a set lists are the only place a
// consumer can read their declarations without fetching them, so a
// merged kit that dropped them describes content nobody can inspect.
func TestAMergedSetMustCarryItsKitsStagedSources(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	delete(a.files, path.Join(StagedKitRoot, "gh", stagedDescriptorName))

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set"].Severity,
		"a warning would let the artifact pass: Report.Err counts only failures")
	require.Contains(t, got["merged-set"].Detail, "at least 3 staged roots are expected")
}

// A set's content is the kits it lists, so a recipe beside them
// describes content the artifact does not carry.
func TestAMergedSetMustNotAlsoDeclareARecipe(t *testing.T) {
	a := mergedSet(t, "shell")
	d, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	d.Build = "FROM scratch\n"
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set"].Severity)
	require.Contains(t, got["merged-set"].Detail, "content comes from exactly one of them")
}

// kind: set is an authoring kind the frontend resolves away; reaching a
// consumer means the merge never ran, so the layers are not what the
// descriptor describes.
func TestAPublishedSetKindIsRefused(t *testing.T) {
	a := mergedSet(t, "shell")
	raw := a.annotations[spec.AnnotationDescriptor]
	a.annotations[spec.AnnotationDescriptor] =
		strings.Replace(raw, `"kind":"workload"`, `"kind":"set"`, 1)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["descriptor-valid"].Severity)
	require.Contains(t, got["descriptor-valid"].Detail, "kind: set")
}

// Every listed kit is pinned: an authored entry names a version by tag,
// a published one records the manifest the build resolved.
func TestAPublishedSetPinsEveryKit(t *testing.T) {
	a := mergedSet(t, "shell")
	raw := a.annotations[spec.AnnotationDescriptor]
	a.annotations[spec.AnnotationDescriptor] =
		strings.Replace(raw, `,"digest":"sha256:`+strings.Repeat("1", 64)+`"`, "", 1)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["descriptor-valid"].Severity)
	require.Contains(t, got["descriptor-valid"].Detail, "with no digest")
}

// The merge is checkable against its inputs, not only against itself:
// each listed kit is fetched at the digest the set recorded, and what
// it offered has to appear in the merged descriptor.
func TestAMergedSetCarriesItsKitsProvides(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	raw := a.annotations[spec.AnnotationDescriptor]
	a.annotations[spec.AnnotationDescriptor] =
		strings.Replace(raw, `,"gh@1.0.0"`, "", 1)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "provides gh@1.0.0")
}

// A merged kit ships its kits' content, so it reports their terms.
func TestAMergedSetCarriesItsKitsLicenses(t *testing.T) {
	a := mergedSet(t, "shell")
	restate(t, a, "shell", func(d *spec.Descriptor) { d.Licenses = []string{"MIT"} })

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "licensed MIT")
}

// A requirement the set answers itself is gone from the merged
// descriptor — keeping it would publish a kit that can never resolve,
// since a kit cannot satisfy its own requirement.
func TestAMergedSetDropsRequirementsItAnswers(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	restate(t, a, "gh", func(d *spec.Descriptor) { d.Requires = []string{"shell >= 1.0.0"} })

	// The set answers it, and the merged descriptor does not restate
	// it: nothing to report.
	require.NotContains(t, findings(t, a), "merged-set-declarations")

	// Restating it is the failure.
	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Requires = []string{"shell >= 1.0.0"}
	setPublished, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(setPublished)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "can never resolve")
}

// Whether a dropped requirement was answered is not decidable from
// outside: the merged descriptor does not distinguish the set's own
// provides from its kits', so an entry the listed kits do not answer
// may still have been answered by the set itself. The check reports
// only the direction that is decidable — an entry the kits answer and
// the merge kept, which nothing can ever satisfy.
func TestTheDeclarationCheckDoesNotJudgeDroppedRequirements(t *testing.T) {
	a := mergedSet(t, "shell")
	restate(t, a, "shell", func(d *spec.Descriptor) { d.Requires = []string{"team >= 1.0.0"} })

	// The merged descriptor drops it, as it would when the set's own
	// provides answered it. Nothing to report.
	require.NotContains(t, findings(t, a), "merged-set-declarations")

	// Retaining one the listed kits answer is the decidable failure.
	b := mergedSet(t, "shell", "gh")
	restate(t, b, "gh", func(d *spec.Descriptor) { d.Requires = []string{"shell >= 1.0.0"} })
	setD, err := spec.Decode([]byte(b.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Requires = []string{"shell >= 1.0.0"}
	published, err := json.Marshal(setD)
	require.NoError(t, err)
	b.annotations[spec.AnnotationDescriptor] = string(published)

	got := findings(t, b)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "can never resolve")
}

// A source with no registry behind it — an OCI layout holds one
// artifact — cannot reach the kits a set lists, and says so rather
// than reporting the merge unchecked as correct.
func TestTheDeclarationCheckSkipsWhenTheKitsAreUnreachable(t *testing.T) {
	a := mergedSet(t, "shell")
	a.kits = nil

	got := findings(t, a)
	require.Equal(t, report.Skip, got["merged-set-declarations"].Severity)
	require.NoError(t, report.Report{Findings: []report.Finding{got["merged-set-declarations"]}}.Err(),
		"a skip is not a failure")
}

// A bare provide takes the version its consumption reference carries
// before the descriptor's own, so a set listing base:2.0.0 whose
// descriptor still says 1.0.0 merged base@2.0.0. Reading the
// descriptor alone would report that conforming set as wrong.
func TestTheDeclarationCheckUsesTheEffectiveVersion(t *testing.T) {
	a := mergedSet(t, "shell")
	// Offers a bare name, and a version its reference has outgrown.
	restate(t, a, "shell", func(d *spec.Descriptor) {
		d.Provides = []string{"shell"}
		d.Version = "0.9.0"
	})

	// The merged descriptor carries what the reference names.
	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Provides = []string{"demo@1.0.0", "shell@1.0.0"}
	setPublished, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(setPublished)

	require.NotContains(t, findings(t, a), "merged-set-declarations",
		"shell@1.0.0 comes from the tag, not the stale descriptor")
}

// §9.5 applies the internal-drop rule to integrates as it does to
// requires, so a set retaining one its kits answer is as unresolvable.
func TestTheDeclarationCheckCoversIntegrates(t *testing.T) {
	a := mergedSet(t, "shell", "gh")
	restate(t, a, "gh", func(d *spec.Descriptor) { d.Integrates = []string{"shell >= 1.0.0"} })

	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Integrates = []string{"shell >= 1.0.0"}
	setPublished, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(setPublished)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "still integrates")
}

// Counting roots proves nothing on its own: an artifact can stage as
// many unrelated roots as it lists kits and carry none of their
// sources. Each listed kit has to be findable among them.
func TestAMergedSetBindsEachKitToAStagedRoot(t *testing.T) {
	a := mergedSet(t, "shell", "gh")

	// Replace one kit's staged sources with an unrelated kit's. The
	// count is unchanged, so only the binding can catch it.
	a.files[path.Join(StagedKitRoot, "gh", stagedDescriptorName)] =
		[]byte("schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\nprovides: [\"decoy@1.0.0\"]\n")

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "sbx-kit-gh:1.0.0 is listed but its staged sources are not present")

	stems, err := a.StagedStems(context.Background())
	require.NoError(t, err)
	require.Len(t, stems, 3, "and the count alone would still have passed")
}

// A re-export is checkable from the artifact alone — the merged
// descriptor keeps kits[].args and the set's own args — and what the
// rule protects is the installer, who sees only the set's declaration.
func TestTheDeclarationCheckJudgesReExports(t *testing.T) {
	a := mergedSet(t, "shell")
	restate(t, a, "shell", func(d *spec.Descriptor) {
		d.Args = map[string]spec.Arg{"mode": {Enum: []string{"fast", "slow"}, Required: true}}
	})

	// The set re-exports it through an arg that keeps the requirement
	// but drops the enum, so the enum is the only thing missing.
	setD, err := spec.Decode([]byte(a.annotations[spec.AnnotationDescriptor]))
	require.NoError(t, err)
	setD.Args = map[string]spec.Arg{"mode": {Required: true}}
	setD.Kits[0].Args = map[string]string{"mode": "${{ kit.args.mode }}"}
	published, err := json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)

	got := findings(t, a)
	require.Equal(t, report.Fail, got["merged-set-declarations"].Severity)
	require.Contains(t, got["merged-set-declarations"].Detail, "does not restate its enum")

	// Restating the contract is what makes it pass.
	setD.Args = map[string]spec.Arg{"mode": {Enum: []string{"fast", "slow"}, Required: true}}
	published, err = json.Marshal(setD)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	require.NotContains(t, findings(t, a), "merged-set-declarations")
}

// sbxWorkload is a workload declaring the platform capability whose
// filesystem satisfies the floor, so each test below can remove exactly
// one thing and see that removal reported.
func sbxWorkload(t *testing.T) *fake {
	t.Helper()
	authored := "schemaVersion: \"3\"\nkind: workload\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\n" +
		"capabilities:\n  - type: com.docker.sandbox/sbx@1\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	ann := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: "3",
	}
	for k, v := range spec.OCIAnnotations(d) {
		ann[k] = v
	}
	// The index annotation is derived, and the annotation check compares
	// it against the descriptor; a fake that omitted it would fail on
	// that rather than on the floor.
	ann[spec.AnnotationCapabilities] = spec.CapabilityTypes(d.Capabilities)
	a := &fake{
		annotations: ann,
		layers:      []ocispec.Descriptor{{Digest: "sha256:aaaa"}},
		layersKnown: true,
		files: map[string][]byte{
			path.Join(StagedKitRoot, stem, stagedDescriptorName): []byte(authored),
			"/bin/sh":                    []byte("elf"),
			"/bin/bash":                  []byte("elf"),
			"/etc/passwd":                []byte("root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000::/home/agent:/bin/bash\n"),
			"/etc/sandbox-persistent.sh": []byte(""),
		},
	}
	a.config.Config.User = "agent"
	a.config.Config.Env = []string{"BASH_ENV=/etc/sandbox-persistent.sh"}
	a.config.Config.Entrypoint = []string{"/usr/local/bin/agent"}
	return a
}

func TestASbxWorkloadSatisfyingTheFloorReportsNothing(t *testing.T) {
	require.Empty(t, findings(t, sbxWorkload(t)))
}

// dpkgStatus is what a status file looks like for the packages a test
// names, at the raw versions a distribution would have written.
func dpkgStatus(packages map[string]string) []byte {
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "Package: %s\nStatus: install ok installed\nVersion: %s\n\n", name, packages[name])
	}
	return []byte(b.String())
}

// derivedWorkload is a workload whose descriptor carries §9.6 entries and
// whose filesystem holds the database they were read from, so each test
// can put exactly one of the two out of step.
func derivedWorkload(t *testing.T, provides []string, installed map[string]string) *fake {
	t.Helper()
	authored := "schemaVersion: \"3\"\nkind: workload\ndisplayName: Shell\nversion: \"1.0.0\"\n" +
		"provides: [" + strings.Join(provides, ", ") + "]\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	ann := map[string]string{
		spec.AnnotationDescriptor:    string(published),
		spec.AnnotationSchemaVersion: "3",
	}
	for k, v := range spec.OCIAnnotations(d) {
		ann[k] = v
	}
	a := &fake{
		annotations: ann,
		layers:      []ocispec.Descriptor{{Digest: "sha256:aaaa"}},
		layersKnown: true,
		files: map[string][]byte{
			path.Join(StagedKitRoot, stem, stagedDescriptorName): []byte(authored),
			spec.DpkgStatusPath: dpkgStatus(installed),
		},
	}
	a.config.Config.Entrypoint = []string{"bash"}
	return a
}

func TestDerivedProvidesMatchingTheDatabaseReportsNothing(t *testing.T) {
	a := derivedWorkload(t,
		[]string{`"deb/bash@5.2.37"`, `"deb/libstdc++6@14.2.0"`},
		map[string]string{"bash": "5.2.37-2+dhi1", "libstdc++6": "14.2.0-19+dhi0"})

	require.NotContains(t, findings(t, a), "derived-provides")
}

// A package the database does not record as installed means the entry
// came from somewhere other than the filesystem it claims to describe.
func TestADerivedProvideNeedsAnInstalledPackage(t *testing.T) {
	a := derivedWorkload(t,
		[]string{`"deb/bash@5.2.37"`, `"deb/ghost@1.0.0"`},
		map[string]string{"bash": "5.2.37-2+dhi1"})

	got := findings(t, a)["derived-provides"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "deb/ghost@1.0.0")
	require.Contains(t, got.Detail, "records as installed")
}

// The published version is the upstream core, so an entry carrying the
// distribution's own decoration is wrong even though it is the truth.
func TestADerivedProvideCarriesTheUpstreamCore(t *testing.T) {
	a := derivedWorkload(t,
		[]string{`"deb/openssl@3.5.7-1"`},
		map[string]string{"openssl": "3.5.7-1~deb13u2+dhi1"})

	got := findings(t, a)["derived-provides"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "3.5.7")
}

// The entries claim a database the image does not have, so there is
// nothing backing them at all.
func TestDerivedProvidesNeedTheDatabaseTheyNameToBePresent(t *testing.T) {
	a := derivedWorkload(t, []string{`"deb/bash@5.2.37"`}, nil)
	delete(a.files, spec.DpkgStatusPath)

	got := findings(t, a)["derived-provides"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, spec.DpkgStatusPath)
}

// A mixin's layers are a delta, so a package database in one is whatever
// its recipe rewrote rather than an inventory of anything.
func TestDerivedProvidesAreWorkloadOnly(t *testing.T) {
	authored := "schemaVersion: \"3\"\nkind: mixin\ndisplayName: Demo\nversion: \"1.0.0\"\n" +
		"provides: [\"deb/bash@5.2.37\"]\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	a := conforming(t)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	for k, v := range spec.OCIAnnotations(d) {
		a.annotations[k] = v
	}
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)
	a.files[spec.DpkgStatusPath] = dpkgStatus(map[string]string{"bash": "5.2.37-2+dhi1"})

	got := findings(t, a)["derived-provides"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "only a workload")
}

// §9.6 states an entry only where every platform agreed, so a package
// this platform carries and the descriptor omits is not a finding — the
// check reads one direction and only one.
func TestADatabasePackageWithNoEntryIsNotAFinding(t *testing.T) {
	a := derivedWorkload(t,
		[]string{`"deb/bash@5.2.37"`},
		map[string]string{"bash": "5.2.37-2+dhi1", "amd64-only": "1.0.0-1"})

	require.NotContains(t, findings(t, a), "derived-provides")
}

// A multiarch database names one package once per architecture, and §9.6
// states an entry only where they agree. Keeping the last version read
// would let an entry that disagrees with an earlier stanza pass on the
// strength of which one happened to come last.
func TestEveryRecordForANameHasToAgreeWithTheEntry(t *testing.T) {
	a := derivedWorkload(t, []string{`"deb/libc6@2.42"`}, nil)
	// Hand-built: the helper's map cannot hold one name twice, which is
	// exactly the shape being tested.
	a.files[spec.DpkgStatusPath] = []byte(
		"Package: libc6\nStatus: install ok installed\nVersion: 2.41-12+dhi1\n\n" +
			"Package: libc6\nStatus: install ok installed\nVersion: 2.42-1\n")

	got := findings(t, a)["derived-provides"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "2.41",
		"the stanza that disagrees is the finding, wherever it sits in the file")
}

// A merged set carries its kits' entries through the provides union, so
// those were read before anything composed onto the workload. Judging
// them against the merged database would report a mixin that upgraded a
// package as the set's violation.
func TestAMergedSetsDerivedEntriesAreNotJudgedHere(t *testing.T) {
	authored := "schemaVersion: \"3\"\nkind: workload\ndisplayName: Set\nversion: \"1.0.0\"\n" +
		"provides: [\"deb/bash@5.2.37\"]\n" +
		"kits:\n  - ref: reg.io/sbx-kit-shell:1.0.0\n    digest: sha256:" + strings.Repeat("a", 64) + "\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	a := derivedWorkload(t, []string{`"deb/bash@5.2.37"`}, map[string]string{"bash": "9.9.9-1"})
	a.annotations[spec.AnnotationDescriptor] = string(published)
	for k, v := range spec.OCIAnnotations(d) {
		a.annotations[k] = v
	}
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)["derived-provides"]
	require.Equal(t, report.Skip, got.Severity,
		"the merged database is not the evidence these entries came from")
	require.Contains(t, got.Detail, "before composition")
}

// A set of mixins merges to kind: mixin, and the union brings a
// nonconforming member's derived entries along with it. That is a
// violation whichever filesystem is available, so the kind is judged
// before the set skip — otherwise nothing refuses the result, since
// ValidatePublished accepts the namespaces by design.
func TestAMergedMixinSetStillFailsTheWorkloadOnlyRule(t *testing.T) {
	authored := "schemaVersion: \"3\"\nkind: mixin\ndisplayName: Set\nversion: \"1.0.0\"\n" +
		"provides: [\"deb/bash@5.2.37\"]\n" +
		"kits:\n  - ref: reg.io/sbx-kit-tool:1.0.0\n    digest: sha256:" + strings.Repeat("a", 64) + "\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	a := derivedWorkload(t, []string{`"deb/bash@5.2.37"`}, map[string]string{"bash": "5.2.37-2+dhi1"})
	a.annotations[spec.AnnotationDescriptor] = string(published)
	for k, v := range spec.OCIAnnotations(d) {
		a.annotations[k] = v
	}
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)["derived-provides"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "only a workload")
}

// Without the capability the floor is not this kit's promise, so the same
// gaps must go unreported rather than being imposed on every workload.
func TestTheFloorIsJudgedOnlyWhenTheCapabilityIsDeclared(t *testing.T) {
	a := sbxWorkload(t)
	authored := "schemaVersion: \"3\"\nkind: workload\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	delete(a.annotations, spec.AnnotationCapabilities)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)
	delete(a.files, "/bin/bash")
	a.config.Config.User = ""

	require.Empty(t, findings(t, a))
}

func TestTheFloorNeedsBothShells(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "/bin/bash"} {
		t.Run(shell, func(t *testing.T) {
			a := sbxWorkload(t)
			delete(a.files, shell)

			got := findings(t, a)["sbx-platform-floor"]
			require.Equal(t, report.Fail, got.Severity)
			require.Contains(t, got.Detail, shell)
		})
	}
}

// An image that names no user leaves the host nothing to honor, which is
// exactly the assumption this capability exists to remove.
func TestTheFloorNeedsTheImageToDeclareAUser(t *testing.T) {
	a := sbxWorkload(t)
	a.config.Config.User = ""

	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "declares no user")
}

// An image stating no identity is still judged for its shells: they are
// their own MUSTs, and a run that stopped at the user would hand back
// the rest one rebuild at a time.
func TestTheFloorStillJudgesTheShellsWithoutAUser(t *testing.T) {
	a := sbxWorkload(t)
	a.config.Config.User = ""
	delete(a.files, "/bin/sh")
	delete(a.files, "/bin/bash")

	rep, err := Run(context.Background(), a)
	require.NoError(t, err)
	var details []string
	for _, f := range rep.Findings {
		if f.Check == "sbx-platform-floor" {
			require.Equal(t, report.Fail, f.Severity)
			details = append(details, f.Detail)
		}
	}
	require.Len(t, details, 3)
	require.Contains(t, details[0], "declares no user")
	require.Contains(t, details[1], "/bin/sh is missing")
	require.Contains(t, details[2], "/bin/bash is missing")
}

// Both spellings have to resolve: the host reads whichever half the image
// did not state out of passwd, before the container exists.
func TestTheFloorResolvesTheUserByNameOrUid(t *testing.T) {
	for _, user := range []string{"agent", "1000", "1000:1000"} {
		t.Run(user, func(t *testing.T) {
			a := sbxWorkload(t)
			a.config.Config.User = user
			require.Empty(t, findings(t, a))
		})
	}

	a := sbxWorkload(t)
	a.config.Config.User = "nobody-here"
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")
}

// A runtime reads a numeric spelling as a uid, so a row merely named
// "1000" is not the uid the image asked for.
func TestANumericUserResolvesByUidNotName(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/passwd"] = []byte("1000:x:2000:2000::/home/odd:/bin/bash\n")
	a.config.Config.User = "1000"
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)

	a = sbxWorkload(t)
	a.files["/etc/passwd"] = []byte("odd:x:1000:1000::/home/odd:/bin/bash\n")
	a.config.Config.User = "1000"
	require.Empty(t, findings(t, a))
}

// An explicit group overrides the passwd primary, so it is the gid the
// host would honor and the one that has to resolve.
func TestAGroupSuffixHasToResolve(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/group"] = []byte("agent:x:1000:\nbuild:x:2000:\n")
	a.config.Config.User = "agent:build"
	require.Empty(t, findings(t, a))

	a = sbxWorkload(t)
	a.files["/etc/group"] = []byte("agent:x:1000:\n")
	a.config.Config.User = "agent:no-such-group"
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")
}

// A row the host cannot read a uid, gid and home out of has not resolved
// anything, however well its name matches.
func TestAMalformedPasswdRowDoesNotResolve(t *testing.T) {
	for _, row := range []string{
		"agent:x:notanumber:1000::/home/agent:/bin/bash",
		"agent:x:1000:notanumber::/home/agent:/bin/bash",
		"agent:x:1000:1000::relative/home:/bin/bash",
		"agent:x:1000:1000:::/bin/bash",
		":x:1000:1000::/home/agent:/bin/bash",
	} {
		t.Run(row, func(t *testing.T) {
			a := sbxWorkload(t)
			a.files["/etc/passwd"] = []byte(row + "\n")
			got := findings(t, a)["sbx-platform-floor"]
			require.Equal(t, report.Fail, got.Severity)
		})
	}
}

// Occupying the path is not being a shell: a placeholder resolves and
// then fails at the first hook.
func TestTheFloorNeedsTheShellsToBeExecutable(t *testing.T) {
	a := sbxWorkload(t)
	a.stats = map[string]FileStat{"/bin/bash": {Mode: 0o644, Regular: true}}
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "not executable")
}

// Which bit applies depends on who the image says will run it: bits that
// leave the declared user out are no more usable than none at all.
func TestTheFloorJudgesTheShellBitsAgainstTheDeclaredUser(t *testing.T) {
	// Root-owned and root-only: the fixture's user is neither.
	a := sbxWorkload(t)
	a.stats = map[string]FileStat{"/bin/bash": {Mode: 0o100, Regular: true}}
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "not executable by agent")

	// The same bits, reached through the group the user belongs to.
	a = sbxWorkload(t)
	a.stats = map[string]FileStat{"/bin/bash": {Mode: 0o010, Gid: 1000, Regular: true}}
	require.Empty(t, findings(t, a))

	// Root bypasses the bits entirely.
	a = sbxWorkload(t)
	a.config.Config.User = "root"
	a.files["/etc/passwd"] = []byte("root:x:0:0:root:/root:/bin/bash\n")
	a.stats = map[string]FileStat{"/bin/bash": {Mode: 0o100, Regular: true}}
	require.Empty(t, findings(t, a))
}

// execve runs ordinary files: a FIFO, socket, or device node occupies the
// path with every bit set and runs none of them.
func TestTheFloorNeedsTheShellsToBeOrdinaryFiles(t *testing.T) {
	a := sbxWorkload(t)
	a.stats = map[string]FileStat{"/bin/sh": {Mode: 0o777}}
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "/bin/sh is not executable")
}

// uid_t is 32-bit unsigned and its top value is the "leave this one
// alone" sentinel, so neither names an identity a host can hold.
func TestAnOutOfRangeIdDoesNotResolve(t *testing.T) {
	for _, uid := range []string{"99999999999999999999", "4294967295"} {
		t.Run(uid, func(t *testing.T) {
			a := sbxWorkload(t)
			a.files["/etc/passwd"] = []byte("agent:x:" + uid + ":1000::/home/agent:/bin/bash\n")
			got := findings(t, a)["sbx-platform-floor"]
			require.Equal(t, report.Fail, got.Severity)
			require.Contains(t, got.Detail, "does not resolve")
		})
	}
}

// A resolver passes over a malformed record and keeps reading, so one
// cannot hide the account that follows it.
func TestAMalformedRecordDoesNotHideALaterOne(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/passwd"] = []byte(
		"agent:x:notanumber:1000::/home/agent:/bin/bash\n" +
			"agent:x:1000:1000::/home/agent:/bin/bash\n")
	require.Empty(t, findings(t, a))

	a = sbxWorkload(t)
	a.files["/etc/group"] = []byte("build:x:notanumber:\nbuild:x:2000:\n")
	a.config.Config.User = "agent:build"
	require.Empty(t, findings(t, a))
}

// An empty group suffix overrides nothing: the passwd primary applies,
// as it does for a user named without one — including the decision not
// to read a group file at all.
func TestAnEmptyGroupSuffixIsNoOverride(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/group"] = nil
	a.config.Config.User = "agent:"
	require.Empty(t, findings(t, a))

	a = sbxWorkload(t)
	a.readErrs = map[string]error{
		"/etc/group": fmt.Errorf("exceeds bytes: %w", assemble.ErrFileTooLarge),
	}
	a.config.Config.User = "agent:"
	require.Empty(t, findings(t, a))
}

// A record is its full shape: a passwd line is seven fields and a group
// line four, and a resolver skips what is short of that.
func TestAShortRecordIsNotAnAccount(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/passwd"] = []byte("agent:x:1000:1000::/home/agent\n")
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")

	a = sbxWorkload(t)
	a.files["/etc/group"] = []byte("build:x:2000\n")
	a.config.Config.User = "agent:build"
	got = findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")
}

// Whitespace inside a record belongs to the field: " agent" is not the
// agent a host looks for.
func TestARecordsFieldsAreLiteral(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/passwd"] = []byte(" agent:x:1000:1000::/home/agent:/bin/bash\n")
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")

	a = sbxWorkload(t)
	a.files["/etc/group"] = []byte(" build:x:2000:\n")
	a.config.Config.User = "agent:build"
	got = findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")

	// A CRLF database still resolves: the terminator is not a field.
	a = sbxWorkload(t)
	a.files["/etc/passwd"] = []byte("agent:x:1000:1000::/home/agent:/bin/bash\r\n")
	require.Empty(t, findings(t, a))
}

// A commented record is not an account, however exactly it spells the
// identity being looked for.
func TestACommentedPasswdRecordIsNotAnAccount(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/passwd"] = []byte("#agent:x:1000:1000::/home/agent:/bin/bash\n")
	a.config.Config.User = "1000"
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")

	a = sbxWorkload(t)
	a.files["/etc/group"] = []byte("#build:x:2000:\n")
	a.config.Config.User = "agent:build"
	got = findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")
}

// A numeric spelling is an id, usable or not: it never falls back to
// being a login name.
func TestAnOutOfRangeNumericIsNotALoginName(t *testing.T) {
	a := sbxWorkload(t)
	a.files["/etc/passwd"] = []byte("4294967295:x:1000:1000::/home/agent:/bin/bash\n")
	a.config.Config.User = "4294967295"
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")

	a = sbxWorkload(t)
	a.files["/etc/group"] = []byte("4294967295:x:2000:\n")
	a.config.Config.User = "agent:4294967295"
	got = findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")
}

// A runtime resolves a numeric user by value, so a padded spelling names
// the same identity as the row it matches.
func TestAPaddedNumericUserResolves(t *testing.T) {
	a := sbxWorkload(t)
	a.config.Config.User = "001000"
	require.Empty(t, findings(t, a))
}

// A database no source will buffer leaves the identity unjudged, which
// is not the same as an image that declared it wrongly.
func TestAnUnreadableAccountFileIsAWarning(t *testing.T) {
	tooLarge := fmt.Errorf("exceeds bytes: %w", assemble.ErrFileTooLarge)

	a := sbxWorkload(t)
	a.readErrs = map[string]error{"/etc/passwd": tooLarge}
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Warn, got.Severity)
	require.Contains(t, got.Detail, "could not be resolved here")

	// The shells are their own MUSTs, and an unjudgeable identity says
	// nothing about whether they are there.
	a = sbxWorkload(t)
	a.readErrs = map[string]error{"/etc/passwd": tooLarge}
	delete(a.files, "/bin/bash")
	got = findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "/bin/bash is missing")

	// A group file is only read for a named group, so an unreadable one
	// cannot spoil an identity that names none.
	a = sbxWorkload(t)
	a.readErrs = map[string]error{"/etc/group": tooLarge}
	require.Empty(t, findings(t, a))

	a = sbxWorkload(t)
	a.readErrs = map[string]error{"/etc/group": tooLarge}
	a.config.Config.User = "agent:agent"
	got = findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Warn, got.Severity)
	require.Contains(t, got.Detail, "/etc/group is larger")
}

// The host resolves the literal value, so a stray space is a user that
// does not exist rather than the one it resembles.
func TestTheFloorResolvesTheUserTheImageLiterallyDeclares(t *testing.T) {
	a := sbxWorkload(t)
	a.config.Config.User = " agent"
	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "does not resolve")

	a = sbxWorkload(t)
	a.config.Config.User = "   "
	got = findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "declares no user")
}

// Bash resolves a relative BASH_ENV from wherever the agent runs, which
// is not the artifact root the check would otherwise look in.
func TestARelativeBashEnvIsWarned(t *testing.T) {
	a := sbxWorkload(t)
	a.config.Config.Env = []string{"BASH_ENV=.sandbox-persistent.sh"}
	got := findings(t, a)["sbx-persistent-env"]
	require.Equal(t, report.Warn, got.Severity)
	require.Contains(t, got.Detail, "absolute path")
}

// A mixin's image config never becomes the composed image's, so the
// declaration would describe an identity no host reads.
func TestTheFloorIsWorkloadOnly(t *testing.T) {
	authored := "schemaVersion: \"3\"\nkind: mixin\ndisplayName: Demo\nprovides: [\"demo@1.0.0\"]\n" +
		"capabilities:\n  - type: com.docker.sandbox/sbx@1\n"
	d, err := spec.Decode([]byte(authored))
	require.NoError(t, err)
	published, err := json.Marshal(d)
	require.NoError(t, err)

	a := sbxWorkload(t)
	a.annotations[spec.AnnotationDescriptor] = string(published)
	a.files[path.Join(StagedKitRoot, stem, stagedDescriptorName)] = []byte(authored)

	got := findings(t, a)["sbx-platform-floor"]
	require.Equal(t, report.Fail, got.Severity)
	require.Contains(t, got.Detail, "only a workload")
}

// SHOULD, so a missing persistent-environment file is a warning: the kit
// still runs, just without whatever the sandbox would have added.
func TestAMissingPersistentEnvIsAWarning(t *testing.T) {
	a := sbxWorkload(t)
	a.config.Config.Env = nil
	require.Equal(t, report.Warn, findings(t, a)["sbx-persistent-env"].Severity)

	a = sbxWorkload(t)
	delete(a.files, "/etc/sandbox-persistent.sh")
	got := findings(t, a)["sbx-persistent-env"]
	require.Equal(t, report.Warn, got.Severity)
	require.Contains(t, got.Detail, "does not ship")
}

func TestDescriptorValidationFindingsIncludeSourceExcerpts(t *testing.T) {
	a := conforming(t)
	raw := "schemaVersion: \"3\"\nkind: mixin\niconUrl: http://example.com\nargs:\n  version:\n    pattern: '['\n"
	a.annotations[spec.AnnotationDescriptor] = raw
	rep, err := Run(t.Context(), a)
	require.NoError(t, err)
	var finding report.Finding
	for _, f := range rep.Findings {
		if f.Check == "descriptor-valid" {
			finding = f
		}
	}
	require.Equal(t, "descriptor-valid", finding.Check)
	require.Contains(t, finding.Detail, spec.AnnotationDescriptor+":3:10: iconUrl:")
	require.Contains(t, finding.Detail, spec.AnnotationDescriptor+":6:14: args.version.pattern:")
	require.Contains(t, finding.Detail, "\n3 | iconUrl: http://example.com\n  |          ^")

	var rendered strings.Builder
	require.NoError(t, rep.Render(&rendered, report.Options{Width: 60}))
	require.Contains(t, rendered.String(), "3 | iconUrl: http://example.com\n        |          ^")
	require.Contains(t, rendered.String(), "6 |     pattern: '['\n        |              ^")
}

func TestListedKitValidationFindingsNameItsSource(t *testing.T) {
	a := mergedSet(t, "shell")
	ref := "reg.example.com/sbx-kit-shell:1.0.0"
	listed := a.kits[ref].(*fake)
	listed.annotations[spec.AnnotationDescriptor] = "schemaVersion: \"3\"\nkind: mixin\niconUrl: http://example.com\n"
	finding := findings(t, a)["merged-set-declarations"]
	require.Equal(t, report.Fail, finding.Severity)
	require.Contains(t, finding.Detail, ref+" (published descriptor):3:10: iconUrl:")
	require.Contains(t, finding.Detail, "\n3 | iconUrl: http://example.com\n  |          ^")
}
