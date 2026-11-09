package spec

import (
	"net/url"
	"strings"
)

// EffectiveProvideVersion is the fallback version for a Kit's unversioned
// provides. A version carried by its consumption reference takes precedence
// over the descriptor's version. Explicitly versioned provides are unchanged.
func EffectiveProvideVersion(reference string, d *Descriptor) string {
	if version := VersionFromReference(reference); version != "" {
		return version
	}
	if d == nil {
		return ""
	}
	return d.Version
}

// VersionFromReference extracts a normalized version from an OCI tag or a
// Git reference's ref parameter. Digest-only references, non-version tags,
// Git branches or commits, and local paths yield an empty string.
func VersionFromReference(reference string) string {
	if strings.HasPrefix(reference, "git+") {
		_, fragment, found := strings.Cut(reference, "#")
		if !found {
			return ""
		}
		values, err := url.ParseQuery(fragment)
		if err != nil {
			return ""
		}
		return TagVersion(values.Get("ref"))
	}
	if i := strings.Index(reference, "@"); i >= 0 {
		reference = reference[:i]
	}
	slash := strings.LastIndex(reference, "/")
	colon := strings.LastIndex(reference, ":")
	if colon <= slash {
		return ""
	}
	return TagVersion(reference[colon+1:])
}
