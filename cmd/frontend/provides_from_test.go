package main

import (
	"context"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"

	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	fstypes "github.com/tonistiigi/fsutil/types"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// packagedReference is a solved filesystem as the gateway presents one:
// files read by name, and a directory listed with each entry's own
// stat, so a symbolic link arrives as a link with its target.
type packagedReference struct {
	fakeReference
	files map[string]string
	links map[string]string
}

func (r packagedReference) ReadFile(_ context.Context, req gwclient.ReadRequest) ([]byte, error) {
	body, ok := r.files[req.Filename]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(body), nil
}

func (r packagedReference) ReadDir(_ context.Context, req gwclient.ReadDirRequest) ([]*fstypes.Stat, error) {
	seen := map[string]*fstypes.Stat{}
	for name := range r.files {
		if path.Dir(name) == req.Path {
			seen[path.Base(name)] = &fstypes.Stat{Path: path.Base(name), Mode: 0o755}
		} else if rest, under := strings.CutPrefix(name, strings.TrimSuffix(req.Path, "/")+"/"); under {
			top, _, _ := strings.Cut(rest, "/")
			seen[top] = &fstypes.Stat{Path: top, Mode: uint32(os.ModeDir | 0o755)}
		}
	}
	for name, target := range r.links {
		if path.Dir(name) == req.Path {
			seen[path.Base(name)] = &fstypes.Stat{Path: path.Base(name), Mode: uint32(os.ModeSymlink | 0o777), Linkname: target}
		}
	}
	if len(seen) == 0 {
		return nil, fs.ErrNotExist
	}
	out := make([]*fstypes.Stat, 0, len(seen))
	for _, st := range seen {
		out = append(out, st)
	}
	return out, nil
}

func debianReference(sshVersion string) packagedReference {
	return packagedReference{
		files: map[string]string{
			spec.DpkgStatusPath: "Package: openssh-client\nStatus: install ok installed\nVersion: " + sshVersion + "\n\n" +
				"Package: python-3.14-minimal\nStatus: install ok installed\nVersion: 3.14.10-6\n",
			spec.DpkgInfoDir + "/openssh-client.list":      "/usr/bin/ssh\n",
			spec.DpkgInfoDir + "/python-3.14-minimal.list": "/usr/bin/python3.14\n/usr/bin/python3\n",
			"/usr/bin/ssh":        "elf",
			"/usr/bin/python3.14": "elf",
		},
		links: map[string]string{
			"/bin":             "usr/bin",
			"/usr/bin/python3": "python3.14",
		},
	}
}

// The spec package decides what a path resolves to; this is the half
// that only the frontend can get wrong — reading a directory through the
// gateway so that a link is seen as one, and naming each platform.
func TestStatedProvidesReadEveryPlatformThroughTheGateway(t *testing.T) {
	from := map[string]string{"python": "/usr/bin/python3", "ssh": "/bin/ssh"}
	arm := &ocispecs.Platform{OS: "linux", Architecture: "arm64"}
	amd := &ocispecs.Platform{OS: "linux", Architecture: "amd64"}

	got, err := statedProvides(t.Context(), from, []platformBuild{
		{platform: arm, ref: debianReference("1:10.0p1-7+deb13u4")},
		{platform: amd, ref: debianReference("1:10.0p1-7+deb13u4")},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"python@3.14.10", "ssh@10.0"}, got)

	_, err = statedProvides(t.Context(), from, []platformBuild{
		{platform: arm, ref: debianReference("1:10.0p1-7+deb13u4")},
		{platform: amd, ref: debianReference("1:10.1p1-1")},
	})
	require.ErrorContains(t, err, "providesFrom.ssh: /bin/ssh is at 10.0 on linux/arm64 and 10.1 on linux/amd64")

	// Nothing asked for, nothing read: a kit without the field never
	// pays for the file lists.
	got, err = statedProvides(t.Context(), nil, []platformBuild{{platform: arm}})
	require.NoError(t, err)
	require.Empty(t, got)
}

// A directory that is not there is an absence, not a failure: the spec
// package turns it into the diagnostic that names the missing database.
func TestImageFilesReportAMissingDirectoryAsAbsent(t *testing.T) {
	files := imageFiles{ref: packagedReference{}}
	entries, present, err := files.ReadDir(context.Background(), spec.DpkgInfoDir)
	require.NoError(t, err)
	require.False(t, present)
	require.Empty(t, entries)

	_, present, err = files.ReadFile(context.Background(), spec.DpkgStatusPath)
	require.NoError(t, err)
	require.False(t, present)
}
