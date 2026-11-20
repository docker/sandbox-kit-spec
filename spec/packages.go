package spec

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
)

// DpkgStatusPath and ApkInstalledPath are where the two package managers
// record what is installed. A kit's own filesystem is the only honest
// source for this: a descriptor states what its author knew, and what a
// base image actually shipped is knowable only by reading it.
const (
	DpkgStatusPath   = "/var/lib/dpkg/status"
	ApkInstalledPath = "/lib/apk/db/installed"
)

// maxPackageDatabaseLine bounds one record line. A dpkg status file
// carries long Description and Conffiles bodies, well past bufio's
// default, and a truncated scan would silently report fewer packages
// rather than failing.
const maxPackageDatabaseLine = 1 << 20

// Package is one installed package as a database records it. Version is
// raw, exactly as written; [PackageVersion] is what turns it into
// something this model can name.
type Package struct {
	Name    string
	Version string
}

// PackageDatabase is one package manager's record of what a filesystem
// carries, and the namespace its records are published under.
type PackageDatabase struct {
	Namespace string
	Path      string
	Read      func(io.Reader) ([]Package, error)
}

// PackageDatabases are the databases §9.6 reads, in the order their
// entries are emitted.
//
// One table, because two sides of publication consult it and a
// disagreement between them would be a conformance failure invented by
// the checker: the frontend derives entries from these paths, and the
// kit suite holds a published artifact's entries back to them.
func PackageDatabases() []PackageDatabase {
	return []PackageDatabase{
		{Namespace: DebNamespace, Path: DpkgStatusPath, Read: ReadDpkgStatus},
		{Namespace: ApkNamespace, Path: ApkInstalledPath, Read: ReadApkInstalled},
	}
}

// ReadDpkgStatus lists the packages a dpkg status file records as
// installed.
//
// Only installed ones. A removed package keeps its stanza in that file
// with a config-files status, so taking every stanza would have a kit
// provide software it no longer carries — which is the opposite of what
// reading the filesystem was for.
func ReadDpkgStatus(r io.Reader) ([]Package, error) {
	var out []Package
	err := eachStanza(r, func(fields map[string]string) {
		if !dpkgInstalled(fields) {
			return
		}
		if name := fields["package"]; name != "" {
			out = append(out, Package{Name: name, Version: fields["version"]})
		}
	}, dpkgField)
	return out, err
}

// dpkgInstalled reports whether a status stanza records a package whose
// files are on disk. The third word of dpkg's want/flag/status triple is
// the one that says so.
func dpkgInstalled(fields map[string]string) bool {
	parts := strings.Fields(fields["status"])
	return len(parts) > 0 && parts[len(parts)-1] == "installed"
}

// dpkgField splits one line of a dpkg status stanza into a lowercased
// field name and its value.
func dpkgField(line string) (string, string, bool) {
	// A continuation line belongs to the field above it, and none of
	// the fields read here spans lines, so it is skipped.
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", "", false
	}
	key, value, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value), true
}

// ReadApkInstalled lists the packages an apk installed database records.
//
// apk keeps no removed-package records, so every stanza counts; the
// stanza shape is the difference, one letter per field.
func ReadApkInstalled(r io.Reader) ([]Package, error) {
	var out []Package
	err := eachStanza(r, func(fields map[string]string) {
		if name := fields["P"]; name != "" {
			out = append(out, Package{Name: name, Version: fields["V"]})
		}
	}, func(line string) (string, string, bool) {
		key, value, found := strings.Cut(line, ":")
		if !found || len(key) != 1 {
			return "", "", false
		}
		return key, value, true
	})
	return out, err
}

// eachStanza walks blank-line-separated records, calling emit once per
// record with its fields as split names them. Both databases are that
// shape and differ only in how a line becomes a field.
func eachStanza(r io.Reader, emit func(map[string]string), split func(string) (string, string, bool)) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxPackageDatabaseLine)
	fields := map[string]string{}
	flush := func() {
		if len(fields) > 0 {
			emit(fields)
			fields = map[string]string{}
		}
	}
	for s.Scan() {
		line := strings.TrimSuffix(s.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if key, value, ok := split(line); ok {
			fields[key] = value
		}
	}
	if err := s.Err(); err != nil {
		return fmt.Errorf("read package database: %w", err)
	}
	// A database need not end on a blank line, and dropping the last
	// record would quietly lose one package.
	flush()
	return nil
}

// DerivedProvides renders the provides entries a namespace's package
// listings agree on, one per package, sorted.
//
// Every listing has to hold the package at one version. A kit publishes
// one descriptor for every platform it builds, so it may only state what
// is true of all of them: a package only some platforms carry, or one
// whose version moves between them, is something a single entry cannot
// say without lying about one of them. Those are dropped rather than
// refused, because an architecture-specific package is ordinary.
//
// The same rule folds a multiarch database, where one name appears once
// per architecture within a single listing.
func DerivedProvides(namespace string, listings [][]Package) []string {
	if len(listings) == 0 {
		return nil
	}
	// "" marks a name already known to disagree with itself, which no
	// later listing can repair.
	const conflicted = ""
	versions := map[string]string{}
	counts := map[string]int{}

	for i, listing := range listings {
		seen := map[string]bool{}
		for _, p := range listing {
			name := namespace + "/" + p.Name
			if !validCapabilityName(name) {
				continue
			}
			version := PackageVersion(p.Version)
			known, ok := versions[name]
			switch {
			case version == "":
				// A record this model can name no version for
				// disagrees with every record that it can, another
				// architecture's copy of the same package included:
				// publishing that one would state a version the
				// content does not carry throughout. Conflicting the
				// name rather than passing over the record, or the
				// derivation would emit an entry the artifact's own
				// check refuses — the check holds an entry to every
				// record for its name — and fail the build that made
				// it.
				versions[name] = conflicted
			case !ok && i == 0:
				versions[name] = version
			case !ok:
				// Absent from an earlier listing, so it is already
				// disqualified; recording it now would let the count
				// below mistake it for unanimous.
				versions[name] = conflicted
			case known != version:
				versions[name] = conflicted
			}
			// Counted once per listing: a name repeated within one
			// listing at one version is one architecture's database
			// mentioning it twice, not agreement across platforms.
			if !seen[name] {
				seen[name] = true
				counts[name]++
			}
		}
	}

	out := make([]string, 0, len(versions))
	for name, version := range versions {
		if version == conflicted || counts[name] != len(listings) {
			continue
		}
		out = append(out, name+"@"+version)
	}
	sort.Strings(out)
	return out
}
