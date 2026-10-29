package spec

import (
	"regexp"
	"slices"
	"strings"
)

var (
	// gitPushRemote is host/owner/repo with at most one "*": lowercase
	// DNS host, then path components of the characters Git hosting
	// services accept in owner and repository names. A ".git" suffix is
	// not part of the name; the runtime compares without it.
	gitPushRemote = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(/[A-Za-z0-9._*-]+)+$`)

	// gitPushRef is a fully qualified ref pattern: "refs/" then
	// slash-separated components without the characters Git refuses in
	// ref names, with at most one "*". Qualification is required so a
	// pattern cannot be read as a branch by one runtime and a tag by
	// another.
	gitPushRef = regexp.MustCompile(`^refs/[A-Za-z0-9._*-]+(/[A-Za-z0-9._*-]+)*$`)
)

// GitPushPatternMatches reports whether a remote or ref matches one
// pattern. A pattern is a literal except for at most one "*", which
// matches any sequence of characters including "/", as a Git refspec
// glob does; "refs/heads/*" covers "refs/heads/feature/x".
func GitPushPatternMatches(pattern, value string) bool {
	before, after, wildcard := strings.Cut(pattern, "*")
	if !wildcard {
		return pattern == value
	}
	return len(value) >= len(before)+len(after) &&
		strings.HasPrefix(value, before) && strings.HasSuffix(value, after)
}

// GitPushRefAllowed applies a refs rule: an excluded ref is refused, an
// absent include admits the rest, a stated include admits only matches.
func GitPushRefAllowed(refs *GitPushRefs, ref string) bool {
	if refs == nil {
		return true
	}
	for _, p := range refs.Exclude {
		if GitPushPatternMatches(p, ref) {
			return false
		}
	}
	if len(refs.Include) == 0 {
		return true
	}
	return slices.ContainsFunc(refs.Include, func(p string) bool { return GitPushPatternMatches(p, ref) })
}

// GitPushRemoteAllowed reports whether a host/owner/repo remote is within
// the remotes list; an absent list admits every remote.
func GitPushRemoteAllowed(remotes []string, remote string) bool {
	if len(remotes) == 0 {
		return true
	}
	remote = strings.TrimSuffix(remote, ".git")
	return slices.ContainsFunc(remotes, func(p string) bool { return GitPushPatternMatches(p, remote) })
}

// validateGitPushNeed decodes and checks one git-push entry.
func validateGitPushNeed(path string, i int, n Capability) (*GitPush, error) {
	if err := validateGitPushPresence(path, i, n); err != nil {
		return nil, err
	}
	var p GitPush
	if err := DecodeCapabilityConfig(n, &p); err != nil {
		return nil, configErrorf(path, i, err)
	}
	if err := validateGitPushList(path+".config.remotes", i, "remotes", p.Remotes, gitPushRemote,
		"a remote is host/owner/repo with a lowercase DNS host and at most one *"); err != nil {
		return nil, err
	}
	if p.Refs != nil {
		for _, list := range []struct {
			field  string
			values []string
		}{{"include", p.Refs.Include}, {"exclude", p.Refs.Exclude}} {
			if err := validateGitPushList(path+".config.refs."+list.field, i, "refs."+list.field, list.values, gitPushRef,
				"a ref pattern starts with refs/ and has at most one *"); err != nil {
				return nil, err
			}
		}
	}
	if err := validateEnum(path+".config.delete", i, "git-push delete", p.Delete, GitPushAllow, GitPushDeny); err != nil {
		return nil, err
	}
	if p.Commits != nil {
		if err := validateEnum(path+".config.commits.signed", i, "git-push commits.signed", p.Commits.Signed, GitPushRequire, GitPushPrefer, GitPushIgnore); err != nil {
			return nil, err
		}
		if err := validateEnum(path+".config.commits.trailer", i, "git-push commits.trailer", p.Commits.Trailer, GitPushRequire, GitPushWarn); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// validateGitPushPresence judges what the entry states before values are
// known: a policy stating nothing bounds nothing, a null or empty list is
// not an absent one, and a stated object must carry a rule.
func validateGitPushPresence(path string, i int, n Capability) error {
	if len(n.Config) == 0 {
		return fieldErrorf(path+".config", "capabilities[%d]: git-push states no rule; bound remotes, refs, delete or commits, or drop the entry", i)
	}
	for _, field := range []string{"remotes", "refs", "delete", "commits"} {
		if v, present := n.Config[field]; present && v == nil {
			return fieldErrorf(path+".config."+field, "capabilities[%d]: git-push %s must not be null", i, field)
		}
	}
	if v, present := n.Config["remotes"]; present && emptyList(v) {
		return fieldErrorf(path+".config.remotes", "capabilities[%d]: git-push remotes is empty; omit it to allow every remote", i)
	}
	if refs, present := n.Config["refs"]; present {
		obj, ok := refs.(map[string]any)
		if !ok || len(obj) == 0 {
			return fieldErrorf(path+".config.refs", "capabilities[%d]: git-push refs must state include or exclude", i)
		}
		for _, field := range []string{"include", "exclude"} {
			if v, present := obj[field]; present && (v == nil || emptyList(v)) {
				return fieldErrorf(path+".config.refs."+field, "capabilities[%d]: git-push refs.%s is empty; omit it instead", i, field)
			}
		}
	}
	if commits, present := n.Config["commits"]; present {
		obj, ok := commits.(map[string]any)
		if !ok || len(obj) == 0 {
			return fieldErrorf(path+".config.commits", "capabilities[%d]: git-push commits must state signed or trailer", i)
		}
		for _, field := range []string{"signed", "trailer"} {
			if v, present := obj[field]; present && v == nil {
				return fieldErrorf(path+".config.commits."+field, "capabilities[%d]: git-push commits.%s must not be null", i, field)
			}
		}
	}
	return nil
}

func emptyList(v any) bool {
	switch list := v.(type) {
	case []any:
		return len(list) == 0
	case []string:
		return len(list) == 0
	}
	return false
}

func validateGitPushList(path string, i int, field string, values []string, pattern *regexp.Regexp, rule string) error {
	seen := map[string]bool{}
	for _, v := range values {
		if !ContainsArgRef(v) && !ContainsEnvRef(v) && (!pattern.MatchString(v) || strings.Count(v, "*") > 1) {
			return fieldErrorf(path, "capabilities[%d]: git-push %s value %q is invalid: %s", i, field, v, rule)
		}
		if seen[v] {
			return fieldErrorf(path, "capabilities[%d]: git-push %s lists %q twice", i, field, v)
		}
		seen[v] = true
	}
	return nil
}

// MergeGitPushes intersects two policies into the one the merged kit
// enforces: excludes union, deny beats allow, and the stricter commit rule
// wins. Two different include or remotes lists have no single-list
// intersection, so they are refused and the authors asked to agree on one.
func MergeGitPushes(a, b GitPush) (GitPush, error) {
	out := GitPush{
		Delete: stricter(a.Delete, b.Delete, GitPushDeny, GitPushAllow),
	}
	remotes, err := sameOrSingle("remotes", a.Remotes, b.Remotes)
	if err != nil {
		return GitPush{}, err
	}
	out.Remotes = remotes
	if a.Refs != nil || b.Refs != nil {
		var ra, rb GitPushRefs
		if a.Refs != nil {
			ra = *a.Refs
		}
		if b.Refs != nil {
			rb = *b.Refs
		}
		include, err := sameOrSingle("refs.include", ra.Include, rb.Include)
		if err != nil {
			return GitPush{}, err
		}
		out.Refs = &GitPushRefs{Include: include, Exclude: unionSorted(ra.Exclude, rb.Exclude)}
	}
	if a.Commits != nil || b.Commits != nil {
		var ca, cb GitPushCommits
		if a.Commits != nil {
			ca = *a.Commits
		}
		if b.Commits != nil {
			cb = *b.Commits
		}
		out.Commits = &GitPushCommits{
			Signed:  stricter(ca.Signed, cb.Signed, GitPushRequire, GitPushPrefer, GitPushIgnore),
			Trailer: stricter(ca.Trailer, cb.Trailer, GitPushRequire, GitPushWarn),
		}
	}
	return out, nil
}

type gitPushMergeError struct{ field string }

func (e gitPushMergeError) Error() string {
	return "both bound git-push " + e.field + " to different lists; the merged kit cannot push to the intersection of two patterns, so one kit has to adopt the other's list"
}

func sameOrSingle(field string, a, b []string) ([]string, error) {
	switch {
	case len(a) == 0:
		return slices.Clone(b), nil
	case len(b) == 0:
		return slices.Clone(a), nil
	}
	sa, sb := slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b))
	if slices.Equal(sa, sb) {
		return sa, nil
	}
	return nil, gitPushMergeError{field: field}
}
