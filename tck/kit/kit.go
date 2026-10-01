package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// StagedKitRoot is where every kit stages its own sources.
const StagedKitRoot = "/usr/share/sandbox/kit"

const (
	stagedDescriptorName = "kit.yaml"
	stagedRecipeName     = "kit.dockerfile"
)

// Artifact is the kit under test, however it is reached: a registry, an
// OCI layout, or the filesystem a build is about to export. Whatever a
// source cannot see it reports as unavailable rather than as absent, so a
// check is skipped where it cannot apply instead of failing there.
type Artifact interface {
	// Annotations are the image manifest's, or the ones a build is about
	// to set.
	Annotations() map[string]string

	// Config is the image config the artifact runs under.
	Config(context.Context) (*ocispec.Image, error)

	// Layers describes the manifest's layers. A build has not assembled
	// them yet and reports false.
	Layers(context.Context) ([]ocispec.Descriptor, bool, error)

	// ReadFile returns a path from the composed filesystem, reporting
	// false when nothing is there.
	ReadFile(context.Context, string) ([]byte, bool, error)

	// StagedStems lists the kit roots under StagedKitRoot.
	StagedStems(context.Context) ([]string, error)

	// IndexAnnotations are the annotations of the index the manifest hangs
	// under, when the artifact has one.
	IndexAnnotations() (map[string]string, bool)
}

// check is one conformance rule.
type check struct {
	name        string
	requirement string
	run         func(context.Context, *state) []report.Finding
}

// state is what the checks share: the artifact plus the descriptor every
// one of them starts from, decoded once.
type state struct {
	artifact   Artifact
	descriptor *spec.Descriptor
	stem       string
}

// Run judges an artifact against every check.
func Run(ctx context.Context, a Artifact) (report.Report, error) {
	// Nothing else can be judged without a descriptor, so this one
	// finding is the whole report — recorded as the check it is, since
	// a report has to say what ran as well as what it found.
	raw := a.Annotations()[spec.AnnotationDescriptor]
	if raw == "" {
		var rep report.Report
		rep.Add("descriptor-annotation", "SPEC-v3 §9.3",
			report.Failf("manifest carries no %s annotation; this is not a kit", spec.AnnotationDescriptor))
		return rep, nil
	}
	d, err := spec.Decode([]byte(raw))
	if err != nil {
		var rep report.Report
		rep.Add("descriptor-annotation", "SPEC-v3 §9.3",
			report.Failf("descriptor annotation does not decode: %v", spec.WithSource(err, spec.AnnotationDescriptor, []byte(raw))))
		return rep, nil
	}

	s := &state{artifact: a, descriptor: d}
	stems, err := a.StagedStems(ctx)
	if err != nil {
		return report.Report{}, fmt.Errorf("list staged kit roots: %w", err)
	}
	s.stem = ownStem(ctx, a, d, stems)

	var rep report.Report
	for _, c := range checks {
		rep.Add(c.name, c.requirement, c.run(ctx, s)...)
	}
	return rep, nil
}

// ownStem picks the staged root holding THIS kit's sources.
//
// One root is the ordinary case and needs no judgment. Several mean the
// artifact carries other kits' sources too — a kit built FROM a kit, or
// a merged set, whose layers include the staged sources of every kit it
// merged — and
// then the kit's own root is the one whose staged descriptor is the
// descriptor the annotation carries. That is the same identity the
// staged-sources check goes on to assert, so a kit with no matching root
// reports an empty stem and fails there rather than being judged against
// a root belonging to something else.
func ownStem(ctx context.Context, a Artifact, d *spec.Descriptor, stems []string) string {
	if len(stems) == 1 {
		return stems[0]
	}
	for _, stem := range stems {
		raw, ok, err := a.ReadFile(ctx, path.Join(StagedKitRoot, stem, stagedDescriptorName))
		if err != nil || !ok {
			continue
		}
		staged, err := spec.Decode(raw)
		if err != nil {
			continue
		}
		if descriptorsAgree(staged, d) {
			return stem
		}
	}
	return ""
}

func fail(format string, args ...any) []report.Finding {
	return []report.Finding{report.Failf(format, args...)}
}

func skip(format string, args ...any) []report.Finding {
	return []report.Finding{report.Skipf(format, args...)}
}

func warn(format string, args ...any) []report.Finding {
	return []report.Finding{report.Warnf(format, args...)}
}

// passwdEntry is what a host reads out of the image to learn the identity
// it must honor: the uid it execs as, the gid it chowns to, the login name
// commands run under, and the home its file writes work from.
type passwdEntry struct {
	name, home string
	uid, gid   int64
}

// allDigits reports that a spelling is numeric, which is what decides
// whether a runtime reads it as an id at all — separately from whether
// the value is one a host can hold.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseID reads a uid or gid the way a runtime must be able to hold one:
// uid_t and gid_t are 32-bit unsigned, and the top value is the
// "leave this one alone" sentinel rather than an identity, so neither it
// nor anything above it names a user a host can honor.
func parseID(s string) (int64, bool) {
	if !allDigits(s) {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id >= math.MaxUint32 {
		return 0, false
	}
	return id, true
}

// explicitGroup is the group half of an image config user, when it
// overrides the passwd primary. "agent:" overrides nothing, so the two
// places that care — resolution, and deciding whether /etc/group is
// worth reading at all — ask the same question here.
func explicitGroup(user string) (string, bool) {
	_, group, ok := strings.Cut(user, ":")
	return group, ok && group != ""
}

// lookupPasswd resolves an image config user — a name, a uid, or either
// with a group suffix — against /etc/passwd and /etc/group content. Both
// spellings have to resolve, because the host needs the half the image
// did not state.
func lookupPasswd(passwd, group, user string) (passwdEntry, bool) {
	want, _, _ := strings.Cut(user, ":")
	wantGroup, hasGroup := explicitGroup(user)
	// A runtime reads a numeric spelling as a uid, so resolution has to
	// as well: a row merely named "1000" is not uid 1000, and 001000 is.
	// A numeric spelling the host cannot hold is not a login name to
	// fall back on either — it resolves to nothing.
	numeric := allDigits(want)
	wantID, inRange := parseID(want)
	if numeric && !inRange {
		return passwdEntry{}, false
	}
	for _, line := range strings.Split(passwd, "\n") {
		// Only the line terminator comes off: whitespace inside a record
		// is part of the field, and normalizing it would resolve " agent"
		// as the agent the host will look for and not find.
		line = strings.TrimSuffix(line, "\r")
		// A commented record is not an account: resolvers skip these, so
		// matching one would certify an identity the host cannot find.
		if strings.HasPrefix(line, "#") {
			continue
		}
		// A passwd record is seven fields. A short line is malformed, and
		// a resolver reads past it rather than making an account out of
		// what it can see.
		fields := strings.Split(line, ":")
		if len(fields) != 7 {
			continue
		}
		if numeric {
			// By value, not by text: a row's uid field and the image's
			// spelling can differ and still be the same identity.
			if rowID, ok := parseID(fields[2]); !ok || rowID != wantID {
				continue
			}
		} else if fields[0] != want {
			continue
		}
		// Matching is not resolving: a row with no login name, whose uid
		// or gid is not an id a host can hold, or whose home is not an
		// absolute path, leaves the host without the values this
		// capability promises it can read. Skipped rather than fatal,
		// because a resolver passes over a malformed record and a later
		// one may be the account.
		uid, uidOK := parseID(fields[2])
		gid, gidOK := parseID(fields[3])
		if fields[0] == "" || !uidOK || !gidOK || !strings.HasPrefix(fields[5], "/") {
			continue
		}
		e := passwdEntry{name: fields[0], home: fields[5], uid: uid, gid: gid}
		if !hasGroup {
			return e, true
		}
		// An explicit group overrides the passwd primary, so it is the
		// gid the host would honor and the one that has to resolve.
		gid, ok := lookupGroup(group, wantGroup)
		if !ok {
			return passwdEntry{}, false
		}
		e.gid = gid
		return e, true
	}
	return passwdEntry{}, false
}

// lookupGroup resolves the group half of an image config user to a gid.
func lookupGroup(groupFile, want string) (int64, bool) {
	if allDigits(want) {
		// Numeric, so it is a gid whether or not it is a usable one; a
		// group merely named "4294967295" is not it.
		return parseID(want)
	}
	for _, line := range strings.Split(groupFile, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "#") {
			continue
		}
		// A group record is four fields, the last being the member list.
		fields := strings.Split(line, ":")
		if len(fields) != 4 || fields[0] != want {
			continue
		}
		if gid, ok := parseID(fields[2]); ok {
			return gid, true
		}
	}
	return 0, false
}

// resolveImageUser reads the identity the image declares out of its
// account databases. An unreadable database leaves the identity unjudged
// — a warning, since the image may be fine and the checker simply cannot
// see — while a readable one that does not resolve is the image's fault.
func resolveImageUser(ctx context.Context, a Artifact, user string) (passwdEntry, bool, []report.Finding, error) {
	passwd, present, err := a.ReadFile(ctx, "/etc/passwd")
	switch {
	case errors.Is(err, assemble.ErrFileTooLarge):
		return passwdEntry{}, false,
			warn("/etc/passwd is larger than a checker will read, so user %q could not be resolved here", user), nil
	case err != nil:
		return passwdEntry{}, false, nil, fmt.Errorf("read /etc/passwd: %w", err)
	case !present:
		return passwdEntry{}, false,
			fail("no /etc/passwd, so user %q resolves to nothing the host can read before the container exists", user), nil
	}

	// Only a named group is looked up, so an unreadable group file is
	// irrelevant to an identity that does not name one.
	var groupFile []byte
	if group, ok := explicitGroup(user); ok {
		if !allDigits(group) {
			groupFile, _, err = a.ReadFile(ctx, "/etc/group")
			switch {
			case errors.Is(err, assemble.ErrFileTooLarge):
				return passwdEntry{}, false,
					warn("/etc/group is larger than a checker will read, so user %q could not be resolved here", user), nil
			case err != nil:
				return passwdEntry{}, false, nil, fmt.Errorf("read /etc/group: %w", err)
			}
		}
	}

	who, resolved := lookupPasswd(string(passwd), string(groupFile), user)
	if !resolved {
		return who, false,
			fail("user %q does not resolve to a uid, gid and home; the host reads those from the image, before the container exists", user), nil
	}
	return who, true, nil, nil
}

var checks = []check{
	{name: "agent-skill-content", requirement: "agent-skill@1/content-present", run: checkAgentSkillContent},
	{
		name:        "descriptor-valid",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			raw := []byte(s.artifact.Annotations()[spec.AnnotationDescriptor])
			if _, err := spec.ValidatePublished(raw, s.descriptor); err != nil {
				return fail("published descriptor is invalid: %v", spec.WithSource(err, spec.AnnotationDescriptor, raw))
			}
			return nil
		},
	},
	{
		name:        "schema-version-annotation",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			got := s.artifact.Annotations()[spec.AnnotationSchemaVersion]
			if got != s.descriptor.SchemaVersion {
				return fail("%s is %q, descriptor says %q", spec.AnnotationSchemaVersion, got, s.descriptor.SchemaVersion)
			}
			return nil
		},
	},
	{
		name:        "capabilities-annotation",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			got, present := s.artifact.Annotations()[spec.AnnotationCapabilities]
			want := spec.CapabilityTypes(s.descriptor.Capabilities)
			switch {
			case want == "" && present:
				return fail("%s is present as %q, but the kit requests no capabilities", spec.AnnotationCapabilities, got)
			case want == "":
				return nil
			case got != want:
				return fail("%s is %q, descriptor requests %q", spec.AnnotationCapabilities, got, want)
			}
			return nil
		},
	},
	{
		name:        "built-by-annotation",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			// Shape only, and deliberately: no reader can know which
			// frontend built an image, so the claim is unverifiable and
			// the check asks the one thing it can — that a stamp which
			// is present is one a consumer can read.
			builtBy, present, err := spec.ParseBuiltBy(s.artifact.Annotations())
			switch {
			case !present:
				// Kits published before the annotation existed carry no
				// stamp, and nothing about them is wrong for it. There
				// is nothing to judge, which is not the same as having
				// judged nothing — so no finding, not a skip.
				return nil
			case err != nil:
				return fail("%s does not decode: %v", spec.AnnotationBuiltBy, err)
			case builtBy.Name == "":
				return fail("%s names no frontend", spec.AnnotationBuiltBy)
			case builtBy.Version == "":
				return fail("%s names no version; an unreleased build says %q", spec.AnnotationBuiltBy, "dev")
			}
			return nil
		},
	},
	{
		name:        "oci-annotations",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			ann := s.artifact.Annotations()
			derived := spec.OCIAnnotations(s.descriptor)
			var findings []report.Finding
			// Every managed key, not only the derived ones: publishing
			// emits no key for an empty field, so an annotation the
			// descriptor cannot account for is as wrong as a mismatched
			// one.
			for _, key := range managedOCIKeys {
				want, derivedHas := derived[key]
				got, present := ann[key]
				switch {
				case derivedHas && got != want:
					findings = append(findings, report.Failf("%s is %q, descriptor derives %q", key, got, want))
				case !derivedHas && present:
					findings = append(findings, report.Failf(
						"%s is %q, but the descriptor field it derives from is empty", key, got))
				}
			}
			// The artifact cannot show whether created came from
			// SOURCE_DATE_EPOCH or the wall clock; only its format is
			// observable here.
			if created, present := ann[ocispec.AnnotationCreated]; present {
				if !validRFC3339Timestamp(created) {
					findings = append(findings, report.Failf("%s is %q, must be an RFC 3339 timestamp", ocispec.AnnotationCreated, created))
				}
			}
			// VCS and base-image state remain deliberately not emitted:
			// provenance records them, never the descriptor.
			for _, key := range []string{
				"org.opencontainers.image.revision",
				"org.opencontainers.image.base.name",
				"org.opencontainers.image.base.digest",
			} {
				if _, present := ann[key]; present {
					findings = append(findings, report.Finding{
						Severity: report.Fail,
						Detail:   fmt.Sprintf("%s must not be emitted", key),
					})
				}
			}
			return findings
		},
	},
	{
		name:        "at-least-one-layer",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			layers, ok, err := s.artifact.Layers(ctx)
			if err != nil {
				return fail("read layers: %v", err)
			}
			if !ok {
				return skip("layers are not assembled yet")
			}
			if len(layers) == 0 {
				return fail("manifest has no layers; the OCI image-manifest schema requires at least one")
			}
			return nil
		},
	},
	{
		name:        "staged-sources",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			if s.stem == "" {
				return fail("no kit sources staged under %s; every kit is self-describing", StagedKitRoot)
			}
			staged, ok, err := s.artifact.ReadFile(ctx, path.Join(StagedKitRoot, s.stem, stagedDescriptorName))
			if err != nil {
				return fail("read staged descriptor: %v", err)
			}
			if !ok {
				return fail("staged %s is missing under %s", stagedDescriptorName, path.Join(StagedKitRoot, s.stem))
			}
			// The annotation carries compact JSON and the staged file the
			// expanded YAML: two serializations of one document, so they
			// are compared decoded.
			stagedDescriptor, err := spec.Decode(staged)
			if err != nil {
				return fail("staged %s does not decode: %v", stagedDescriptorName, err)
			}
			if !descriptorsAgree(stagedDescriptor, s.descriptor) {
				return fail("staged %s and the descriptor annotation describe different kits", stagedDescriptorName)
			}
			return nil
		},
	},
	{
		name:        "staged-recipe",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			if s.stem == "" {
				return skip("no staged kit root to look in")
			}
			present, err := hasFile(ctx, s.artifact, path.Join(StagedKitRoot, s.stem, stagedRecipeName))
			if err != nil {
				return fail("read staged recipe: %v", err)
			}
			// A declaration-only mixin has no recipe to stage; anything
			// with content does.
			hasRecipe := s.descriptor.Build != "" || s.descriptor.Dockerfile != ""
			if hasRecipe && !present {
				return fail("descriptor declares a recipe but %s is not staged", stagedRecipeName)
			}
			return nil
		},
	},
	{
		name:        "agent-context-staged",
		requirement: "agent-context@1",
		run: func(ctx context.Context, s *state) []report.Finding {
			for _, entry := range spec.DeclaredCapabilities(s.descriptor.Capabilities) {
				ac, err := spec.AgentContextOf([]spec.Capability{entry})
				if err != nil {
					return fail("decode context: %v", err)
				}
				if ac == nil || ac.ContentFile == "" {
					continue
				}
				if s.stem == "" {
					return skip("no staged kit root to resolve the context against")
				}
				// Beside this kit's own sources, not merely somewhere under
				// the root: pointing at another kit's directory would make the
				// body something this artifact does not carry.
				own := path.Join(StagedKitRoot, s.stem) + "/"
				clean := path.Clean(ac.ContentFile)
				if !strings.HasPrefix(clean, own) || clean != ac.ContentFile {
					return fail("published contentFile is %q; publishing rewrites it to a path directly under %s", ac.ContentFile, own)
				}
				// The staged descriptor and recipe paths are reserved: a
				// contentFile claiming one would collide with what staging
				// itself writes, and the file existing proves the collision
				// rather than the context. Only the two direct source paths
				// collide — a nested docs/kit.yaml is an ordinary name. The
				// frontend refuses this at build; the shared rule has to
				// refuse it for artifacts the frontend never saw.
				if clean == own+stagedDescriptorName || clean == own+stagedRecipeName {
					return fail("contentFile %q is reserved for staged kit sources", clean)
				}
				present, err := hasFile(ctx, s.artifact, ac.ContentFile)
				if err != nil {
					return fail("read staged context: %v", err)
				}
				if !present {
					return fail("contentFile points at %q, which the image does not carry", ac.ContentFile)
				}
			}
			return nil
		},
	},
	{
		name:        "image-config",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			// Every kit is an ordinary image with an image config, so the
			// blob must parse for BOTH kinds — returning early for mixins
			// would certify an artifact whose config is malformed JSON.
			cfg, err := s.artifact.Config(ctx)
			if err != nil {
				return fail("read image config: %v", err)
			}
			if s.descriptor.Kind != spec.KindWorkload {
				return nil
			}
			// The effective launch command is entrypoint followed by cmd;
			// a slice holding one empty string satisfies a nil check and
			// then executes an empty path.
			argv := append(append([]string{}, cfg.Config.Entrypoint...), cfg.Config.Cmd...)
			if len(argv) == 0 || argv[0] == "" {
				return fail("a workload runs under a bare docker run, so its config needs a runnable entrypoint or cmd")
			}
			return nil
		},
	},
	{
		// Judged on the entries alone, because that is what replaces the
		// base's: an overlay staying out of the home ships neither and
		// has nothing to answer for.
		name:        "overlay-home-ownership",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			w, findings := overlayWalkerFor(ctx, s)
			if w == nil {
				return findings
			}
			for _, level := range homeLevels {
				st, present, err := w.DirStat(ctx, level.path)
				if err != nil {
					return append(findings, fail("read %s: %v", level.path, err)...)
				}
				if present && st.Uid != level.uid {
					findings = append(findings, fail("the overlay ships %s owned by uid %d; its entry replaces the base's, and %s must own it", level.path, st.Uid, level.owner)...)
				}
			}
			return findings
		},
	},
	{
		// SHOULD, so a warning: from the artifact alone a link into the
		// base it will land on looks the same as one into a build stage
		// that no longer exists, and only the second is always broken.
		name:        "overlay-links-resolve",
		requirement: "SPEC-v3 §10",
		run: func(ctx context.Context, s *state) []report.Finding {
			w, findings := overlayWalkerFor(ctx, s)
			if w == nil {
				return findings
			}
			dangling, err := w.DanglingSymlinks(ctx)
			if err != nil {
				return fail("read symlinks: %v", err)
			}
			for _, l := range dangling {
				findings = append(findings, warn("%s links to %s, which does not resolve within the overlay's own layers", l.Path, l.Target)...)
			}
			return findings
		},
	},
	{
		// The floor a workload promises by declaring sbx@1: the shells the
		// host runs things through, and an identity it can resolve without
		// assuming one. Judged from the artifact, so a kit that cannot be
		// operated this way is caught at publish rather than at the first
		// failed hook.
		name:        "sbx-platform-floor",
		requirement: "sbx@1",
		run: func(ctx context.Context, s *state) []report.Finding {
			if !spec.HasCapability(spec.DeclaredCapabilities(s.descriptor.Capabilities), spec.CapabilitySbx) {
				return nil
			}
			if s.descriptor.Kind != spec.KindWorkload {
				return fail("only a workload can carry the platform floor: a mixin's image config never becomes the composed image's, so nothing a host reads would come from here")
			}

			// The identity comes first: which execute bit applies to a
			// shell depends on who the image says will run it.
			cfg, err := s.artifact.Config(ctx)
			if err != nil {
				return fail("read image config: %v", err)
			}
			// Not trimmed: the host resolves the literal value, so an
			// image declaring " agent" is asking for a user that does
			// not exist, and certifying it against "agent" would hide
			// exactly that.
			user := cfg.Config.User
			var (
				who      passwdEntry
				resolved bool
				findings []report.Finding
			)
			if strings.TrimSpace(user) == "" {
				// Recorded and carried past, not returned: the shells
				// are their own MUSTs, there or missing whoever would
				// have run them. Stopping at the identity would report
				// an image that states none and ships no shell either
				// as having one thing wrong, and hand back the rest a
				// rebuild at a time.
				findings = fail("image config declares no user; declaring sbx@1 asks the host to honor an identity the image does not state")
			} else {
				who, resolved, findings, err = resolveImageUser(ctx, s.artifact, user)
				if err != nil {
					return fail("%v", err)
				}
			}

			for _, shell := range []string{"/bin/sh", "/bin/bash"} {
				present, err := hasFile(ctx, s.artifact, shell)
				if err != nil {
					return append(findings, fail("read %s: %v", shell, err)...)
				}
				if !present {
					findings = append(findings, fail("%s is missing; the host runs hooks through sh and launches the agent under bash", shell)...)
					continue
				}
				if !resolved {
					continue
				}
				// Occupying the path is not being a shell the declared
				// user can run: a placeholder, or a binary whose bits
				// leave this identity out, resolves here and then fails
				// at the first hook.
				exec, known, err := executableBy(ctx, s.artifact, shell, who)
				if err != nil {
					return append(findings, fail("read %s permissions: %v", shell, err)...)
				}
				if known && !exec {
					findings = append(findings, fail("%s is not executable by %s, the user the image declares; the host runs hooks through sh and launches the agent under bash", shell, user)...)
				}
			}
			return findings
		},
	},
	{
		// BASH_ENV is the only thing that loads the sandbox's persistent
		// environment into the agent, which is started from neither a
		// login nor an interactive shell. SHOULD, so a kit that names no
		// file is warned rather than failed.
		name:        "sbx-persistent-env",
		requirement: "sbx@1",
		run: func(ctx context.Context, s *state) []report.Finding {
			if !spec.HasCapability(spec.DeclaredCapabilities(s.descriptor.Capabilities), spec.CapabilitySbx) ||
				s.descriptor.Kind != spec.KindWorkload {
				return nil
			}
			cfg, err := s.artifact.Config(ctx)
			if err != nil {
				return fail("read image config: %v", err)
			}
			const key = "BASH_ENV="
			var file string
			for _, e := range cfg.Config.Env {
				if strings.HasPrefix(e, key) {
					file = strings.TrimPrefix(e, key)
				}
			}
			if file == "" {
				return warn("image config sets no BASH_ENV, so the agent starts without the sandbox's persistent environment")
			}
			if !path.IsAbs(file) {
				return warn("BASH_ENV is %s, which bash resolves against whatever directory the agent happens to run from; name an absolute path", file)
			}
			present, err := hasFile(ctx, s.artifact, file)
			if err != nil {
				return fail("read %s: %v", file, err)
			}
			if !present {
				return warn("BASH_ENV names %s, which the image does not ship", file)
			}
			return nil
		},
	},
	{
		// What §9.6 derived is evidence rather than a claim, and the
		// evidence travels with it: the databases those entries were
		// read from are in the artifact. So each entry is held back to
		// the filesystem that is supposed to have produced it, which is
		// what separates a derivation from an assertion.
		//
		// One direction only. A package with no entry is not a finding:
		// §9.6 states an entry only where every platform agreed, so a
		// package this platform's database lists is legitimately absent
		// from a descriptor another platform disagreed about.
		name:        "derived-provides",
		requirement: "SPEC-v3 §9.6",
		run: func(ctx context.Context, s *state) []report.Finding {
			byNamespace := derivedByNamespace(s.descriptor.Provides)
			if len(byNamespace) == 0 {
				return nil
			}
			// A mixin's layers are a delta, where a package database is
			// whatever the recipe happened to rewrite rather than an
			// inventory — and §5.3 admits one workload per composition,
			// which is what gives a derived name one owner.
			//
			// Judged first, before the set skip below, because it needs
			// no database: a set of mixins merges to kind: mixin, and if
			// one of them carried derived entries the union brings them
			// along — ValidatePublished accepts the namespaces by design,
			// so nothing else would refuse the result.
			if s.descriptor.Kind != spec.KindWorkload {
				entries := flattenDerived(byNamespace)
				return fail("descriptor carries %d provides entries derived from package databases, starting %s; only a workload's root filesystem answers for those, and a mixin's layers are a delta",
					len(entries), entries[0])
			}
			// A merged set carries its kits' entries through the provides
			// union rather than deriving its own (§9.6), so these were
			// read from the listed workload's filesystem BEFORE anything
			// composed onto it. What this artifact can read is the merged
			// database, which a mixin that upgraded or removed a package
			// has since changed — and judging a correctly carried entry
			// against it would report the merge as a violation. Only the
			// comparison is skipped; the entries stay checkable at the
			// kit they came from, where merged-set-declarations reaches.
			if len(s.descriptor.Kits) > 0 {
				return skip("a merged set carries its kits' derived entries, which were read before composition")
			}

			var findings []report.Finding
			for _, db := range spec.PackageDatabases() {
				entries := byNamespace[db.Namespace]
				if len(entries) == 0 {
					continue
				}
				body, present, err := s.artifact.ReadFile(ctx, db.Path)
				switch {
				case errors.Is(err, assemble.ErrFileTooLarge):
					findings = append(findings, report.Warnf(
						"%s is larger than a checker will read, so the %s/ entries could not be held to it", db.Path, db.Namespace))
					continue
				case err != nil:
					return append(findings, fail("read %s: %v", db.Path, err)...)
				case !present:
					findings = append(findings, report.Failf(
						"descriptor carries %d %s/ entries but the image has no %s to read them from",
						len(entries), db.Namespace, db.Path))
					continue
				}
				installed, err := db.Read(bytes.NewReader(body))
				if err != nil {
					return append(findings, fail("%s: %v", db.Path, err)...)
				}
				// Every record for a name, not the last one. A multiarch
				// database names one package once per architecture, and
				// §9.6 states an entry only where they agree — so
				// keeping one version would let an entry that disagrees
				// with a stanza it is not next to pass on the strength
				// of whichever was read last.
				recorded := map[string][]string{}
				for _, p := range installed {
					recorded[p.Name] = append(recorded[p.Name], p.Version)
				}
				for _, entry := range entries {
					raws, ok := recorded[entry.name]
					if !ok {
						findings = append(findings, report.Failf(
							"%s is not a package %s records as installed, so nothing in the image offers it",
							entry.spelling, db.Path))
						continue
					}
					for _, raw := range raws {
						if want := spec.PackageVersion(raw); want != entry.version {
							findings = append(findings, report.Failf(
								"%s names version %s, but %s records %s, whose published version is %s",
								entry.spelling, entry.version, db.Path, raw, want))
						}
					}
				}
			}
			return findings
		},
	},
	{
		name:        "merged-set",
		requirement: "SPEC-v3 §9.5",
		run: func(ctx context.Context, s *state) []report.Finding {
			if len(s.descriptor.Kits) == 0 {
				return nil
			}
			var findings []report.Finding
			// A set's content is the kits it lists, so the merged
			// artifact must not also claim a recipe: whichever it was
			// built from, the other is a description of content it
			// does not carry.
			if s.descriptor.Build != "" || s.descriptor.Dockerfile != "" {
				findings = append(findings, report.Failf(
					"descriptor lists kits and declares a content recipe; a kit's content comes from exactly one of them"))
			}
			// Each listed kit's own sources ride along in the merged
			// filesystem, which is what makes a merged kit
			// self-describing about what it was built from — and the
			// only place a consumer can read their declarations
			// without fetching them. Every one of them stages under
			// its own stem (two sharing one is refused at build), so a
			// merged kit carries at least its own root plus theirs.
			stems, err := s.artifact.StagedStems(ctx)
			if err != nil {
				return append(findings, report.Failf("list staged kit roots: %v", err))
			}
			if want := len(s.descriptor.Kits) + 1; len(stems) < want {
				findings = append(findings, report.Failf(
					"descriptor lists %d kits, so at least %d staged roots are expected (theirs plus its own), but %d are present: %s",
					len(s.descriptor.Kits), want, len(stems), strings.Join(stems, ", ")))
			}
			return findings
		},
	},
	{
		name:        "merged-set-declarations",
		requirement: "SPEC-v3 §9.5",
		run: func(ctx context.Context, s *state) []report.Finding {
			if len(s.descriptor.Kits) == 0 {
				return nil
			}
			resolver, ok := s.artifact.(kitResolver)
			if !ok {
				return skip("this source cannot reach the kits the set lists")
			}

			// Each listed kit is read at the digest the set recorded,
			// so what the merge is judged against is what it merged.
			contributions := make([]spec.Contribution, 0, len(s.descriptor.Kits))
			published := make([]spec.Contribution, 0, len(s.descriptor.Kits))
			for _, k := range s.descriptor.Kits {
				listed, err := resolver.ResolveKit(ctx, k.Ref, k.Digest)
				if err != nil {
					return skip("%s is not reachable from here: %v", k.Ref, err)
				}
				raw := listed.Annotations()[spec.AnnotationDescriptor]
				if raw == "" {
					return fail("%s carries no descriptor annotation, so it is not a kit this set could have merged", k.Ref)
				}
				d, err := spec.Decode([]byte(raw))
				if err != nil {
					return fail("descriptor does not decode: %v", spec.WithSource(err, k.Ref+" (published descriptor)", []byte(raw)))
				}
				// Held to the published form before it is compared
				// against: a kit the frontend could never have merged
				// — an unmerged set, an unversioned provide — would
				// otherwise have its malformed declarations skipped
				// and the merge reported as conforming to them.
				if _, err := spec.ValidatePublished([]byte(raw), d); err != nil {
					return fail("%s is not a valid published kit, so this set could not have merged it: %v", k.Ref, spec.WithSource(err, k.Ref+" (published descriptor)", []byte(raw)))
				}
				published = append(published, spec.Contribution{Reference: k.Ref, Descriptor: d})
				// What the merge read is not this descriptor but its
				// effective form: expanded with the args the set
				// recorded, and versioned by the reference the set
				// named rather than by a version: the kit may have
				// outgrown. Comparing the published form instead
				// reports a conforming set as wrong.
				effective, err := effectiveContribution(k, d)
				if err != nil {
					return fail("%s: %v", k.Ref, err)
				}
				contributions = append(contributions, spec.Contribution{Reference: k.Ref, Descriptor: effective})
			}
			findings := judgeStagedSources(ctx, s.artifact, published)
			findings = append(findings, judgeReExports(s.descriptor, published)...)
			return append(findings, judgeMergedDeclarations(s.descriptor, contributions)...)
		},
	},
	{
		name:        "index-annotations",
		requirement: "SPEC-v3 §9.3",
		run: func(_ context.Context, s *state) []report.Finding {
			indexAnn, ok := s.artifact.IndexAnnotations()
			if !ok {
				return nil
			}
			manifestAnn := s.artifact.Annotations()
			var findings []report.Finding
			// Absence is an optimization, never the contract: a
			// multi-node builder merges results into a fresh index and
			// dissolves annotations, and consumers must fall back to the
			// manifest. Every promoted key gets the same treatment —
			// warning when the manifest carries it and the index does
			// not, since consumers reading only the index would take a
			// missing capabilities value as "none requested". Present but
			// disagreeing is worse than absent: a consumer reading the
			// index would judge a different kit than the one it runs.
			for _, key := range []string{
				spec.AnnotationDescriptor,
				spec.AnnotationSchemaVersion,
				spec.AnnotationCapabilities,
				spec.AnnotationBuiltBy,
			} {
				manifestValue, onManifest := manifestAnn[key]
				value, onIndex := indexAnn[key]
				switch {
				case onIndex && !onManifest:
					// Presence is meaning: an empty capabilities value
					// says "none requested", which an index must not say
					// when the manifest says nothing at all.
					findings = append(findings, report.Failf(
						"index carries %s, which the manifest does not; the index describes a kit the manifest is not", key))
				case !onIndex && onManifest:
					findings = append(findings, report.Warnf(
						"index carries no %s annotation; consumers fall back to the platform manifest", key))
				case onIndex && value != manifestValue:
					findings = append(findings, report.Failf(
						"index %s disagrees with the manifest's", key))
				}
			}
			return findings
		},
	},
}

// derivedEntry is one §9.6 provides entry, split for comparison against
// a database and kept with the spelling the descriptor used, so a finding
// names the string a reader would search the descriptor for.
type derivedEntry struct {
	spelling string
	name     string
	version  string
}

// derivedByNamespace groups a descriptor's derived provides by the
// package manager that answers for them. An entry that does not parse is
// left alone: descriptor-valid owns the grammar, and reporting it twice
// would read as two problems.
func derivedByNamespace(provides []string) map[string][]derivedEntry {
	out := map[string][]derivedEntry{}
	for _, s := range provides {
		p, err := spec.ParseProvide(s)
		if err != nil || !spec.IsDerivedProvide(p.Name) {
			continue
		}
		namespace, name, _ := strings.Cut(p.Name, "/")
		out[namespace] = append(out[namespace], derivedEntry{
			spelling: strings.TrimSpace(s),
			name:     name,
			version:  p.Version,
		})
	}
	return out
}

// flattenDerived lists every grouped entry as the descriptor spelled it,
// ordered so a finding naming one of them names the same one every run.
func flattenDerived(byNamespace map[string][]derivedEntry) []string {
	var out []string
	for _, entries := range byNamespace {
		for _, e := range entries {
			out = append(out, e.spelling)
		}
	}
	sort.Strings(out)
	return out
}

// kitResolver is a source that can reach the kits a merged set lists,
// which is what makes the merge checkable against its inputs rather
// than only against itself. A source with one artifact and no registry
// behind it does not implement this, and the check skips.
type kitResolver interface {
	ResolveKit(ctx context.Context, ref, digest string) (Artifact, error)
}

// judgeReExports holds the set's arg declarations to the contracts of
// the kits' args they stand in for.
//
// Checkable from the artifact alone, which is why it is checked here:
// the merged descriptor keeps kits[].args and the set's own args, and
// each kit's declarations come from the digest it is pinned to. What
// the rule protects is the installer, who sees only the set's
// declaration — so an enum re-exported through an unconstrained arg
// puts a value the kit would have refused in front of it with
// nothing left to refuse.
func judgeReExports(merged *spec.Descriptor, kits []spec.Contribution) []report.Finding {
	byReference := make(map[string]*spec.Descriptor, len(kits))
	for _, c := range kits {
		byReference[c.Reference] = c.Descriptor
	}

	var findings []report.Finding
	for _, k := range merged.Kits {
		d, ok := byReference[k.Ref]
		if !ok {
			continue
		}
		names := make([]string, 0, len(k.Args))
		for name := range k.Args {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			value := k.Args[name]
			if !spec.IsWholeArgRef(value) {
				continue
			}
			referenced := spec.ReferencedArgs([]byte(value))
			if len(referenced) != 1 {
				continue
			}
			setArg, declared := merged.Args[referenced[0]]
			if err := spec.CheckReExport(name, d.Args[name], referenced[0], setArg, declared); err != nil {
				findings = append(findings, report.Failf("%s: %v", k.Ref, err))
			}
		}
	}
	return findings
}

// judgeStagedSources binds each listed kit to a staged root carrying
// its descriptor.
//
// The count alone proves nothing: an artifact with as many unrelated
// roots as it lists kits satisfies it while carrying none of their
// sources. What §10 asks is that a consumer can read the declarations
// of every kit that went in, so each one has to be findable — matched
// by the descriptor itself, since a staged root is named for a
// filename stem the reference does not carry.
func judgeStagedSources(ctx context.Context, a Artifact, kits []spec.Contribution) []report.Finding {
	stems, err := a.StagedStems(ctx)
	if err != nil {
		return fail("list staged kit roots: %v", err)
	}
	staged := make([]*spec.Descriptor, 0, len(stems))
	for _, stem := range stems {
		raw, ok, err := a.ReadFile(ctx, path.Join(StagedKitRoot, stem, stagedDescriptorName))
		if err != nil || !ok {
			continue
		}
		if d, err := spec.Decode(raw); err == nil {
			staged = append(staged, d)
		}
	}

	// Consumed as they match: two kits may publish identical
	// descriptors, and one staged root standing in for both would let
	// an artifact carry half of what it lists.
	used := make([]bool, len(staged))
	var findings []report.Finding
	for _, c := range kits {
		found := false
		for i, d := range staged {
			if !used[i] && descriptorsAgree(d, c.Descriptor) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			findings = append(findings, report.Failf(
				"%s is listed but its staged sources are not present; a merged kit carries the declarations of every kit it merged, and %d staged root(s) hold something else",
				c.Reference, len(staged)))
		}
	}
	return findings
}

// judgeMergedDeclarations holds a merged descriptor to the kits it says
// it was merged from.
//
// Three of §9.5's rules are visible from outside: a listed kit's
// provides carry over, its licenses carry over, and a requirement the
// set answers internally is dropped while one nothing answers is kept.
// The rest of the merge — how policies union, how hooks order — cannot
// be recovered from the result, because the merged entry does not say
// which contribution each part came from.
//
// Only the subset direction is judged. The merged descriptor also
// carries the set's OWN declarations, which no consumer can tell apart
// from its kits', so "nothing was lost" is checkable and "nothing was
// invented" is not.
func judgeMergedDeclarations(merged *spec.Descriptor, kits []spec.Contribution) []report.Finding {
	var findings []report.Finding

	carried := map[string]bool{}
	for _, s := range merged.Provides {
		p, err := spec.ParseProvide(s)
		if err != nil {
			continue
		}
		// A published descriptor may leave a provide bare and carry
		// the version in version:, which §9.2 permits — so the key
		// has to materialize the merged descriptor's own fallback,
		// or a conforming set reads as having dropped what it kept.
		if p.Version == "" {
			p.Version = merged.Version
		}
		carried[p.Name+"@"+p.Version] = true
	}
	for _, c := range kits {
		for _, s := range c.Descriptor.Provides {
			p, err := spec.ParseProvide(s)
			if err != nil {
				continue
			}
			if p.Version == "" {
				p.Version = resolve.EffectiveProvideVersion(c.Reference, c.Descriptor)
			}
			if !carried[p.Name+"@"+p.Version] {
				findings = append(findings, report.Failf(
					"%s provides %s@%s, which the merged descriptor does not; a merged kit offers what its kits offered",
					c.Reference, spec.DisplayCapabilityName(p.Name), p.Version))
			}
		}
	}

	licensed := map[string]bool{}
	for _, l := range merged.Licenses {
		licensed[l] = true
	}
	for _, c := range kits {
		for _, l := range c.Descriptor.Licenses {
			if !licensed[l] {
				findings = append(findings, report.Failf(
					"%s is licensed %s, which the merged descriptor does not name; a merged kit ships its kits' content and reports their terms",
					c.Reference, l))
			}
		}
	}

	// A requirement the set answers itself is gone from the merged
	// descriptor; one nothing in the set answers is still there, since
	// it is now an ask of whatever composition the merged kit lands in.
	// Ownership, as the merge and the resolver both apply it: a kit's
	// own provide never answers its own requirement, so a merged
	// descriptor that dropped one on that basis is wrong however it
	// reads in isolation.
	owned := spec.OwnedProvides(kits)
	for _, relation := range []struct {
		name         string
		mergedStates []string
		of           func(*spec.Descriptor) []string
	}{
		{"requires", merged.Requires, func(d *spec.Descriptor) []string { return d.Requires }},
		{"integrates", merged.Integrates, func(d *spec.Descriptor) []string { return d.Integrates }},
	} {
		// Indexed by what each entry means rather than how it was
		// typed: an artifact restating an input in an equivalent
		// spelling would otherwise read as having dropped it.
		stated := map[string]bool{}
		for _, s := range relation.mergedStates {
			if r, err := spec.ParseRequire(s); err == nil {
				stated[spec.CanonicalRequire(r)] = true
			}
		}
		for _, c := range kits {
			for _, s := range relation.of(c.Descriptor) {
				r, err := spec.ParseRequire(s)
				if err != nil {
					continue
				}
				// Only the retained direction is decidable from
				// outside. A dropped entry may have been answered by
				// the set's OWN declarations, which the merged
				// descriptor does not distinguish from its kits' —
				// so absence is not evidence of anything, while an
				// entry the listed kits answer and the merge kept is
				// one nothing can ever satisfy.
				if spec.SatisfiedByOther(owned, r, c.Reference) && stated[spec.CanonicalRequire(r)] {
					findings = append(findings, report.Failf(
						"the merged descriptor still %s %q, which the kits it lists answer; a kit cannot satisfy its own requirement, so this can never resolve", relation.name, s))
				}
			}
		}
	}
	return findings
}

// effectiveContribution rebuilds what the frontend merged from one
// listed kit: its published descriptor with the args the set recorded
// resolved into it, and the version its consumption reference carries.
//
// The set's record is what makes this reproducible — the same args,
// against the same digest — so a check reading the published form
// alone would judge the merge against something it never saw.
func effectiveContribution(k spec.Kit, d *spec.Descriptor) (*spec.Descriptor, error) {
	values, err := spec.KitArgValues(d.Args, k.Args)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	expanded, err := spec.ExpandCreateArgs(raw, d.Args, values)
	if err != nil {
		return nil, err
	}
	out, err := spec.Decode(expanded)
	if err != nil {
		return nil, fmt.Errorf("declarations do not decode once the set's args resolve: %w", err)
	}
	out.Version = resolve.EffectiveProvideVersion(k.Ref, out)
	out.Args = nil
	return out, nil
}

// fileChecker is a source that can establish presence without fetching
// content. Existence checks prefer it: a context body is deliberately
// bulky, and reading it in full to learn it exists would subject it to
// content-read bounds that do not apply to it.
type fileChecker interface {
	HasFile(ctx context.Context, name string) (bool, error)
}

// FileStat is a path's permission metadata: which execute bit applies
// depends on who the image says will run it, and only an ordinary file
// can be run at all.
type FileStat struct {
	Mode     int64
	Uid, Gid int
	Regular  bool
}

// statChecker is a source that can report that metadata. A source that
// cannot leaves permission judgments unmade rather than guessed.
type statChecker interface {
	FileStat(ctx context.Context, name string) (FileStat, bool, error)
}

// Symlink is a link the composed filesystem exposes, at its path and with
// its target as written.
type Symlink struct {
	Path, Target string
}

// overlayWalker is a source that can read a layer's own entries rather
// than the files they resolve to: the directory metadata an overlay
// replaces the base's with, and the links that resolve nowhere within it.
// A source that cannot leaves both unjudged.
type overlayWalker interface {
	DirStat(ctx context.Context, name string) (FileStat, bool, error)
	DanglingSymlinks(ctx context.Context) ([]Symlink, error)
}

// homeLevels are the directories §12 lays out, with the owner each must
// keep when an overlay ships an entry for it.
var homeLevels = []struct {
	path  string
	uid   int
	owner string
}{
	{"/home", 0, "root"},
	{"/home/agent", 1000, "the agent user (uid 1000)"},
}

// overlayWalkerFor is the source's overlayWalker when the check applies:
// a mixin, whose layers are the overlay, from a source that has
// assembled them. A skip is returned where the layers are not there yet,
// or the source cannot read them entry by entry.
func overlayWalkerFor(ctx context.Context, s *state) (overlayWalker, []report.Finding) {
	if s.descriptor.Kind != spec.KindMixin {
		return nil, nil
	}
	_, ok, err := s.artifact.Layers(ctx)
	if err != nil {
		return nil, fail("read layers: %v", err)
	}
	if !ok {
		return nil, skip("layers are not assembled yet")
	}
	w, ok := s.artifact.(overlayWalker)
	if !ok {
		return nil, skip("this source cannot read the overlay's own entries")
	}
	return w, nil
}

// executableBy reports whether the identity the image declares can
// execute a path, and whether the source could tell. The file's own bits
// only: root bypasses them, and whether an ancestor directory can be
// traversed is not something the layer inventory models.
func executableBy(ctx context.Context, a Artifact, name string, who passwdEntry) (exec, known bool, err error) {
	c, ok := a.(statChecker)
	if !ok {
		return false, false, nil
	}
	st, present, err := c.FileStat(ctx, name)
	if err != nil || !present {
		return false, false, err
	}
	// execve runs ordinary files. A FIFO, socket, or device node can
	// carry every execute bit there is and run none of them.
	if !st.Regular {
		return false, true, nil
	}
	if who.uid == 0 {
		return st.Mode&0o111 != 0, true, nil
	}
	switch {
	case int64(st.Uid) == who.uid:
		return st.Mode&0o100 != 0, true, nil
	case int64(st.Gid) == who.gid:
		return st.Mode&0o010 != 0, true, nil
	default:
		return st.Mode&0o001 != 0, true, nil
	}
}

func hasFile(ctx context.Context, a Artifact, name string) (bool, error) {
	if c, ok := a.(fileChecker); ok {
		return c.HasFile(ctx, name)
	}
	_, present, err := a.ReadFile(ctx, name)
	return present, err
}

// Requirements lists every requirement id the kit suite's checks name.
func Requirements() []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.requirement)
	}
	return out
}

// CheckNames lists the kit suite's check names, each unique. The
// statement-coverage guard pins its kit mapping to the CHECK, because
// several checks share one section-level requirement id and a mapping
// keyed by section could survive the deletion of the one check that
// actually supplies the evidence.
func CheckNames() []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.name)
	}
	return out
}

// descriptorsAgree reports whether two decoded descriptors describe the
// same kit. Serialization differs between the annotation and the staged
// file — compact JSON against expanded YAML — so they are compared as
// documents rather than as bytes: re-encoded canonically, then equal or
// not. Comparing a hand-picked subset would let a staged descriptor
// differ in everything unlisted.
func descriptorsAgree(a, b *spec.Descriptor) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

// ErrNotAKit reports an artifact carrying no descriptor annotation.
var ErrNotAKit = errors.New("not a kit")

// managedOCIKeys are the org.opencontainers.image.* keys derived from
// the descriptor. A builder-supplied created timestamp is checked
// separately because the descriptor cannot answer it.
var managedOCIKeys = []string{
	spec.OCIAnnotationTitle,
	spec.OCIAnnotationDescription,
	spec.OCIAnnotationAuthors,
	spec.OCIAnnotationSource,
	spec.OCIAnnotationLicenses,
	spec.OCIAnnotationVersion,
}
