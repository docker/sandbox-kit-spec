package main

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/containerd/platforms"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// derivedProvides reads what every platform's filesystem says it carries
// and renders the provides entries they agree on (§9.6).
//
// Read from the built filesystems rather than declared, because a base
// image's contents are not something an author knows: a kit that names
// `bash` in provides is stating a fact about content it inherited, and
// the only honest version for it is the one dpkg recorded. Nothing is
// derived for a mixin — its delta is not a root filesystem, and §5.3
// admits one workload per composition, so a workload-only rule gives
// every derived name exactly one owner.
func derivedProvides(ctx context.Context, refs []gwclient.Reference) ([]string, error) {
	var out []string
	for _, db := range spec.PackageDatabases() {
		listings := make([][]spec.Package, 0, len(refs))
		for _, ref := range refs {
			// A declaration-only kit has no filesystem to read, which is
			// not the same as one whose filesystem holds no packages: the
			// first cannot be judged, and treating it as an empty listing
			// would make every platform disagree with it.
			if ref == nil {
				return nil, nil
			}
			body, present, err := readImageFile(ctx, ref, db.Path)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", db.Path, err)
			}
			if !present {
				// An image with no such database installs through
				// something else — a DHI rootfs assembled from debs
				// carries no dpkg at all — and has nothing to say here.
				listings = nil
				break
			}
			packages, err := db.Read(bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", db.Path, err)
			}
			listings = append(listings, packages)
		}
		out = append(out, spec.DerivedProvides(db.Namespace, listings)...)
	}
	return out, nil
}

// statedProvides resolves the kit's providesFrom declaration against
// every platform's filesystem (§9.7): each path to the installed package
// that owns it, and each name to that package's version.
func statedProvides(ctx context.Context, from map[string]string, builds []platformBuild) ([]string, error) {
	if len(from) == 0 {
		return nil, nil
	}
	files := make([]spec.PlatformFiles, 0, len(builds))
	for _, b := range builds {
		p := spec.PlatformFiles{Platform: platforms.FormatAll(platformOrDefault(b.platform))}
		if b.ref != nil {
			p.Files = imageFiles{ref: b.ref}
		}
		files = append(files, p)
	}
	return spec.ProvidesFromPaths(ctx, from, files)
}

// imageFiles reads a solved filesystem for the spec package, which
// knows what to look for and not what a BuildKit reference is.
type imageFiles struct{ ref gwclient.Reference }

func (f imageFiles) ReadFile(ctx context.Context, name string) ([]byte, bool, error) {
	return readImageFile(ctx, f.ref, name)
}

// ReadDir lists a directory through the gateway, which walks it without
// following its entries: a symbolic link arrives with its target, which
// is the one thing StatFile cannot say — it resolves the whole path and
// answers for wherever that ends.
func (f imageFiles) ReadDir(ctx context.Context, name string) ([]spec.ImageEntry, bool, error) {
	stats, err := f.ref.ReadDir(ctx, gwclient.ReadDirRequest{Path: name})
	if err != nil {
		if isNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	entries := make([]spec.ImageEntry, 0, len(stats))
	for _, st := range stats {
		entries = append(entries, spec.ImageEntry{
			Name: st.Path,
			Dir:  os.FileMode(st.Mode).IsDir(),
			Link: st.Linkname,
		})
	}
	return entries, true, nil
}
