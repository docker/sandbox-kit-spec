package spec

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DpkgInfoDir is where dpkg keeps one <package>.list per installed
// package, naming every path the package put on disk. The status file
// says what is installed; this is the only place that says whose a file
// is.
const DpkgInfoDir = "/var/lib/dpkg/info"

// maxProvidesFromLinks bounds how many symbolic links one path is
// followed through. Deeper than an alternatives chain ever is, and
// small enough that a link cycle ends in a diagnostic rather than a
// build that never does.
const maxProvidesFromLinks = 16

// validateProvidesFrom holds providesFrom to its grammar (§9.7).
//
// The kind rules are here rather than at resolution because they are
// knowable from the descriptor alone: a mixin's layers are a delta and
// a set builds nothing, so neither has a filesystem whose package
// database is an inventory, and saying so before a build runs is kinder
// than saying it after.
func validateProvidesFrom(d *Descriptor) error {
	var errs ValidationErrors
	if len(d.ProvidesFrom) == 0 {
		return nil
	}
	switch {
	case len(d.Kits) > 0:
		errs.add(fieldErrorf("providesFrom", "providesFrom reads the filesystem this kit builds, and a set builds none: its content is the kits it lists, and the workload among them already states what it carries"))
	case d.Kind != KindWorkload:
		errs.add(fieldErrorf("providesFrom", "providesFrom is for kind: %s only: a %s's layers are a delta, where a package database is whatever the recipe happened to rewrite rather than an inventory", KindWorkload, d.Kind))
	}

	errs.add(RequireDistinctProvidesFrom(d, d.ProvidesFrom))
	seen := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(d.ProvidesFrom)) {
		at := "providesFrom." + name
		file := d.ProvidesFrom[name]
		if err := capabilityNameError(name); err != nil {
			errs.add(fieldErrorf(at, "providesFrom key %q: %v", name, err))
			continue
		}
		if IsDerivedProvide(name) {
			namespace, _, _ := strings.Cut(name, "/")
			errs.add(fieldErrorf(at, "providesFrom key %q is in the %s/ namespace, which publishing fills from the image's package database; name the capability the file stands for instead", name, namespace))
			continue
		}
		normalized := NormalizeCapabilityName(name)
		if prev, dup := seen[normalized]; dup {
			errs.add(fieldErrorf(at, "providesFrom keys %q and %q are the same capability", prev, name))
		}
		seen[normalized] = name
		switch {
		case ContainsKitPlaceholder(file):
			errs.add(fieldErrorf(at, "providesFrom.%s: arg references are not allowed in providesFrom; the path names a file in the image, not an input", name))
		case !canonicalAbsPath(file) || file == "/":
			errs.add(fieldErrorf(at, "providesFrom.%s: %q is not an absolute, canonical path to a file", name, file))
		}
	}
	return errs.err()
}

// RequireDistinctProvidesFrom refuses a providesFrom key that names a
// capability the descriptor's provides already states: a capability has
// one version, and two entries for it would leave a resolver to pick.
//
// Separate from the grammar check, and exported, because the frontend
// asks it a second time on the build-expanded descriptor: an authored
// `${{ kit.args.tool }}@1.0.0` holds no name to compare until the
// build-phase value is in it.
func RequireDistinctProvidesFrom(d *Descriptor, from map[string]string) error {
	var errs ValidationErrors
	authored := map[string]int{}
	for i, s := range d.Provides {
		if p, err := ParseProvide(s); err == nil {
			authored[p.Name] = i
		}
	}
	for _, name := range slices.Sorted(maps.Keys(from)) {
		if capabilityNameError(name) != nil {
			continue
		}
		if i, dup := authored[NormalizeCapabilityName(name)]; dup {
			errs.add(fieldErrorf("providesFrom."+name, "providesFrom key %q is already stated by provides[%d]; a capability has one version, so state it in one place", name, i))
		}
	}
	return errs.err()
}

// providesFromField is the descriptor key, as an author writes it.
const providesFromField = "providesFrom"

// maxYAMLIndirection bounds how far a merge key or an alias is followed
// while looking for the field. YAML admits an anchor that contains its
// own alias, and a walk with no bound would not return from one.
const maxYAMLIndirection = 32

// WithoutProvidesFrom returns a descriptor with its providesFrom field
// removed, for the published form (§9.7).
//
// The node tree is edited rather than re-encoded from the struct, for
// the reason every other publish-time rewrite here is: the copy a
// reader finds staged in the image keeps the author's own comments and
// ordering. A descriptor without the field comes back byte for byte.
//
// The field is removed wherever the root mapping gets it from, not only
// where it is written at the top level. A YAML merge key hands the root
// every key of another mapping, so `<<: {providesFrom: …}` decodes to
// the same descriptor as the plain spelling and has to publish the same
// way. And the removal takes nothing else with it: an alias elsewhere
// in the document that points into what is removed is given its own
// copy first, since an anchor that disappears leaves every alias to it
// naming nothing and the document no longer parsing.
func WithoutProvidesFrom(raw []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("reparse descriptor: %w", err)
	}
	root := documentRoot(&doc)
	if root == nil || !mappingCarries(root, providesFromField, 0) {
		return raw, nil
	}

	// Three passes, in this order. What is about to change is found
	// first and nothing is touched, so that the second pass can copy an
	// aliased node as the author wrote it; only then is anything removed.
	edit := &fieldRemoval{field: providesFromField, removed: map[*yaml.Node]bool{}, edited: map[*yaml.Node]bool{}}
	edit.plan(root, 0)
	edit.detachAliases(&doc)
	edit.strip(root, 0)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("re-encode descriptor without %s: %w", providesFromField, err)
	}
	// Parsed once more before it is handed back: this is the document
	// that gets staged and signed, and a rewrite that left it unreadable
	// should say so here rather than as a decode failure with no cause.
	var check yaml.Node
	if err := yaml.Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("descriptor no longer parses with %s removed: %w", providesFromField, err)
	}
	if mappingCarries(documentRoot(&check), providesFromField, 0) {
		return nil, fmt.Errorf("%s could not be removed from the descriptor; write it as a plain top-level field", providesFromField)
	}
	return out, nil
}

// carriesProvidesFrom reports whether a raw descriptor has the field at
// all — written out or inherited through a merge key, with entries or
// without. Presence is the question for the published form, where a
// reader built before the field refuses the key whatever is under it,
// and the decoded struct cannot answer it: an empty map and a null
// decode exactly like a field that was never there.
func carriesProvidesFrom(raw []byte) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		// Unparseable bytes are another rule's to report.
		return false
	}
	return mappingCarries(documentRoot(&doc), providesFromField, 0)
}

// documentRoot returns the mapping a descriptor document consists of,
// or nil when it is not one.
func documentRoot(doc *yaml.Node) *yaml.Node {
	root := doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	root = aliasTarget(root)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

// aliasTarget follows an alias to the node it names.
func aliasTarget(n *yaml.Node) *yaml.Node {
	for range maxYAMLIndirection {
		if n == nil || n.Kind != yaml.AliasNode {
			return n
		}
		n = n.Alias
	}
	return nil
}

// isMergeKey reports whether a mapping key is YAML's merge key. Only
// the plain scalar is: a quoted "<<" is an ordinary string key, which
// is the distinction the decoder draws too.
func isMergeKey(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Value == "<<" && n.Tag == "!!merge"
}

// mergeSources returns the mappings a merge value contributes: the
// value itself, or each element when it is a sequence of them.
func mergeSources(value *yaml.Node) []*yaml.Node {
	if value.Kind == yaml.SequenceNode {
		return value.Content
	}
	return []*yaml.Node{value}
}

// mappingCarries reports whether a mapping has key, directly or through
// any mapping it merges in.
func mappingCarries(m *yaml.Node, key string, depth int) bool {
	m = aliasTarget(m)
	if m == nil || m.Kind != yaml.MappingNode || depth > maxYAMLIndirection {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if isMergeKey(k) {
			for _, source := range mergeSources(v) {
				if mappingCarries(source, key, depth+1) {
					return true
				}
			}
			continue
		}
		if k.Value == key {
			return true
		}
	}
	return false
}

// fieldRemoval takes one key out of a mapping and everything the
// mapping merges in.
type fieldRemoval struct {
	field string
	// removed holds every node that leaves the document: the field's
	// key and value, and all that is under the value.
	removed map[*yaml.Node]bool
	// edited holds the anchored nodes that are changed in place: a
	// mapping the field is taken out of, or a merge key's sequence that
	// loses or swaps an element. An alias to one of these would see it
	// change underneath.
	edited map[*yaml.Node]bool
}

// plan records what strip will remove from m and which shared mappings
// it will edit, without changing anything.
//
// A merge source reached through an alias is not recorded for itself:
// strip works on a copy of it, so the anchored original stays as it was
// for whatever else refers to it.
func (e *fieldRemoval) plan(m *yaml.Node, depth int) {
	if depth > maxYAMLIndirection {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if isMergeKey(k) {
			for _, source := range mergeSources(v) {
				if !mappingCarries(source, e.field, depth+1) {
					continue
				}
				// The list of sources is itself a node an alias can
				// name. Whatever happens to this source — edited where
				// it stands, replaced by a copy, or dropped — happens
				// to that list.
				if v.Kind == yaml.SequenceNode && v.Anchor != "" {
					e.edited[v] = true
				}
				if source.Kind != yaml.MappingNode {
					continue
				}
				if source.Anchor != "" {
					e.edited[source] = true
				}
				e.plan(source, depth+1)
			}
			continue
		}
		if k.Value == e.field {
			e.markRemoved(k)
			e.markRemoved(v)
		}
	}
}

func (e *fieldRemoval) markRemoved(n *yaml.Node) {
	if n == nil || e.removed[n] {
		return
	}
	e.removed[n] = true
	for _, child := range n.Content {
		e.markRemoved(child)
	}
}

// detachAliases gives every alias that survives the removal its own
// copy of a node the removal is about to take away or change.
func (e *fieldRemoval) detachAliases(n *yaml.Node) {
	if n == nil || e.removed[n] {
		return
	}
	if n.Kind == yaml.AliasNode {
		if e.removed[n.Alias] || e.edited[n.Alias] {
			// Onto the alias node itself, so whatever holds it now
			// holds the copy.
			*n = *e.detachedCopy(n.Alias, 0)
		}
		return
	}
	for _, child := range n.Content {
		e.detachAliases(child)
	}
}

// detachedCopy copies a subtree so that it stands on its own: no anchor
// is copied, since the original keeps its name or is going away, and an
// alias to a node that is going away is replaced by a copy of that
// node. An alias to anything that stays is kept as an alias.
//
// hops counts the aliases followed to get here, not how deep the tree
// is: only following an alias can lead back to where the copy started.
func (e *fieldRemoval) detachedCopy(n *yaml.Node, hops int) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil && e.removed[n.Alias] && hops < maxYAMLIndirection {
		return e.detachedCopy(n.Alias, hops+1)
	}
	c := *n
	c.Anchor = ""
	c.Content = nil
	for _, child := range n.Content {
		c.Content = append(c.Content, e.detachedCopy(child, hops))
	}
	return &c
}

// strip removes the field from m and from every mapping m merges in.
func (e *fieldRemoval) strip(m *yaml.Node, depth int) {
	if depth > maxYAMLIndirection {
		return
	}
	kept := make([]*yaml.Node, 0, len(m.Content))
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		switch {
		case isMergeKey(k) && v.Kind == yaml.SequenceNode:
			sources := make([]*yaml.Node, 0, len(v.Content))
			for _, source := range v.Content {
				if source = e.stripSource(source, depth); source != nil {
					sources = append(sources, source)
				}
			}
			v.Content = sources
			// A merge of nothing is noise the author did not write.
			if len(sources) == 0 && v.Anchor == "" {
				continue
			}
		case isMergeKey(k):
			if v = e.stripSource(v, depth); v == nil {
				continue
			}
		case k.Value == e.field:
			continue
		}
		kept = append(kept, k, v)
	}
	m.Content = kept
}

// stripSource returns one merge source with the field taken out, or nil
// when nothing is left of it to merge.
func (e *fieldRemoval) stripSource(source *yaml.Node, depth int) *yaml.Node {
	if !mappingCarries(source, e.field, depth+1) {
		return source
	}
	if source.Kind == yaml.AliasNode {
		// The anchored mapping belongs to wherever it was defined, and
		// may be merged or aliased there for reasons of its own, so
		// the root merges a copy without the field instead.
		target := aliasTarget(source)
		if target == nil {
			return source
		}
		source = e.detachedCopy(target, 0)
	}
	if source.Kind != yaml.MappingNode {
		return source
	}
	e.strip(source, depth+1)
	if len(source.Content) == 0 && source.Anchor == "" {
		return nil
	}
	return source
}

// ImageEntry is one name in a directory of a kit's filesystem. Link is
// the target of a symbolic link exactly as stored, and empty for
// anything else.
type ImageEntry struct {
	Name string
	Dir  bool
	Link string
}

// ImageFiles reads one platform's filesystem. It is the whole of what
// [ProvidesFromPaths] needs from a build, so a frontend adapts whatever
// holds its result — a BuildKit reference, a directory — without this
// package learning what that is.
//
// present is false, with a nil error, for a path that does not exist.
// ReadDir lists a directory's own entries without descending and
// without following a link in the last component of an entry, so a
// link is reported as a link.
type ImageFiles interface {
	ReadFile(ctx context.Context, name string) (body []byte, present bool, err error)
	ReadDir(ctx context.Context, name string) (entries []ImageEntry, present bool, err error)
}

// PlatformFiles is one platform's filesystem beside the name
// diagnostics call it by.
type PlatformFiles struct {
	Platform string
	Files    ImageFiles
}

// ProvidesFromPaths resolves a providesFrom declaration against every
// platform a kit publishes and renders the provides entries it states,
// sorted (§9.7).
//
// Each path has to resolve on every platform, to one version. Derived
// entries are dropped where platforms disagree, because nobody asked
// for them one by one; these were asked for by name, so an entry that
// cannot be stated is a build failure the author can see rather than a
// capability a consumer discovers missing.
func ProvidesFromPaths(ctx context.Context, from map[string]string, platforms []PlatformFiles) ([]string, error) {
	if len(from) == 0 {
		return nil, nil
	}
	if len(platforms) == 0 {
		return nil, fmt.Errorf("providesFrom: no filesystem to read; the kit built no content")
	}
	names := slices.Sorted(maps.Keys(from))
	versions := map[string]string{}
	for i, p := range platforms {
		if p.Files == nil {
			return nil, fmt.Errorf("providesFrom: no filesystem to read%s; the kit built no content", onPlatform(p.Platform))
		}
		owners, err := readFileOwners(ctx, p.Files)
		if err != nil {
			return nil, fmt.Errorf("providesFrom%s: %w", onPlatform(p.Platform), err)
		}
		for _, name := range names {
			version, err := owners.version(ctx, from[name])
			if err != nil {
				return nil, fmt.Errorf("providesFrom.%s%s: %w", name, onPlatform(p.Platform), err)
			}
			if i > 0 && versions[name] != version {
				return nil, fmt.Errorf("providesFrom.%s: %s is at %s%s and %s%s; one descriptor serves every platform, so it can state only a version they share",
					name, from[name], versions[name], onPlatform(platforms[0].Platform), version, onPlatform(p.Platform))
			}
			versions[name] = version
		}
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"@"+versions[name])
	}
	sort.Strings(out)
	return out, nil
}

func onPlatform(platform string) string {
	if platform == "" {
		return ""
	}
	return " on " + platform
}

// fileOwners is one filesystem's record of which installed package put
// each path on disk.
type fileOwners struct {
	files ImageFiles
	// owners maps a path to the packages that list it, each named
	// <namespace>/<package>. More than one is ordinary for a directory
	// and a fact worth refusing for a file.
	owners map[string]map[string]bool
	// versions maps a package to the one version this model can name
	// for it, or "" when it can name none.
	versions map[string]string
	// aliases maps a top-level directory that is a symbolic link to
	// where it leads: /bin to /usr/bin on a merged-/usr system.
	aliases map[string]string
	dirs    map[string][]ImageEntry
}

// readFileOwners reads every package database the filesystem carries.
func readFileOwners(ctx context.Context, files ImageFiles) (*fileOwners, error) {
	o := &fileOwners{
		files:    files,
		owners:   map[string]map[string]bool{},
		versions: map[string]string{},
		aliases:  map[string]string{},
		dirs:     map[string][]ImageEntry{},
	}
	// The top-level links first, because every path read below is
	// compared through them. A merged-/usr system reaches one file by
	// two spellings — dpkg may record /bin/sh for what PATH finds at
	// /usr/bin/sh — and a lookup that knew only one of them would call
	// an installed command unowned.
	root, _, err := files.ReadDir(ctx, "/")
	if err != nil {
		return nil, fmt.Errorf("read /: %w", err)
	}
	o.dirs["/"] = root
	for _, e := range root {
		if e.Link == "" {
			continue
		}
		// To the directory the link really leads to, however many
		// links that takes, so one substitution is the whole answer. A
		// link that leads nowhere is no alias for anything.
		target, reached, err := o.realDirectory(ctx, []string{e.Name})
		if err != nil {
			return nil, err
		}
		if reached {
			o.aliases["/"+e.Name] = target
		}
	}

	found := false
	for _, db := range PackageDatabases() {
		body, present, err := files.ReadFile(ctx, db.Path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", db.Path, err)
		}
		if !present {
			continue
		}
		found = true
		packages, err := db.Read(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", db.Path, err)
		}
		for _, p := range packages {
			key := db.Namespace + "/" + p.Name
			version := PackageVersion(p.Version)
			// A multiarch database names one package once per
			// architecture. Two records at two versions are a package
			// this model cannot put one version on.
			if known, ok := o.versions[key]; ok && known != version {
				version = ""
			}
			o.versions[key] = version
		}
		switch db.Namespace {
		case DebNamespace:
			err = o.readDpkgLists(ctx, body)
		case ApkNamespace:
			err = o.readApkFiles(body)
		}
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, fmt.Errorf("the image carries neither %s nor %s, so nothing records which package a file belongs to", DpkgStatusPath, ApkInstalledPath)
	}
	return o, nil
}

// readDpkgLists reads dpkg's per-package file lists.
//
// Files rather than `dpkg -S`, because an image need not carry dpkg to
// carry its database: one assembled from packages keeps the lists
// without the tool that wrote them.
func (o *fileOwners) readDpkgLists(ctx context.Context, status []byte) error {
	installed, err := dpkgInstalledArchitectures(status)
	if err != nil {
		return fmt.Errorf("%s: %w", DpkgStatusPath, err)
	}
	entries, present, err := o.files.ReadDir(ctx, DpkgInfoDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", DpkgInfoDir, err)
	}
	if !present {
		return fmt.Errorf("the image has %s but no %s, so nothing records which package a file belongs to", DpkgStatusPath, DpkgInfoDir)
	}
	for _, e := range entries {
		stem, isList := strings.CutSuffix(e.Name, ".list")
		if !isList || e.Dir {
			continue
		}
		// A list outlives its package: a removed one keeps the
		// conffiles it left behind. Only what the status file calls
		// installed owns anything, and that is judged for the package
		// the list is actually named for. A co-installable package has
		// one list per architecture, <package>:<arch>.list, and one of
		// them can be removed while another stays: the list of the
		// removed one must not pass for the installed one's because
		// they share a name.
		name, arch, qualified := strings.Cut(stem, ":")
		architectures, known := installed[name]
		if !known || (qualified && !architectures[arch]) {
			continue
		}
		// The architecture is part of which package this is, not of
		// the name a capability is stated under.
		key := DebNamespace + "/" + name
		list := path.Join(DpkgInfoDir, e.Name)
		body, present, err := o.files.ReadFile(ctx, list)
		if err != nil {
			return fmt.Errorf("read %s: %w", list, err)
		}
		if !present {
			continue
		}
		if err := eachLine(bytes.NewReader(body), func(line string) {
			o.own(line, key)
		}); err != nil {
			return fmt.Errorf("%s: %w", list, err)
		}
	}
	return nil
}

// dpkgInstalledArchitectures maps each installed package name to the
// architectures it is installed for. [ReadDpkgStatus] folds those into
// one name, which is all a derived entry needs; choosing a file list
// needs to know which of them a list belongs to.
func dpkgInstalledArchitectures(status []byte) (map[string]map[string]bool, error) {
	installed := map[string]map[string]bool{}
	err := eachStanza(bytes.NewReader(status), func(fields map[string]string) {
		name := fields["package"]
		if name == "" || !dpkgInstalled(fields) {
			return
		}
		if installed[name] == nil {
			installed[name] = map[string]bool{}
		}
		installed[name][fields["architecture"]] = true
	}, dpkgField)
	return installed, err
}

// readApkFiles reads the file lists apk keeps inside its installed
// database: within a package's stanza, an F: line names a directory and
// each R: line after it a file in that directory.
func (o *fileOwners) readApkFiles(body []byte) error {
	key, dir := "", ""
	return eachLine(bytes.NewReader(body), func(line string) {
		field, value, found := strings.Cut(line, ":")
		if !found || len(field) != 1 {
			return
		}
		switch field {
		case "P":
			key, dir = ApkNamespace+"/"+value, ""
		case "F":
			dir = value
		case "R":
			if key != "" {
				o.own(path.Join("/", dir, value), key)
			}
		}
	}, func() { key, dir = "", "" })
}

// eachLine calls line for every non-blank line and blank, when given,
// for every blank one — the stanza boundary of both databases.
func eachLine(r io.Reader, line func(string), blank ...func()) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxPackageDatabaseLine)
	for s.Scan() {
		text := strings.TrimSuffix(s.Text(), "\r")
		if strings.TrimSpace(text) == "" {
			for _, f := range blank {
				f()
			}
			continue
		}
		line(text)
	}
	if err := s.Err(); err != nil {
		return fmt.Errorf("read package database: %w", err)
	}
	return nil
}

func (o *fileOwners) own(file, key string) {
	if !strings.HasPrefix(file, "/") {
		return
	}
	file = o.canonical(file)
	if o.owners[file] == nil {
		o.owners[file] = map[string]bool{}
	}
	o.owners[file][key] = true
}

// canonical rewrites a recorded path through a top-level symbolic link,
// so the spellings a merged-/usr system gives one file compare equal.
//
// One substitution is enough, because an alias already names the
// directory its link finally reaches: with /bin pointing at compat/bin
// and /compat at usr, the alias for /bin is /usr/bin.
func (o *fileOwners) canonical(file string) string {
	file = path.Clean(file)
	rest := strings.TrimPrefix(file, "/")
	top, tail, nested := strings.Cut(rest, "/")
	if target, aliased := o.aliases["/"+top]; aliased && nested {
		return path.Join(target, tail)
	}
	return file
}

// version names the version of the package that owns file, following
// the file through symbolic links until something owns it.
//
// The link is followed only when the path itself is unowned. A package
// that ships a link — python3 pointing at python3.14 — is the answer
// for that link; a link nothing owns was made by a recipe or by
// update-alternatives, and what it leads to is the thing installed.
//
// The owner is looked up where the file really is: with every directory
// on the way resolved, /usr/lib/jvm/default/bin/java is the file a
// package installed as /usr/lib/jvm/java-21/bin/java. That spelling
// comes first because it names the file that is actually there. A list
// can still hold the path as it was written — a record from before the
// directory became a link — and taking that one would state the version
// of a package whose file this no longer is. The written spelling is
// the fallback, for a package that ships into a linked directory and
// records the path that way.
func (o *fileOwners) version(ctx context.Context, file string) (string, error) {
	current := file
	for range maxProvidesFromLinks {
		resolved, reached, err := o.resolveDirectories(ctx, current)
		if err != nil {
			return "", err
		}
		// A walk that did not get there names no file, and nothing is
		// looked up for it. What is left of the path is not a path: a
		// ".." after a component that is missing would otherwise step
		// back out of the hole and land on a file that is there.
		var entry ImageEntry
		present := false
		if reached {
			if entry, present, err = o.entry(ctx, resolved); err != nil {
				return "", err
			}
		}
		if !present {
			if current != file {
				return "", fmt.Errorf("%s leads to %s, which does not exist in the image", file, current)
			}
			return "", fmt.Errorf("%s does not exist in the image", file)
		}
		if entry.Dir {
			return "", fmt.Errorf("%s is a directory; name a file a package installs, such as the command itself", current)
		}
		owners := o.owners[resolved]
		// Only a spelling with nothing to collapse can be compared as
		// text: a link target may climb with "..", and what that means
		// depends on the directories before it.
		if len(owners) == 0 && current == path.Clean(current) {
			owners = o.owners[o.canonical(current)]
		}
		if len(owners) > 0 {
			return o.ownerVersion(file, owners)
		}
		if entry.Link == "" {
			return "", fmt.Errorf("%s belongs to no installed package; a file a recipe placed has no package to take a version from, so state its version in provides", current)
		}
		// Relative to where the link really is, and left as written:
		// resolveDirectories walks it a component at a time, which is
		// the only reading under which sub/../tool means the parent of
		// wherever sub leads rather than the directory sub is in.
		current = entry.Link
		if !path.IsAbs(current) {
			current = path.Dir(resolved) + "/" + current
		}
	}
	return "", fmt.Errorf("%s leads through more than %d symbolic links", file, maxProvidesFromLinks)
}

// resolveDirectories returns file with every directory on the way to it
// that is a symbolic link replaced by where the link leads. The last
// component is left alone: whether the file itself is a link is the
// caller's question.
//
// file is read a component at a time and never cleaned as text, so a
// ".." is the parent of the directory actually reached.
//
// reached is false when the directories do not lead anywhere, and the
// path returned then means nothing.
//
// Only requested paths come through here. Doing the same for every path
// a package recorded would mean a directory read per directory in the
// image, so recorded paths are compared as recorded, through top-level
// links only.
func (o *fileOwners) resolveDirectories(ctx context.Context, file string) (resolved string, reached bool, err error) {
	parts := strings.Split(file, "/")
	// A path that ends in a slash, a "." or a ".." can only name a
	// directory, so all of it is walked as one: a file with a slash
	// after it is refused, as the system refuses it, and a directory
	// is handed back for the caller to say that it is one.
	if last := parts[len(parts)-1]; last == "" || last == "." || last == ".." {
		return o.realDirectory(ctx, parts)
	}
	dir, reached, err := o.realDirectory(ctx, parts[:len(parts)-1])
	if err != nil || !reached {
		return "", false, err
	}
	return path.Join(dir, parts[len(parts)-1]), true, nil
}

// realDirectory walks components from the root and returns the
// directory they name with every symbolic link on the way followed,
// the last one included.
//
// reached is false when the walk cannot be completed the way the
// system would complete it: a component does not exist, a component is
// a file where a directory has to be, or the links lead in a circle.
// Nothing is returned for the path then. Stopping is the point — the
// components still pending are not a remainder to append, since a ".."
// among them would be taken from somewhere the walk never got to.
func (o *fileOwners) realDirectory(ctx context.Context, parts []string) (dir string, reached bool, err error) {
	pending := append([]string(nil), parts...)
	resolved := "/"
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = path.Dir(resolved)
			continue
		}
		candidate := path.Join(resolved, part)
		entry, present, err := o.entry(ctx, candidate)
		if err != nil {
			return "", false, err
		}
		switch {
		case !present:
			return "", false, nil
		case entry.Link != "":
			if links++; links > maxProvidesFromLinks {
				return "", false, nil
			}
			if path.IsAbs(entry.Link) {
				resolved = "/"
			}
			pending = append(strings.Split(entry.Link, "/"), pending...)
		case !entry.Dir:
			// A file cannot be walked through, and the system refuses
			// the path whatever comes after it.
			return "", false, nil
		default:
			resolved = candidate
		}
	}
	return resolved, true, nil
}

func (o *fileOwners) ownerVersion(file string, owners map[string]bool) (string, error) {
	keys := slices.Sorted(maps.Keys(owners))
	if len(keys) > 1 {
		return "", fmt.Errorf("%s is listed by %s; a version comes from one package, so name a file only one of them installs", file, strings.Join(keys, " and "))
	}
	version := o.versions[keys[0]]
	if version == "" {
		return "", fmt.Errorf("%s belongs to %s, whose recorded version this model cannot name (§9.6); state the capability's version in provides", file, keys[0])
	}
	return version, nil
}

// entry reports what stands at file, read out of its directory's
// listing so that a symbolic link is seen as one.
func (o *fileOwners) entry(ctx context.Context, file string) (ImageEntry, bool, error) {
	dir, base := path.Dir(file), path.Base(file)
	entries, cached := o.dirs[dir]
	if !cached {
		var err error
		entries, _, err = o.files.ReadDir(ctx, dir)
		if err != nil {
			return ImageEntry{}, false, fmt.Errorf("read %s: %w", dir, err)
		}
		o.dirs[dir] = entries
	}
	for _, e := range entries {
		if e.Name == base {
			return e, true, nil
		}
	}
	return ImageEntry{}, false, nil
}
