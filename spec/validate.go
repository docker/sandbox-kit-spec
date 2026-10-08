package spec

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	containerpath "path"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/distribution/reference"
)

// canonicalAbsPath reports whether p is absolute and in canonical form.
// Canonical form is validated rather than normalized in: /x/../skills and
// /skills are different strings for one destination, so an alias would
// evade duplicate detection and could claim a second mode for a path
// already declared.
func canonicalAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") && p == containerpath.Clean(p)
}

// Size limits for the published descriptor. OCI puts no limit on annotation
// values, but the manifest as a whole meets practical registry ceilings
// around 4 MB; the grammar exiles everything bulky, so a rich descriptor
// measures in single-digit kilobytes and these bounds exist so no kit ever
// discovers a registry's limit in production.
const (
	// SizeWarnBytes is the advisory budget: crossing it is a warning.
	SizeWarnBytes = 64 * 1024
	// SizeErrorBytes is the hard budget: crossing it fails validation.
	SizeErrorBytes = 512 * 1024
)

var (
	envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	octalMode  = regexp.MustCompile(`^[0-7]{3,4}$`)
	sizeBytes  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?\s*([kKmMgGtT]i?[bB]?)?$`)
)

// Validate checks every rule the descriptor grammar states. It returns
// non-fatal warnings alongside all independent errors; a nil error with
// warnings means the descriptor is usable but the author should look.
// Errors are returned as ValidationErrors. Each FieldError carries the
// offending element's dotted path; WithSource adds source locations when
// the caller has the raw bytes.
func Validate(d *Descriptor) (warnings []string, err error) {
	var errs ValidationErrors
	if d.Kind != KindWorkload && d.Kind != KindMixin && d.Kind != KindSet {
		errs.add(fieldErrorf("kind", "kind must be %q, %q, or %q, got %q", KindWorkload, KindMixin, KindSet, d.Kind))
	}

	errs.add(validateIconURL(d.IconURL))
	errs.add(validateCapabilityNames(d))
	errs.add(validateCapabilityEntries(d))
	errs.add(validateArgs(d.Args))
	errs.add(validateRecipe(d))
	errs.add(validateKits(d))
	return warnings, errs.err()
}

// validateIconURL holds the icon to an absolute https URL. Every other
// display field is free text a consumer decides how to show; this one
// names a resource a consumer fetches and renders, so the schemes that
// turn a rendered image into code execution or a local-file read are
// refused here rather than left to each surface to remember.
func validateIconURL(icon string) error {
	if icon == "" {
		return nil
	}
	u, err := url.Parse(icon)
	if err != nil {
		return fieldErrorf("iconUrl", "iconUrl %q: %v", icon, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fieldErrorf("iconUrl", "iconUrl %q must be an absolute https URL", icon)
	}
	return nil
}

// validateRecipe checks the content-recipe declarations: build:,
// dockerfile:, and kits: are three homes for one thing, and an
// explicit dockerfile path must be servable by the build's dockerfile
// context — relative, and not escaping the descriptor's directory, which
// is that context's root.
func validateRecipe(d *Descriptor) error {
	var errs ValidationErrors
	if d.Build != "" && d.Dockerfile != "" {
		errs.add(fieldErrorf("dockerfile", "build: and dockerfile: are both declared; a kit's content recipe lives in exactly one place"))
	}
	// A set's content is the kits it lists, so a recipe beside them is a
	// second answer to what the layers are, not an addition to them.
	if len(d.Kits) > 0 {
		if d.Build != "" {
			errs.add(fieldErrorf("build", "kits: and an inline build: block are both declared; a set's content is the kits it lists, so it declares no recipe"))
		}
		if d.Dockerfile != "" {
			errs.add(fieldErrorf("dockerfile", "kits: and dockerfile: are both declared; a set's content is the kits it lists, so it declares no recipe"))
		}
	}
	if d.Dockerfile == "" {
		return errs.err()
	}
	cleaned := containerpath.Clean(d.Dockerfile)
	if containerpath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		errs.add(fieldErrorf("dockerfile", "dockerfile: %q must be a relative path inside the descriptor's directory — that directory is the build's dockerfile context, so nothing outside it can be read", d.Dockerfile))
	}
	return errs.err()
}

// manifestDigest is the one digest spelling a listed kit may be pinned
// by. Registries address manifests by sha256 today, and accepting a
// second algorithm here would mean accepting a pin no resolver could
// compare.
var manifestDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// validateKits checks a set's kits list: a set lists at least one, the
// entries are well-formed and distinct, and their args name something.
//
// A reference is held to a shape the frontend can resolve from the
// manifest alone. A local directory or a git URL names a kit that may
// not be published at all, and a set whose content could only be
// reproduced from someone's working copy is not shareable, which is the
// whole point of writing one.
func validateKits(d *Descriptor) error {
	var errs ValidationErrors
	if d.Kind == KindSet && len(d.Kits) == 0 {
		errs.add(fieldErrorf("kits", "kind: %s lists no kits; a set's content is the kits it lists", KindSet))
	}
	// Written and left empty, under any kind. Absent means a kit with
	// an ordinary recipe; present and empty means an author who meant
	// to list something, and reading it as absence would publish a
	// declaration-only kit where content was intended. The schema
	// refuses it through minItems, so the two agree.
	if d.Kind != KindSet && d.Kits != nil && len(d.Kits) == 0 {
		errs.add(fieldErrorf("kits", "kits: is present but empty; list the kits this one is merged from, or drop the field"))
	}
	seen := map[string]int{}
	for i, k := range d.Kits {
		at := fmt.Sprintf("kits[%d]", i)
		if strings.TrimSpace(k.Ref) == "" {
			errs.add(fieldErrorf(at+".ref", "kits[%d]: ref is required — the reference this kit is consumed by", i))
		}
		// The digest is judged whatever the reference looks like: it is
		// a pin the author wrote literally, and nothing an arg resolves
		// to can make a malformed one well-formed.
		if k.Digest != "" && !manifestDigest.MatchString(k.Digest) {
			errs.add(fieldErrorf(at+".digest", "kits[%d]: digest %q is not a sha256 manifest digest", i, k.Digest))
		}
		// Only the reference grammar defers, and only for an arg that
		// resolves in time to be of use. A set's kits are resolved
		// during the build, so a build-phase arg — the registry
		// namespace a set of one org's kits is published under is
		// exactly that — leaves a literal reference behind before
		// anything needs it. A create-phase arg does not: expansion
		// would leave the placeholder standing and the build would
		// fail trying to parse it as an image reference, which is a
		// worse place to learn it than here.
		//
		// An undeclared name is left alone: ValidateRaw reports it
		// against the whole document, and saying it twice differently
		// would only confuse the author.
		if ContainsArgRef(k.Ref) {
			for _, name := range ReferencedArgs([]byte(k.Ref)) {
				if decl, declared := d.Args[name]; declared && decl.BuildArg == "" {
					errs.add(fieldErrorf(at+".ref", "kits[%d]: ref names %q, which resolves at create, but a set's kits are resolved at build; declare it with buildArg:, or write the reference out", i, name))
				}
			}
		} else if strings.TrimSpace(k.Ref) != "" {
			errs.add(validateKitReference(at, i, k))
		}
		if prev, dup := seen[k.Ref]; dup {
			errs.add(fieldErrorf(at+".ref", "kits[%d]: %q already listed at kits[%d]", i, k.Ref, prev))
		} else if strings.TrimSpace(k.Ref) != "" {
			seen[k.Ref] = i
		}
		for _, name := range slices.Sorted(maps.Keys(k.Args)) {
			if !envVarName.MatchString(name) {
				errs.add(fieldErrorf(at+".args", "kits[%d]: %q is not a valid arg name", i, name))
			}
		}
	}
	return errs.err()
}

// validateKitReference holds one listed kit's reference to the shape a
// frontend can resolve.
//
// The two refusals come first because they name what an author is
// likely to have reached for, and a generic parse error would not say
// why those forms cannot work. Everything else is held to the image
// reference grammar itself rather than to a set of prefix rules: a
// descriptor that validates has to be one the frontend can resolve, and
// anything short of the real parser leaves shapes (a URL with a scheme,
// an uppercase path) that pass here and fail at build.
func validateKitReference(at string, i int, k Kit) error {
	if k.Ref != strings.TrimSpace(k.Ref) || strings.ContainsAny(k.Ref, " \t\n") {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q contains whitespace", i, k.Ref)
	}
	if strings.HasPrefix(k.Ref, "git+") {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q is a git reference; a set's kits are published kit images, so the frontend can resolve them from the manifest alone", i, k.Ref)
	}
	if strings.HasPrefix(k.Ref, ".") || strings.HasPrefix(k.Ref, "/") {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q is a local path; a set's kits are published kit images, so the set stays reproducible away from the directory it was written in", i, k.Ref)
	}
	named, err := reference.ParseNormalizedNamed(k.Ref)
	if err != nil {
		return fieldErrorf(at+".ref", "kits[%d]: ref %q is not an image reference: %v", i, k.Ref, err)
	}
	// A reference that already carries a digest is pinned twice once
	// digest: is also stated, and two pins that disagree cannot both be
	// what the author meant. Judged here rather than only where the
	// build resolves it, because an artifact this frontend never built
	// reaches consumers through ValidatePublished alone — and a
	// descriptor recording one pin beside content fetched by another
	// is provenance that cannot be checked.
	if canonical, ok := named.(reference.Canonical); ok && k.Digest != "" && canonical.Digest().String() != k.Digest {
		return fieldErrorf(at+".digest", "kits[%d]: ref pins %s but digest: says %s; one kit has one pin", i, canonical.Digest(), k.Digest)
	}
	return nil
}

// ValidateRaw runs Validate plus the checks that need the raw bytes: the
// size budget, and that every ${{ kit.args.* }} reference names a declared
// arg.
func ValidateRaw(raw []byte, d *Descriptor) (warnings []string, err error) {
	var errs ValidationErrors
	warnings, err = Validate(d)
	errs.add(err)

	if len(raw) > SizeErrorBytes {
		errs.add(fieldErrorf("", "descriptor is %d bytes, over the %d byte budget; move bulk into layers", len(raw), SizeErrorBytes))
	}
	if len(raw) > SizeWarnBytes {
		warnings = append(warnings, fmt.Sprintf("descriptor is %d bytes; the advisory budget is %d", len(raw), SizeWarnBytes))
	}

	var references map[string][]string
	for _, name := range ReferencedArgs(raw) {
		if _, ok := d.Args[name]; !ok {
			if references == nil {
				references = argReferencePaths(raw)
			}
			paths := references[name]
			if len(paths) == 0 {
				paths = []string{""}
			}
			for _, at := range paths {
				errs.add(fieldErrorf(at, "descriptor references ${{ kit.args.%s }} but declares no arg %q", name, name))
			}
		}
	}
	return warnings, errs.err()
}

func validateCapabilityNames(d *Descriptor) error {
	var errs ValidationErrors
	// An authored version may reference a build-phase arg, expanded into
	// the published descriptor; ValidatePublished requires the literal.
	if d.Version != "" && !argRef.MatchString(d.Version) {
		if _, err := parseVersion(d.Version); err != nil {
			errs.add(fieldErrorf("version", "version: %v", err))
		}
	}
	for i, s := range d.Provides {
		// An authored provide may reference a build-phase arg
		// (`gh@${{ kit.args.version }}`); it parses only after the
		// frontend expands it into the published descriptor, which
		// ValidatePublished enforces.
		if argRef.MatchString(s) {
			continue
		}
		if _, err := ParseProvide(s); err != nil {
			errs.add(fieldErrorf(fmt.Sprintf("provides[%d]", i), "%v", err))
		}
	}
	// Requires, integrates, and conflicts stay literal in both forms: they
	// are what the resolver judges, and a parameterized constraint would
	// make the judgment depend on caller input.
	for i, s := range d.Requires {
		if argRef.MatchString(s) {
			errs.add(fieldErrorf(fmt.Sprintf("requires[%d]", i), "requires entry %q: arg references are not allowed in requires", s))
			continue
		}
		if _, err := ParseRequire(s); err != nil {
			errs.add(fieldErrorf(fmt.Sprintf("requires[%d]", i), "%v", err))
		}
	}
	for i, s := range d.Integrates {
		if argRef.MatchString(s) {
			errs.add(fieldErrorf(fmt.Sprintf("integrates[%d]", i), "integrates entry %q: arg references are not allowed in integrates", s))
			continue
		}
		if _, err := ParseRequire(s); err != nil {
			errs.add(fieldErrorf(fmt.Sprintf("integrates[%d]", i), "%v", err))
		}
	}
	for i, s := range d.Conflicts {
		if argRef.MatchString(s) {
			errs.add(fieldErrorf(fmt.Sprintf("conflicts[%d]", i), "conflicts entry %q: arg references are not allowed in conflicts", s))
			continue
		}
		if err := capabilityNameError(s); err != nil {
			errs.add(fieldErrorf(fmt.Sprintf("conflicts[%d]", i), "conflicts entry %q: %v", s, err))
		}
	}
	return errs.err()
}

// ValidatePublished runs ValidateRaw plus the published-form requirement:
// every build-phase reference has been expanded, so provides entries are
// literal. Create-phase references in hooks and files legitimately remain.
func ValidatePublished(raw []byte, d *Descriptor) (warnings []string, err error) {
	cp := *d
	cp.declarationsOnly = true
	d = &cp
	var errs ValidationErrors
	warnings, err = ValidateRaw(raw, d)
	errs.add(err)
	errs.add(validatePublishedSkillPaths(d.Capabilities, "capabilities"))
	if argRef.MatchString(d.Version) {
		errs.add(fieldErrorf("version", "published descriptor still references an arg in version %q; build-phase expansion did not run", d.Version))
	}
	for i, s := range d.Provides {
		if argRef.MatchString(s) {
			errs.add(fieldErrorf(fmt.Sprintf("provides[%d]", i), "published descriptor still references an arg in provides entry %q; build-phase expansion did not run", s))
		}
	}
	// §9.2 is a property of every published descriptor, not only of the
	// frontend's build path, so third-party artifacts are held to it too.
	if d.Version == "" {
		for i, s := range d.Provides {
			if argRef.MatchString(s) {
				continue
			}
			p, err := ParseProvide(s)
			if err == nil && p.Version == "" {
				errs.add(unversionedProvide(i, s))
			}
		}
	}
	// Build-phase args are baked before signing, so a reference to one
	// anywhere in the published form means expansion did not reach it.
	// Checked across the whole document rather than field by field:
	// create-phase references legitimately remain in hooks and file
	// contents, and only the arg's own declaration says which kind a
	// reference is.
	var references map[string][]string
	for _, name := range ReferencedArgs(raw) {
		if decl, declared := d.Args[name]; declared && decl.BuildArg != "" {
			if references == nil {
				references = argReferencePaths(raw)
			}
			paths := references[name]
			if len(paths) == 0 {
				paths = []string{""}
			}
			for _, at := range paths {
				// These fields have more specific published-form diagnostics.
				if at == "version" || strings.HasPrefix(at, "provides[") || (strings.HasPrefix(at, "kits[") && strings.HasSuffix(at, "].ref")) {
					continue
				}
				errs.add(fieldErrorf(at, "published descriptor still references ${{ kit.args.%s }}, which resolves at build; expansion did not reach it", name))
			}
		}
	}
	// kind: set is an authoring convenience the frontend resolves into
	// the kind the listed kits imply. Reaching a consumer means the
	// merge never ran, so the layers are not what the descriptor
	// describes.
	if d.Kind == KindSet {
		errs.add(fieldErrorf("kind", "published descriptor still declares kind: %s; the frontend derives %q or %q from the kits it lists, so this artifact was never merged", KindSet, KindWorkload, KindMixin))
	}
	// An authored entry names a version by tag; a published one has
	// been resolved, and the pin is what makes the record of how the
	// content was produced reproducible rather than a moving claim.
	for i, k := range d.Kits {
		if argRef.MatchString(k.Ref) {
			errs.add(fieldErrorf(fmt.Sprintf("kits[%d].ref", i), "published descriptor still references an arg in %q; a set's kits resolve at build, so build-phase expansion did not run", k.Ref))
		}
		if k.Digest == "" {
			errs.add(fieldErrorf(fmt.Sprintf("kits[%d].digest", i), "published descriptor lists %s with no digest; the frontend pins every one of a set's kits to the manifest it resolved", k.Ref))
		}
	}
	return warnings, errs.err()
}

// RequireVersionedProvides is the publish-time rule the frontend enforces
// on the expanded descriptor: every provides entry must carry a version —
// its own @version, or the descriptor's version: fallback. A published kit
// with an unversioned provide would satisfy only unconstrained requires and
// silently defeat version-constraint resolution; the build is where the author
// can still fix it. Consumption references (a version tag) can override
// these versions, never substitute for them. Kits with no provides publish
// fine: they offer nothing matchable, so there is nothing to version.
func RequireVersionedProvides(d *Descriptor) error {
	var errs ValidationErrors
	if d.Version != "" {
		return errs.err()
	}
	for i, s := range d.Provides {
		p, err := ParseProvide(s)
		if err != nil {
			errs.add(fieldErrorf(fmt.Sprintf("provides[%d]", i), "%v", err))
			continue
		}
		if p.Version == "" {
			errs.add(unversionedProvide(i, s))
		}
	}
	return errs.err()
}

func unversionedProvide(i int, s string) error {
	return fieldErrorf(fmt.Sprintf("provides[%d]", i),
		"provides entry %q has no version: add @<version> here or a top-level version: field (a version-shaped image tag can override it at consumption, but a published kit must carry one)", s)
}

// RequireAuthoredProvides refuses a provides entry an author must not
// write: §5.1 reserves the deb/ and apk/ namespaces for §9.6, where
// publishing fills them from the package databases in the content.
//
// Authored form only, which is why it is not folded into Validate: the
// published form legitimately carries these, and a runtime revalidating
// a descriptor on load has to accept what publishing put there. The
// distinction is the whole point — an entry under these namespaces is
// something read off a filesystem, and one written by hand would assert
// a fact about content instead of offering a capability, with nothing
// left to catch the difference.
func RequireAuthoredProvides(d *Descriptor) error {
	var errs ValidationErrors
	for i, s := range d.Provides {
		p, err := ParseProvide(s)
		// A malformed entry, or one still holding an arg reference, is
		// another rule's to report; a name that cannot be parsed cannot
		// be in a reserved namespace either.
		if err != nil {
			continue
		}
		if IsDerivedProvide(p.Name) {
			namespace, _, _ := strings.Cut(p.Name, "/")
			errs.add(fieldErrorf(fmt.Sprintf("provides[%d]", i),
				"provides entry %q is in the %s/ namespace, which publishing fills from the image's package database; drop it and let the build state what the content carries", s, namespace))
		}
	}
	return errs.err()
}

// needType is <namespace>/<name>@<version>: a dotted lowercase
// namespace, a hyphenated lowercase name, an integer config-schema
// version.
var needType = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?/[a-z0-9]([a-z0-9-]*[a-z0-9])?@[1-9][0-9]*$`)

// singletonCapabilities are policy-shaped types: at most one entry each.
// Instance-shaped types appear once per thing requested within each
// declaration block. Cross-contribution reconciliation belongs to merge;
// a shared instance key does not imply that a type permits merging.
var singletonCapabilities = map[string]bool{
	CapabilityGitIdentity:              true,
	CapabilityNetworkPolicy:            true,
	CapabilityNetworkPolicyV2:          true,
	CapabilityResources:                true,
	CapabilityPrivileged:               true,
	CapabilityKitRegistry:              true,
	CapabilityAgentSessions:            true,
	CapabilityAgentInteractiveSessions: true,
	CapabilityLifecycle:                true,
	CapabilityAgentContext:             true,
	CapabilitySbx:                      true,
	CapabilityLongRunning:              true,
}

// deref unwraps pointer and interface layers, the way JSON marshaling
// does, and reports whether it reached a null: a nil pointer, interface,
// map or slice.
func deref(v reflect.Value) (reflect.Value, bool) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return v, true
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		if v.IsNil() {
			return v, true
		}
	case reflect.Invalid:
		return v, true
	}
	return v, false
}

// hasNull reports whether an authored config value is null or a list
// holding a null element. Configs built in code can carry typed slices
// and pointers (a nil []string, CommandLine or *string marshals to null),
// so values are read by kind after unwrapping, not by one concrete type;
// arrays marshal to lists too.
func hasNull(v any) bool {
	rv, null := deref(reflect.ValueOf(v))
	if null {
		return true
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return false
	}
	for i := range rv.Len() {
		if _, null := deref(rv.Index(i)); null {
			return true
		}
	}
	return false
}

// emptyCommand reports whether an authored command (string or list form)
// names nothing: an empty or blank string, or a list that is empty or
// whose executable is blank. Values are unwrapped and read by kind, as in
// hasNull, so a CommandLine, []string or pointer built in code is judged
// like a decoded []any.
func emptyCommand(v any) bool {
	rv, null := deref(reflect.ValueOf(v))
	if null {
		return false
	}
	switch rv.Kind() {
	case reflect.String:
		return strings.TrimSpace(rv.String()) == ""
	case reflect.Slice, reflect.Array:
		if rv.Len() == 0 {
			return true
		}
		// argv[0] is the executable: an empty one cannot run.
		first, null := deref(rv.Index(0))
		return !null && first.Kind() == reflect.String && strings.TrimSpace(first.String()) == ""
	}
	return false
}

// argvContains reports whether any argv element contains the substring
// — placeholders may ride inside a larger token ("--prompt={{.Prompt}}").
func argvContains(argv []string, sub string) bool {
	for _, a := range argv {
		if strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

// configlessCapabilities take no config at all: present or absent.
var configlessCapabilities = map[string]bool{
	CapabilityGitIdentity: true,
	CapabilityPrivileged:  true,
	CapabilityKitRegistry: true,
	CapabilitySbx:         true,
	CapabilityLongRunning: true,
}

// validateCapabilityEntries checks the typed capability list. Type syntax and
// arity hold for every entry; config schemas are enforced strictly for
// types this grammar knows — an unknown type is the extension point
// working as designed, and the HOST decides at resolve whether it can
// provide it (fail-closed when required, skipped when optional). The
// one cross-entry invariant follows the loop: every credential inject
// domain must appear in the matching phase of the network policy's
// allow list — injection sets the header, egress is gated separately,
// and the two must agree per phase or a credential is presented to a
// domain the phase cannot reach.
func validateCapabilityBlock(d *Descriptor) error {
	var errs ValidationErrors
	needs := d.Capabilities
	// The platform contract is about the image config a workload's layers
	// come with, and a mixin's config never becomes the composed image's.
	// Rejected here rather than only in the kit suite because Merge
	// unions a set's declarations into one workload-kinded descriptor:
	// by the time an artifact is judged, a mixin's claim is
	// indistinguishable from the workload's own. A set is exempt — its
	// kind is derived from its members later.
	if d.Kind == KindMixin && HasCapability(needs, CapabilitySbx) {
		errs.add(fieldErrorf("capabilities", "%s is workload-only: a mixin's image config never becomes the composed image's, so the identity it would promise is not the one a host reads", CapabilitySbx))
	}
	seenSingleton := map[string]int{}
	seenExact := map[string]int{}
	seenCredential := map[string]int{}
	seenSSHAgent := map[string]int{}
	seenVolume := map[string]int{}
	seenHostMount := map[string]int{}
	seenSkills := map[string]int{}
	seenBundledSkills := map[string]int{}
	seenPort := map[string]int{}

	deferCrossChecks := false
	invalidCredentials := map[int]bool{}
	// lifecycle's interactive tail and agent-interactive-sessions'
	// newSession name the same launch, so a Kit stating both states it
	// once. Captured from the literal entries; a parameterized entry
	// defers decoding and is judged on the effective descriptor, as is a
	// lifecycle inside a group once one is selected.
	var newSession, interactive []string
	newSessionAt, interactiveAt := -1, -1
	// Both session capabilities enumerate the same sessions whichever
	// mode opened them, so a Kit declaring list on both states one
	// command. Compared on the decoded argv, so the string and list
	// spellings of one command are equal.
	var headlessList, interactiveList CommandLine
	interactiveListAt := -1
	for i, n := range needs {
		path := fmt.Sprintf("capabilities[%d]", i)
		if _, keys := environmentReferences(n.Config); keys {
			errs.add(fieldErrorf(path+".config", "environment references are not allowed in mapping keys"))
		}
		if n.Source != nil && n.Source.Path == "" {
			errs.add(fieldErrorf(path+".source.path", "source path is required"))
		}
		if !needType.MatchString(n.Type) {
			errs.add(fieldErrorf(path+".type", "capabilities[%d]: type %q is not <namespace>/<name>@<version>", i, n.Type))
			continue
		}
		duplicateSingleton := false
		if singletonCapabilities[n.Type] {
			if prev, dup := seenSingleton[n.Type]; dup {
				duplicateSingleton = true
				deferCrossChecks = deferCrossChecks || n.Type == CapabilityNetworkPolicy || n.Type == CapabilityNetworkPolicyV2
				errs.add(fieldErrorf(path+".type", "capabilities[%d]: %s already declared at capabilities[%d]", i, n.Type, prev))
			} else {
				seenSingleton[n.Type] = i
			}
		}
		if key := capabilitySurfaceEntry(n); true {
			if prev, dup := seenExact[key]; dup {
				if !duplicateSingleton {
					errs.add(fieldErrorf(path, "capabilities[%d]: identical to capabilities[%d]", i, prev))
				}
				// The first copy owns its value and cross-entry errors.
				if n.Type == CapabilityCredential {
					invalidCredentials[i] = true
				}
				continue
			}
			seenExact[key] = i
		}
		// Presence, not emptiness: `config: {}` and `config: null` are
		// config values, which these types' schemas (`not: {}`) reject,
		// and neither is distinguishable from an omitted key by the
		// decoded map alone. Testing length or nil-ness would let one
		// descriptor pass here and fail schema validation.
		if configlessCapabilities[n.Type] && n.ConfigStated() {
			errs.add(fieldErrorf(path+".config", "capabilities[%d]: %s takes no config", i, n.Type))
		}

		// Record literal phases before deferring parameterized values. A
		// merge can otherwise union overlapping entries and erase the error.
		if n.Type == CapabilitySSHAgent {
			var phaseOnly SSHAgent
			phaseConfig := Capability{Type: n.Type, Config: map[string]any{"phase": n.Config["phase"]}}
			if DecodeCapabilityConfig(phaseConfig, &phaseOnly) == nil {
				for _, phase := range phaseOnly.Phase {
					if ContainsArgRef(phase) || ContainsEnvRef(phase) {
						continue
					}
					if prev, dup := seenSSHAgent[phase]; dup && prev != i {
						errs.add(fieldErrorf(path+".config.phase", "capabilities[%d]: ssh-agent for phase %q already declared at capabilities[%d]", i, phase, prev))
					}
					seenSSHAgent[phase] = i
				}
			}
		}

		if n.Type == CapabilityCredential {
			service, literal := n.Config["service"].(string)
			phaseOnly, err := credentialPhases(n)
			if err == nil && literal && !ContainsArgRef(service) && !ContainsEnvRef(service) {
				for _, phase := range phaseOnly {
					if ContainsArgRef(phase) || ContainsEnvRef(phase) {
						continue
					}
					key := service + "\x00" + phase
					if prev, dup := seenCredential[key]; dup && prev != i {
						errs.add(fieldErrorf(path+".config.service", "capabilities[%d]: credential for service %q phase %q already declared at capabilities[%d]", i, service, phase, prev))
					}
					seenCredential[key] = i
				}
			}
		}

		if n.Type == CapabilityAgentContext {
			errs.add(validateContextDirectory(path, i, d.Kind, n))
		}

		// A parameterized entry — its config references a kit arg — defers
		// its typed validation to ValidateEffective, after expansion has
		// resolved every placeholder: a string placeholder cannot pass a
		// numeric field's decode, and a placeholder value cannot pass a
		// value check. Type grammar, arity, and dedup above still hold;
		// what the entry ASKS never goes unchecked, only WHEN it is
		// checked moves.
		if capabilityIsParameterized(n) {
			deferCrossChecks = true
			// Except for the rules about which fields are present:
			// those do not wait for values, and deferring them let a
			// contradictory pair through to a merge that normalizes
			// one of the two away — after which the evidence is gone
			// and the strict pass at the end sees a valid descriptor.
			errs.add(validatePresenceRules(path, i, n))
			continue
		}

		switch n.Type {
		case CapabilityNetworkPolicy:
			var p PhasedNetwork
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				deferCrossChecks = true
				continue
			}
		case CapabilityNetworkPolicyV2:
			var p PhasedNetworkV2
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				deferCrossChecks = true
				continue
			}
			if err := validateNetworkEntries(path, i, &p); err != nil {
				deferCrossChecks = true
				errs.add(err)
			}
		case CapabilityCredential:
			_, err := validateCredentialNeed(path, i, n)
			if err != nil {
				errs.add(err)
				invalidCredentials[i] = true
				continue
			}
		case CapabilitySSHAgent:
			_, err := validateSSHAgentNeed(path, i, n)
			if err != nil {
				errs.add(err)
				continue
			}

		case CapabilityVolume:
			var v Volume
			if err := DecodeCapabilityConfig(n, &v); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			if !strings.HasPrefix(v.Path, "/") {
				errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: volume path %q must be absolute", i, v.Path))
			}
			if v.Size != "" && !sizeBytes.MatchString(v.Size) {
				errs.add(fieldErrorf(path+".config.size", "capabilities[%d]: invalid size %q", i, v.Size))
			}
			if v.Mode != "" && !octalMode.MatchString(v.Mode) {
				errs.add(fieldErrorf(path+".config.mode", "capabilities[%d]: invalid octal mode %q", i, v.Mode))
			}
			volumePath := containerpath.Clean(v.Path)
			if prev, dup := seenVolume[volumePath]; dup {
				errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: volume for %q already declared at capabilities[%d]", i, v.Path, prev))
			}
			seenVolume[volumePath] = i
			if prev, dup := seenHostMount[containerpath.Clean(v.Path)]; dup {
				errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: volume path %q conflicts with host mount at capabilities[%d]", i, v.Path, prev))
			}
		case CapabilityHostMount:
			var mount HostMount
			if err := DecodeCapabilityConfig(n, &mount); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			if mount.Path == "/" || !canonicalAbsPath(mount.Path) || strings.ContainsRune(mount.Path, '\x00') {
				errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: host mount path %q must be absolute and canonical, without NUL or root /", i, mount.Path))
			}
			if _, stated := n.Config["mode"]; stated && !octalMode.MatchString(mount.Mode) {
				errs.add(fieldErrorf(path+".config.mode", "capabilities[%d]: invalid octal mode %q", i, mount.Mode))
			}
			if prev, dup := seenHostMount[mount.Path]; dup {
				errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: host mount for %q already declared at capabilities[%d]", i, mount.Path, prev))
			}
			seenHostMount[mount.Path] = i
			for volumePath, prev := range seenVolume {
				if containerpath.Clean(volumePath) == mount.Path {
					errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: host mount path %q conflicts with volume at capabilities[%d]", i, mount.Path, prev))
				}
			}
		case CapabilityAgentSkill:
			key, err := validateBundledSkill(path, n)
			if err != nil {
				errs.add(err)
				continue
			}
			seen := seenBundledSkills
			if prev, dup := seen[key]; dup {
				errs.add(fieldErrorf(path+".config", "%s %q already declared at capabilities[%d]", n.Type, key, prev))
			}
			seen[key] = i
		case CapabilityAgentSkills:
			var s AgentSkills
			if err := DecodeCapabilityConfig(n, &s); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			// Canonical form is required, not normalized in: /x/../skills
			// and /skills are different keys for one mount destination, so
			// an alias would slip past the duplicate check below and could
			// declare a second mode for the same path.
			if s.Path == "/" || !canonicalAbsPath(s.Path) {
				errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: skills path %q must be absolute and canonical (no . or .. segments, no trailing slash, not the root)", i, s.Path))
			}
			switch s.Mode {
			case "", SkillsReadOnly, SkillsReadWrite:
			default:
				errs.add(fieldErrorf(path+".config.mode", "capabilities[%d]: mode must be %q or %q, got %q", i, SkillsReadOnly, SkillsReadWrite, s.Mode))
			}
			// No dedup by path here: two entries naming one path with the
			// same mode are identical requests, which the exact-duplicate
			// rule above already rejects; naming one path with different
			// modes is contradictory, so the narrower one is the request
			// and stating both is an error.
			if prev, dup := seenSkills[s.Path]; dup {
				errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: skills for %q already declared at capabilities[%d]", i, s.Path, prev))
			}
			seenSkills[s.Path] = i
		case CapabilityPort:
			var p Port
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			if p.Container < 1 || p.Container > 65535 {
				errs.add(fieldErrorf(path+".config.container", "capabilities[%d]: container port %d out of range", i, p.Container))
			}
			switch p.Transport {
			case "", "tcp", "udp":
			default:
				errs.add(fieldErrorf(path+".config.transport", "capabilities[%d]: transport must be tcp or udp, got %q", i, p.Transport))
			}
			key := PortKey(p)
			if prev, dup := seenPort[key]; dup {
				errs.add(fieldErrorf(path+".config.container", "capabilities[%d]: port %d already declared at capabilities[%d]", i, p.Container, prev))
			}
			seenPort[key] = i
		case CapabilityUSBDevice:
			var u USBDevice
			if err := DecodeCapabilityConfig(n, &u); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			hasID := u.VendorID != "" || u.ProductID != ""
			if hasID == (u.Class != "") {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: declare either vendorId/productId or class, not both and not neither", i))
			}
			if (u.VendorID != "") != (u.ProductID != "") {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: vendorId and productId go together", i))
			}
		case CapabilityResources:
			var r Resources
			if err := DecodeCapabilityConfig(n, &r); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			if r.CPU < 0 {
				errs.add(fieldErrorf(path+".config.cpu", "capabilities[%d]: cpu must be >= 0", i))
			}
			if r.Memory != "" && !sizeBytes.MatchString(r.Memory) {
				errs.add(fieldErrorf(path+".config.memory", "capabilities[%d]: invalid memory %q", i, r.Memory))
			}
		case CapabilityAgentSessions:
			var a AgentSessions
			if err := DecodeCapabilityConfig(n, &a); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			if len(a.Prompt) == 0 && len(a.Resume) == 0 && len(a.Continue) == 0 && len(a.List) == 0 {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: agent-sessions declares no verbs; drop the entry instead", i))
			}
			// The placeholder is the verb's whole point: a prompt verb
			// that never receives the prompt (or a resume that never
			// names the session) runs the agent with the caller's input
			// silently discarded.
			if len(a.Prompt) > 0 && !argvContains(a.Prompt, SessionPromptPlaceholder) {
				errs.add(fieldErrorf(path+".config.prompt", "capabilities[%d]: prompt must reference %s", i, SessionPromptPlaceholder))
			}
			if len(a.Resume) > 0 && !argvContains(a.Resume, SessionIDPlaceholder) {
				errs.add(fieldErrorf(path+".config.resume", "capabilities[%d]: resume must reference %s", i, SessionIDPlaceholder))
			}
			headlessList = a.List
		case CapabilityAgentInteractiveSessions:
			var a AgentInteractiveSessions
			if err := DecodeCapabilityConfig(n, &a); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			errs.add(validateInteractiveSessionsAuthored(path, i, n))
			// Presence, not length: an empty tail is a verb ("the launch
			// argv alone"), so a declaration holding only newSession: []
			// says something.
			if a.Prompt == nil && a.Resume == nil && a.Continue == nil && a.NewSession == nil && a.SessionPicker == nil && len(a.List) == 0 {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: agent-interactive-sessions declares no verbs; drop the entry instead", i))
			}
			// Same reasoning as agent-sessions: a prompt verb that never
			// receives the prompt discards the caller's input. Judged on
			// presence here, so prompt: [] cannot carry it and is refused.
			if a.Prompt != nil && !argvContains(a.Prompt, SessionPromptPlaceholder) {
				errs.add(fieldErrorf(path+".config.prompt", "capabilities[%d]: prompt must reference %s", i, SessionPromptPlaceholder))
			}
			if a.Resume != nil && !argvContains(a.Resume, SessionIDPlaceholder) {
				errs.add(fieldErrorf(path+".config.resume", "capabilities[%d]: resume must reference %s", i, SessionIDPlaceholder))
			}
			newSession, newSessionAt = a.NewSession, i
			interactiveList, interactiveListAt = a.List, i
		case CapabilityLifecycle:
			var l Lifecycle
			if err := DecodeCapabilityConfig(n, &l); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			interactive, interactiveAt = l.Interactive, i
			if len(l.Install) == 0 && len(l.Startup) == 0 && len(l.Files) == 0 && len(l.Interactive) == 0 {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: lifecycle declares no hooks, no files, and no interactive tail; drop the entry instead", i))
			}
			errs.add(validateLifecycle(path, i, &l))
		case CapabilityAgentContext:
			var a AgentContext
			if err := DecodeCapabilityConfig(n, &a); err != nil {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
				continue
			}
			if a.ContentFile != "" && a.Content != "" {
				errs.add(fieldErrorf(path+".config", "capabilities[%d]: agent-context contentFile and content are mutually exclusive", i))
			}
		}
	}

	one, hasOne := seenSingleton[CapabilityNetworkPolicy]
	two, hasTwo := seenSingleton[CapabilityNetworkPolicyV2]
	if hasOne && hasTwo {
		deferCrossChecks = true
		errs.add(fieldErrorf(fmt.Sprintf("capabilities[%d].type", two),
			"capabilities[%d]: %s cannot be declared beside %s at capabilities[%d]; a descriptor states one network-policy version",
			two, CapabilityNetworkPolicyV2, CapabilityNetworkPolicy, one))
	}

	// Compared on literal values only, so an unrelated deferral does not
	// suppress it. Whenever both are stated the argvs must agree, an empty
	// tail included: newSession: [--tui] beside interactive: [] would
	// launch two different ways. Composition makes the same comparison
	// (see checkInteractiveAgreement) on the decoded asks.
	if newSession != nil && interactive != nil && !slices.Equal(newSession, interactive) {
		errs.add(fieldErrorf(fmt.Sprintf("capabilities[%d].config.newSession", newSessionAt),
			"capabilities[%d]: newSession %q disagrees with lifecycle interactive %q at capabilities[%d]; they name the same launch",
			newSessionAt, newSession, interactive, interactiveAt))
	}

	if len(headlessList) > 0 && len(interactiveList) > 0 && !slices.Equal(headlessList, interactiveList) {
		errs.add(fieldErrorf(fmt.Sprintf("capabilities[%d].config.list", interactiveListAt),
			"capabilities[%d]: list %q differs from agent-sessions list %q; both capabilities enumerate the same sessions, so they name one command",
			interactiveListAt, []string(interactiveList), []string(headlessList)))
	}

	// The inject⊆allow invariant needs literal domains on both sides;
	// with any parameterized entry in play it runs on the effective
	// descriptor instead, where every domain is literal. An invalid or
	// ambiguous policy cannot judge credentials; an invalid credential
	// only prevents checking that credential, not its valid siblings.
	if deferCrossChecks || d.declarationsOnly {
		return errs.err()
	}
	errs.add(validateInjectWithinAllow(needs, invalidCredentials))
	return errs.err()
}

// Validate literal destination structure even when another config field
// defers decoding until argument expansion.
func validateContextDirectory(field string, i int, kind string, n Capability) error {
	value, stated := n.Config["directory"]
	if !stated {
		if filename, ok := n.Config["filename"].(string); ok && filename != "" && kind == KindMixin {
			return fieldErrorf(field+".config.filename", "capabilities[%d]: agent-context filename without directory is workload-kit-only; an agent mixin must state an explicit directory", i)
		}
		return nil
	}
	var errs ValidationErrors
	directory, isString := value.(string)
	if !isString {
		errs.add(fieldErrorf(field+".config.directory", "capabilities[%d]: agent-context directory must be a string", i))
	} else if !ContainsArgRef(directory) && !ContainsEnvRef(directory) {
		if !canonicalAbsPath(directory) || strings.ContainsAny(directory, "\\\x00") {
			errs.add(fieldErrorf(field+".config.directory", "capabilities[%d]: agent-context directory must be an absolute, canonical path", i))
		}
	}
	filename, ok := n.Config["filename"].(string)
	if !ok || filename == "" {
		errs.add(fieldErrorf(field+".config.filename", "capabilities[%d]: agent-context directory requires filename in the same entry", i))
	} else if !ContainsArgRef(filename) && !ContainsEnvRef(filename) {
		if filename == "." || filename == ".." || strings.ContainsAny(filename, "/\\\x00") {
			errs.add(fieldErrorf(field+".config.filename", "capabilities[%d]: agent-context filename must be a single filename when directory is stated", i))
		}
	}
	return errs.err()
}

// httpMethods are the tokens a NetworkEntry may name. Canonical uppercase is
// required rather than normalized in, so a published rule reads the same
// as the one enforcement matches.
var httpMethods = map[string]bool{
	"GET":     true,
	"HEAD":    true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"OPTIONS": true,
	"TRACE":   true,
	"CONNECT": true,
}

// HTTPMethods returns the method tokens a NetworkEntry may name, excluding
// MethodAny, sorted. The published schema's enum is pinned against it.
func HTTPMethods() []string {
	out := make([]string, 0, len(httpMethods))
	for m := range httpMethods {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// validateNetworkEntries checks both phases' entries.
//
// A bounded entry names its hosts exactly. A pattern cannot be bounded
// and unbounded at once — an entry bounding "*.example.com" to GET would
// overlap any other entry naming a host inside it, and nothing here can
// rank the two, because the matcher deciding what a pattern covers is
// runtime-owned. The restriction is per-entry rather than per-phase: an
// unbounded entry beside a bounded one keeps @1's patterns, because it
// raises no question of rank with anything.
//
// Deny entries may name patterns however they are bounded: a deny wins
// outright, so an overlap between two of them decides the same way.
func validateNetworkEntries(path string, i int, p *PhasedNetworkV2) error {
	var errs ValidationErrors
	for _, phase := range []struct {
		name  string
		rules *NetworkRulesV2
	}{{"install", p.Install}, {"runtime", p.Runtime}} {
		if phase.rules == nil {
			continue
		}
		for j, e := range phase.rules.Allow {
			at := fmt.Sprintf("%s.config.%s.allow[%d]", path, phase.name, j)
			errs.add(validateNetworkEntry(at, i, phase.name, e))
			if !e.Bounded() {
				continue
			}
			for _, h := range e.Hosts {
				if isHostPattern(h) {
					errs.add(fieldErrorf(at+".hosts",
						"capabilities[%d]: %s allow entry bounds the pattern %q; an entry stating methods or paths names its hosts exactly, or the pattern would be bounded and unbounded at once",
						i, phase.name, h))
				}
			}
		}
		for j, e := range phase.rules.Deny {
			at := fmt.Sprintf("%s.config.%s.deny[%d]", path, phase.name, j)
			errs.add(validateNetworkEntry(at, i, phase.name, e))
		}
	}
	return errs.err()
}

// isHostPattern reports whether an entry is a glob rather than one
// literal host.
func isHostPattern(h string) bool { return strings.Contains(h, "*") }

// validateNetworkEntryShape judges which fields an entry states and
// how many things they hold — the part of a policy a placeholder
// cannot hide, and the part that decides how wide the entry is.
//
// Checked before expansion so merge normalization cannot discard an
// invalid entry before it is rejected.
func validateNetworkEntryShape(path string, i int, phase string, r NetworkEntry) error {
	var errs ValidationErrors
	if len(r.Hosts) == 0 {
		errs.add(fieldErrorf(path+".hosts", "capabilities[%d]: %s entry declares no hosts", i, phase))
	}
	for _, h := range r.Hosts {
		if h == "" {
			errs.add(fieldErrorf(path+".hosts", "capabilities[%d]: %s entry has an empty host", i, phase))
		}
	}
	// Wildcard semantics belong to OMITTED fields alone: a generated
	// document stating methods: [] would otherwise silently grant every
	// method, which is the opposite of what an empty restriction reads as.
	if r.Methods != nil && len(r.Methods) == 0 {
		// Omitting methods only unbounds the entry when nothing else
		// bounds it; with paths stated it fails the paths-without-methods
		// rule below, so the remedy has to name both.
		remedy := "omit the field to leave the entry unbounded, or name the methods"
		if len(r.Paths) > 0 {
			remedy = "name the methods the paths bound, or omit paths too to leave the entry unbounded"
		}
		errs.add(fieldErrorf(path+".methods",
			"capabilities[%d]: %s entry states an empty methods list; %s", i, phase, remedy))
	}
	if r.Paths != nil && len(r.Paths) == 0 {
		errs.add(fieldErrorf(path+".paths",
			"capabilities[%d]: %s entry states an empty paths list; omit the field for every path, or name the paths", i, phase))
	}
	if len(r.Paths) > 0 && len(r.Methods) == 0 {
		errs.add(fieldErrorf(path+".paths",
			"capabilities[%d]: %s entry states paths without methods; name the methods the paths bound, or %s for every method",
			i, phase, "methods: ["+MethodAny+"]"))
	}
	return errs.err()
}

func validateNetworkEntry(path string, i int, phase string, r NetworkEntry) error {
	var errs ValidationErrors
	errs.add(validateNetworkEntryShape(path, i, phase, r))
	seen := map[string]bool{}
	for _, m := range r.Methods {
		if m != MethodAny && !httpMethods[m] {
			errs.add(fieldErrorf(path+".methods",
				"capabilities[%d]: %s entry method %q is not an uppercase HTTP method or %s",
				i, phase, m, MethodAny))
		}
		if seen[m] {
			errs.add(fieldErrorf(path+".methods", "capabilities[%d]: %s entry repeats method %q", i, phase, m))
		}
		seen[m] = true
	}
	if seen[MethodAny] && len(r.Methods) > 1 {
		errs.add(fieldErrorf(path+".methods",
			"capabilities[%d]: %s entry names %s beside a specific method; %s already covers every method",
			i, phase, MethodAny, MethodAny))
	}
	for _, p := range r.Paths {
		if !strings.HasPrefix(p, "/") {
			errs.add(fieldErrorf(path+".paths", "capabilities[%d]: %s entry path %q must start with \"/\"", i, phase, p))
		}
	}
	return errs.err()
}

// validateInteractiveSessionsAuthored judges the authored values of an
// agent-interactive-sessions entry whose meaning is presence. The typed
// decode reads a stated null as absent (and a null element as ""), so a
// null is refused here rather than silently becoming "unsupported"; and
// list is a complete command, so a present one must name a command. The
// string form decodes to ["sh", "-c", s], which a length check would let
// an empty string slip through as a three-element argv. These do not wait
// for argument values, so a parameterized entry is checked the same way.
func validateInteractiveSessionsAuthored(path string, i int, n Capability) error {
	var errs ValidationErrors
	for _, key := range []string{"prompt", "resume", "continue", "newSession", "sessionPicker", "list"} {
		if value, stated := n.Config[key]; stated && hasNull(value) {
			errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s must not be null or contain null", i, key))
		}
	}
	if list, stated := n.Config["list"]; stated && emptyCommand(list) {
		errs.add(fieldErrorf(path+".config.list", "capabilities[%d]: list must name a command", i))
	}
	return errs.err()
}

// validatePresenceRules judges what an entry states rather than what
// it states it as, which is the part of a config a placeholder does
// not hide.
func validatePresenceRules(path string, i int, n Capability) error {
	var errs ValidationErrors
	if n.Type == CapabilityAgentInteractiveSessions {
		// Whether a verb is stated is its meaning, so its nulls and its
		// empty command are judged now, whatever another field defers.
		return validateInteractiveSessionsAuthored(path, i, n)
	}
	if n.Type == CapabilityCredential {
		phases, err := credentialPhases(n)
		if err != nil {
			return fieldErrorf(path+".config.phase", "capabilities[%d]: %v", i, err)
		}
		return validatePhases(path, i, phases)
	}
	if n.Type == CapabilityNetworkPolicyV2 {
		var p PhasedNetworkV2
		if err := DecodeCapabilityConfig(n, &p); err != nil {
			// Undecodable is the merge's to report, in the words it
			// has for a re-export it cannot carry.
			return errs.err()
		}
		for _, phase := range []struct {
			name  string
			rules *NetworkRulesV2
		}{{"install", p.Install}, {"runtime", p.Runtime}} {
			if phase.rules == nil {
				continue
			}
			for _, group := range []struct {
				name    string
				entries []NetworkEntry
			}{{"allow", phase.rules.Allow}, {"deny", phase.rules.Deny}} {
				for j, e := range group.entries {
					at := fmt.Sprintf("%s.config.%s.%s[%d]", path, phase.name, group.name, j)
					errs.add(validateNetworkEntryShape(at, i, phase.name, e))
				}
			}
		}
		return errs.err()
	}
	if n.Type == CapabilitySSHAgent {
		if err := validateSSHAgentNulls(path, i, n); err != nil {
			return err
		}
		// Only the boolean placeholder prevents typed decoding. Choose a
		// presence-compatible value in a copy; expansion still judges the
		// actual boolean, while literal siblings and duplicates are checked now.
		if v, ok := n.Config["unrestricted"].(string); ok && ContainsArgRef(v) {
			config := make(map[string]any, len(n.Config))
			for k, v := range n.Config {
				config[k] = v
			}
			_, sign := config["sign"]
			_, authenticate := config["authenticate"]
			config["unrestricted"] = !sign && !authenticate
			n.Config = config
		}
		_, err := validateSSHAgentNeed(path, i, n)
		return err
	}
	if n.Type == CapabilityAgentSkill {
		return validateParameterizedSkillSource(path, n)
	}
	if n.Type != CapabilityAgentContext {
		return errs.err()
	}
	// Read as raw keys: decoding wants types the placeholder does not
	// have, and presence is all this rule asks about.
	_, hasFile := n.Config["contentFile"]
	_, hasContent := n.Config["content"]
	if hasFile && hasContent {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: agent-context contentFile and content are mutually exclusive", i))
	}
	return errs.err()
}

// capabilityIsParameterized reports whether the entry's config references
// a Kit argument or environment value in its canonical JSON rendering.
func capabilityIsParameterized(n Capability) bool {
	if len(n.Config) == 0 {
		return false
	}
	data, err := json.Marshal(n.Config)
	if err != nil {
		return false
	}
	values, _ := environmentReferences(n.Config)
	return ContainsArgRef(string(data)) || values
}

// validateCredentialNeed decodes and checks one credential entry.
func validateCredentialNeed(path string, i int, n Capability) (*Credential, error) {
	var errs ValidationErrors
	var c Credential
	if err := DecodeCapabilityConfig(n, &c); err != nil {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
		return nil, errs.err()
	}
	if c.Service == "" {
		errs.add(fieldErrorf(path+".config.service", "capabilities[%d]: credential service is required", i))
	} else if !handleName.MatchString(c.Service) {
		errs.add(fieldErrorf(path+".config.service", "capabilities[%d]: invalid service name %q", i, c.Service))
	}
	errs.add(validatePhases(path, i, c.Phase))
	if c.APIKey == nil && c.OAuth == nil {
		errs.add(fieldErrorf(path+".config", "capabilities[%d] (%s): declare apiKey or oauth", i, c.Service))
	}
	// Present nulls decay through typed decoding — to nil pointers, empty
	// strings, or nil maps — and would masquerade as omitted fields,
	// while the schema rejects null at every credential position. One
	// walk judges the whole raw shape; the single subtree where null is
	// legal is credentialFile.structure under the json encoding, whose
	// values the schema leaves open (the toml walk judges them
	// separately).
	for _, at := range nullConfigPaths("config", n.Config, "config.oauth.credentialFile.structure") {
		errs.add(fieldErrorf(path+"."+at, "capabilities[%d] (%s): %s is null; omit the field or declare a value", i, c.Service, strings.TrimPrefix(at, "config.")))
	}
	if c.APIKey != nil {
		// An empty name with inject rules is the inject-only shape: the
		// key exists solely as outbound rewrites, with no environment
		// presence — not even a sentinel.
		if c.APIKey.Name == "" && len(c.APIKey.Inject) == 0 {
			errs.add(fieldErrorf(path+".config.apiKey", "capabilities[%d] (%s): apiKey needs a name, inject rules, or both", i, c.Service))
		}
		if c.APIKey.Name != "" && !envVarName.MatchString(c.APIKey.Name) {
			errs.add(fieldErrorf(path+".config.apiKey.name", "capabilities[%d] (%s): apiKey.name %q is not a valid env var name", i, c.Service, c.APIKey.Name))
		}
		for j, inj := range c.APIKey.Inject {
			if inj.Domain == "" {
				errs.add(fieldErrorf(fmt.Sprintf("%s.config.apiKey.inject[%d]", path, j), "capabilities[%d] (%s): inject[%d]: domain is required", i, c.Service, j))
			}
		}
	}
	if c.OAuth != nil && c.OAuth.TokenEndpoint != nil && c.OAuth.TokenEndpoint.Host == "" {
		errs.add(fieldErrorf(path+".config.oauth.tokenEndpoint", "capabilities[%d] (%s): oauth.tokenEndpoint.host is required", i, c.Service))
	}
	if c.OAuth != nil && c.OAuth.CredentialFile != nil {
		// An explicitly empty format decays to the same "" as an omitted
		// one through typed decoding, but the published schema's enum
		// rejects it — one grammar, so the raw shape is judged first.
		// (Present nulls are already rejected by the config-wide walk.)
		if oauth, ok := n.Config["oauth"].(map[string]any); ok {
			if cf, ok := oauth["credentialFile"].(map[string]any); ok {
				if v, present := cf["format"]; present && v == "" {
					errs.add(fieldErrorf(path+".config.oauth.credentialFile.format", "capabilities[%d] (%s): credentialFile.format is empty; omit the field for json or name an encoding", i, c.Service))
				}
			}
		}
		switch c.OAuth.CredentialFile.Format {
		case "", "json", "toml":
		default:
			errs.add(fieldErrorf(path+".config.oauth.credentialFile.format", "capabilities[%d] (%s): credentialFile.format %q is not json or toml", i, c.Service, c.OAuth.CredentialFile.Format))
		}
		// An omitted structure is schema-legal (path alone is required),
		// so there is nothing to walk; the null check is for values
		// inside a structure that exists.
		if c.OAuth.CredentialFile.Format == "toml" && c.OAuth.CredentialFile.Structure != nil {
			for _, at := range nullConfigPaths("structure", c.OAuth.CredentialFile.Structure, "") {
				errs.add(fieldErrorf(path+".config.oauth.credentialFile."+at, "capabilities[%d] (%s): %s is null, which TOML cannot represent", i, c.Service, at))
			}
		}
	}
	return &c, errs.err()
}

// nullConfigPaths finds nulls before typed decoding can make them look
// absent. JSON credential structures allow nulls; TOML structures do not.
// Skip exempts the contents of one subtree, but not a null subtree itself.
func nullConfigPaths(at string, v any, skip string) []string {
	var paths []string
	switch t := v.(type) {
	case nil:
		return []string{at}
	case map[string]any:
		if at == skip {
			return nil
		}
		for _, key := range slices.Sorted(maps.Keys(t)) {
			paths = append(paths, nullConfigPaths(at+"."+key, t[key], skip)...)
		}
	case []any:
		for i, value := range t {
			paths = append(paths, nullConfigPaths(fmt.Sprintf("%s[%d]", at, i), value, skip)...)
		}
	}
	return paths
}

// validateInjectWithinAllow is the cross-entry invariant: every inject
// domain must appear in the matching phase of the network policy's
// allow list. It reads the policy in the version-agnostic shape, so the
// invariant holds for a kit on either version.
func validateInjectWithinAllow(needs []Capability, invalidCredentials map[int]bool) error {
	var errs ValidationErrors
	policy, err := NetworkPolicyV2Of(needs)
	if err != nil {
		return err
	}
	for i, n := range needs {
		if n.Type != CapabilityCredential || invalidCredentials[i] {
			continue
		}
		var c Credential
		if err := DecodeCapabilityConfig(n, &c); err != nil {
			errs.add(err)
			continue
		}
		if c.APIKey == nil {
			continue
		}
		for _, phase := range c.Phase {
			allow := phaseAllow(policy, phase)
			for j, inj := range c.APIKey.Inject {
				// A bare "*" (or "**") allow entry grants every host, so any
				// inject domain is covered. Narrower glob patterns are not
				// expanded here: the exact-match rule keeps published inject
				// domains auditable against the allow list without
				// reimplementing the enforcement matcher.
				if !allow[stripPort(inj.Domain)] && !allow["*"] && !allow["**"] {
					errs.add(fieldErrorf(fmt.Sprintf("capabilities[%d].config.apiKey.inject[%d].domain", i, j),
						"capabilities[%d] (%s): inject domain %q is not in the network policy's %s allow list", i, c.Service, inj.Domain, phase))
				}
			}
		}
	}
	return errs.err()
}

func phaseAllow(n *PhasedNetworkV2, phase string) map[string]bool {
	allow := map[string]bool{}
	if n == nil {
		return allow
	}
	var rules *NetworkRulesV2
	if phase == "install" {
		rules = n.Install
	} else {
		rules = n.Runtime
	}
	if rules == nil {
		return allow
	}
	// Every allowed host counts, bounded or not: an inject domain the
	// phase reaches only for GET is still a domain it reaches, and
	// whether the credential's own requests match the bound is the
	// runtime's question at enforcement, not one this check can answer.
	for _, e := range rules.Allow {
		for _, h := range e.Hosts {
			allow[stripPort(h)] = true
		}
	}
	return allow
}

// stripPort normalizes "host:443" to "host" for allow-list membership.
func stripPort(domain string) string {
	host, _, found := strings.Cut(domain, ":")
	if found {
		return host
	}
	return domain
}

func validateArgs(args map[string]Arg) error {
	var errs ValidationErrors
	for _, name := range slices.Sorted(maps.Keys(args)) {
		a := args[name]
		path := "args." + name
		if !envVarName.MatchString(name) {
			errs.add(fieldErrorf(path, "args.%s: invalid arg name", name))
		}
		if a.Required && a.Default != nil {
			errs.add(fieldErrorf(path, "args.%s: required and default are mutually exclusive", name))
		}
		if len(a.Enum) > 0 && a.Pattern != "" {
			errs.add(fieldErrorf(path, "args.%s: enum and pattern are mutually exclusive", name))
		}
		if a.Pattern != "" {
			if _, err := regexp.Compile(a.Pattern); err != nil {
				errs.add(fieldErrorf(path+".pattern", "args.%s: invalid pattern: %v", name, err))
			}
		}
		if a.Env != "" && a.BuildArg != "" {
			errs.add(fieldErrorf(path, "args.%s: env and buildArg are mutually exclusive; an arg resolves in one phase", name))
		}
		if a.Env != "" && !envVarName.MatchString(a.Env) {
			errs.add(fieldErrorf(path+".env", "args.%s: env %q is not a valid env var name", name, a.Env))
		}
		if a.BuildArg != "" && !envVarName.MatchString(a.BuildArg) {
			errs.add(fieldErrorf(path+".buildArg", "args.%s: buildArg %q is not a valid build-arg name", name, a.BuildArg))
		}
	}
	return errs.err()
}

// validateLifecycle checks one lifecycle capability's hooks and files.
// base is the entry's dotted path ("capabilities[N]"), so errors point
// into the config the author wrote.
func validateLifecycle(base string, i int, l *Lifecycle) error {
	var errs ValidationErrors
	for j, h := range l.Install {
		p := fmt.Sprintf("%s.config.install[%d]", base, j)
		if len(h.Command) == 0 {
			errs.add(fieldErrorf(p, "capabilities[%d]: install[%d]: command is required", i, j))
		}
		for k, e := range h.Env {
			if !envVarName.MatchString(e) {
				errs.add(fieldErrorf(fmt.Sprintf("%s.env[%d]", p, k), "capabilities[%d]: install[%d]: env entry %q is not a valid env var name", i, j, e))
			}
		}
	}
	for j, h := range l.Startup {
		p := fmt.Sprintf("%s.config.startup[%d]", base, j)
		if len(h.Command) == 0 {
			errs.add(fieldErrorf(p, "capabilities[%d]: startup[%d]: command is required", i, j))
		}
		for k, e := range h.Env {
			if !envVarName.MatchString(e) {
				errs.add(fieldErrorf(fmt.Sprintf("%s.env[%d]", p, k), "capabilities[%d]: startup[%d]: env entry %q is not a valid env var name", i, j, e))
			}
		}
	}
	for j, f := range l.Files {
		p := fmt.Sprintf("%s.config.files[%d]", base, j)
		if !strings.HasPrefix(f.Path, "/") {
			errs.add(fieldErrorf(p+".path", "capabilities[%d]: files[%d]: path %q must be absolute", i, j, f.Path))
		}
		if f.Mode != "" && !octalMode.MatchString(f.Mode) {
			errs.add(fieldErrorf(p+".mode", "capabilities[%d]: files[%d]: invalid octal mode %q", i, j, f.Mode))
		}
	}
	return errs.err()
}
