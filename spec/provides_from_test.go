package spec

import (
	"context"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeImage is a filesystem held in a map: path to file body, with a
// symbolic link written as "-> target" and a directory implied by
// anything beneath it.
type fakeImage map[string]string

func (f fakeImage) ReadFile(_ context.Context, name string) ([]byte, bool, error) {
	body, ok := f[name]
	if !ok || strings.HasPrefix(body, "-> ") {
		return nil, false, nil
	}
	return []byte(body), true, nil
}

func (f fakeImage) ReadDir(_ context.Context, name string) ([]ImageEntry, bool, error) {
	seen := map[string]ImageEntry{}
	for file, body := range f {
		if path.Dir(file) == name {
			e := ImageEntry{Name: path.Base(file)}
			if target, link := strings.CutPrefix(body, "-> "); link {
				e.Link = target
			}
			seen[e.Name] = e
			continue
		}
		prefix := strings.TrimSuffix(name, "/") + "/"
		if rest, under := strings.CutPrefix(file, prefix); under {
			top, _, _ := strings.Cut(rest, "/")
			if _, known := seen[top]; !known {
				seen[top] = ImageEntry{Name: top, Dir: true}
			}
		}
	}
	out := make([]ImageEntry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, len(out) > 0, nil
}

// debianImage is the shape of a merged-/usr Debian root: /bin is a link,
// python3 is a link its package ships, awk is an alternatives link no
// package owns, and one removed package still has a list.
func debianImage() fakeImage {
	return fakeImage{
		"/bin": "-> usr/bin",
		DpkgStatusPath: strings.Join([]string{
			"Package: python-3.14-minimal\nStatus: install ok installed\nVersion: 3.14.10-6\n",
			"Package: openssh-client\nStatus: install ok installed\nVersion: 1:10.0p1-7+deb13u4+dhi3\n",
			"Package: mawk\nStatus: install ok installed\nVersion: 1.3.4.20250131-1+deb13u0\n",
			"Package: bash\nStatus: install ok installed\nVersion: 5.2.37-2+dhi1\n",
			"Package: gone\nStatus: deinstall ok config-files\nVersion: 1.0-1\n",
			"Package: pre\nStatus: install ok installed\nVersion: 2.0~rc1-1\n",
			"Package: libfoo\nStatus: install ok installed\nVersion: 1.2.3-1\n",
			"Package: libbar\nStatus: install ok installed\nVersion: 4.5.6-1\n",
		}, "\n"),
		DpkgInfoDir + "/python-3.14-minimal.list": "/.\n/usr\n/usr/bin\n/usr/bin/python3.14\n/usr/bin/python3\n",
		DpkgInfoDir + "/openssh-client.list":      "/.\n/usr\n/usr/bin\n/usr/bin/ssh\n",
		DpkgInfoDir + "/mawk.list":                "/.\n/usr\n/usr/bin\n/usr/bin/mawk\n",
		// Recorded under /bin, as packages built before the /usr merge do.
		DpkgInfoDir + "/bash.list":         "/.\n/bin\n/bin/bash\n",
		DpkgInfoDir + "/gone.list":         "/etc/gone.conf\n",
		DpkgInfoDir + "/pre.list":          "/usr/bin/pre\n",
		DpkgInfoDir + "/libfoo:arm64.list": "/usr/lib/shared.so\n",
		DpkgInfoDir + "/libbar.list":       "/usr/lib/shared.so\n",
		DpkgInfoDir + "/bash.md5sums":      "ignored\n",
		"/usr/bin/python3":                 "-> python3.14",
		"/usr/bin/python3.14":              "elf",
		"/usr/bin/ssh":                     "elf",
		"/usr/bin/mawk":                    "elf",
		"/usr/bin/awk":                     "-> /etc/alternatives/awk",
		"/etc/alternatives/awk":            "-> /usr/bin/mawk",
		"/usr/bin/bash":                    "elf",
		"/usr/bin/pre":                     "elf",
		"/usr/lib/shared.so":               "elf",
		"/usr/local/bin/claude":            "elf",
		"/usr/local/bin/dangling":          "-> /nowhere",
		"/usr/local/bin/loop":              "-> loop",
		"/etc/gone.conf":                   "conf",
	}
}

func one(f ImageFiles) []PlatformFiles {
	return []PlatformFiles{{Platform: "linux/arm64", Files: f}}
}

func TestProvidesFromPaths(t *testing.T) {
	got, err := ProvidesFromPaths(context.Background(), map[string]string{
		"python":            "/usr/bin/python3",
		"ssh":               "/usr/bin/ssh",
		"awk":               "/usr/bin/awk",
		"bash":              "/usr/bin/bash",
		"com.example/shell": "/bin/bash",
	}, one(debianImage()))
	require.NoError(t, err)
	require.Equal(t, []string{
		"awk@1.3.4",
		"bash@5.2.37",
		"com.example/shell@5.2.37",
		"python@3.14.10",
		"ssh@10.0",
	}, got)
}

func TestProvidesFromPathsRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		want string
	}{
		"absent":            {"/usr/bin/nope", "does not exist in the image"},
		"directory":         {"/usr/bin", "is a directory"},
		"unowned":           {"/usr/local/bin/claude", "belongs to no installed package"},
		"dangling link":     {"/usr/local/bin/dangling", "leads to /nowhere, which does not exist"},
		"link cycle":        {"/usr/local/bin/loop", "more than 16 symbolic links"},
		"removed package":   {"/etc/gone.conf", "belongs to no installed package"},
		"prerelease":        {"/usr/bin/pre", "cannot name"},
		"two packages":      {"/usr/lib/shared.so", "deb/libbar and deb/libfoo"},
		"unversioned owner": {"/usr/bin/pre", "state the capability's version in provides"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ProvidesFromPaths(context.Background(), map[string]string{"x": tc.file}, one(debianImage()))
			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, "providesFrom.x on linux/arm64")
		})
	}
}

func TestProvidesFromPathsNeedsADatabase(t *testing.T) {
	_, err := ProvidesFromPaths(context.Background(), map[string]string{"x": "/bin/x"}, one(fakeImage{"/bin/x": "elf"}))
	require.ErrorContains(t, err, "neither /var/lib/dpkg/status nor /lib/apk/db/installed")

	noLists := fakeImage{"/bin/x": "elf", DpkgStatusPath: "Package: x\nStatus: install ok installed\nVersion: 1.0-1\n"}
	_, err = ProvidesFromPaths(context.Background(), map[string]string{"x": "/bin/x"}, one(noLists))
	require.ErrorContains(t, err, "no /var/lib/dpkg/info")

	_, err = ProvidesFromPaths(context.Background(), map[string]string{"x": "/bin/x"}, nil)
	require.ErrorContains(t, err, "no filesystem to read")

	_, err = ProvidesFromPaths(context.Background(), map[string]string{"x": "/bin/x"}, []PlatformFiles{{Platform: "linux/amd64"}})
	require.ErrorContains(t, err, "no filesystem to read on linux/amd64")

	got, err := ProvidesFromPaths(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestProvidesFromPathsAcrossPlatforms(t *testing.T) {
	arm, amd := debianImage(), debianImage()
	platforms := []PlatformFiles{{Platform: "linux/arm64", Files: arm}, {Platform: "linux/amd64", Files: amd}}
	from := map[string]string{"ssh": "/usr/bin/ssh"}

	got, err := ProvidesFromPaths(context.Background(), from, platforms)
	require.NoError(t, err)
	require.Equal(t, []string{"ssh@10.0"}, got)

	// A Debian revision is packaging bookkeeping (§9.6), so platforms
	// that differ only there still share a version.
	amd[DpkgStatusPath] = strings.Replace(amd[DpkgStatusPath], "1:10.0p1-7+deb13u4+dhi3", "1:10.0p1-8", 1)
	got, err = ProvidesFromPaths(context.Background(), from, platforms)
	require.NoError(t, err)
	require.Equal(t, []string{"ssh@10.0"}, got)

	amd[DpkgStatusPath] = strings.Replace(amd[DpkgStatusPath], "1:10.0p1-8", "1:10.1p1-1", 1)
	_, err = ProvidesFromPaths(context.Background(), from, platforms)
	require.ErrorContains(t, err, "/usr/bin/ssh is at 10.0 on linux/arm64 and 10.1 on linux/amd64")

	delete(amd, "/usr/bin/ssh")
	_, err = ProvidesFromPaths(context.Background(), from, platforms)
	require.ErrorContains(t, err, "providesFrom.ssh on linux/amd64: /usr/bin/ssh does not exist")
}

func TestProvidesFromPathsApk(t *testing.T) {
	image := fakeImage{
		ApkInstalledPath: strings.Join([]string{
			"C:Q1abc=\nP:busybox\nV:1.37.0-r20\nA:aarch64\nF:bin\nR:busybox\na:0:0:755\nZ:Q1def=\nF:etc\nR:securetty\n",
			"C:Q1ghi=\nP:python3\nV:3.12.12-r0\nF:usr\nF:usr/bin\nR:python3.12\nR:python3\na:0:0:777\n",
		}, "\n"),
		"/bin/busybox":        "elf",
		"/bin/sh":             "-> /bin/busybox",
		"/usr/bin/python3":    "-> python3.12",
		"/usr/bin/python3.12": "elf",
	}
	got, err := ProvidesFromPaths(context.Background(), map[string]string{
		"python": "/usr/bin/python3",
		"sh":     "/bin/sh",
	}, one(image))
	require.NoError(t, err)
	require.Equal(t, []string{"python@3.12.12", "sh@1.37.0"}, got)
}

func TestValidateProvidesFrom(t *testing.T) {
	base := func() *Descriptor {
		return &Descriptor{SchemaVersion: "3", Kind: KindWorkload, Build: "FROM scratch\n"}
	}
	ok := base()
	ok.Provides = []string{"claude@1.0.0"}
	ok.ProvidesFrom = map[string]string{"python": "/usr/bin/python3", "com.example/tool": "/opt/tool/bin/tool"}
	_, err := Validate(ok)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		mutate func(*Descriptor)
		want   string
	}{
		"mixin": {func(d *Descriptor) { d.Kind = KindMixin }, "providesFrom is for kind: workload only"},
		"set": {func(d *Descriptor) {
			d.Kind, d.Build = KindSet, ""
			d.Kits = []Kit{{Ref: "docker.io/example/base:1.0.0"}}
		}, "a set builds none"},
		"bad name":      {func(d *Descriptor) { d.ProvidesFrom = map[string]string{"Python": "/usr/bin/python3"} }, "invalid capability name"},
		"derived space": {func(d *Descriptor) { d.ProvidesFrom = map[string]string{"deb/python": "/usr/bin/python3"} }, "deb/ namespace"},
		"same capability": {func(d *Descriptor) {
			d.ProvidesFrom = map[string]string{"gh": "/usr/bin/gh", "com.docker.kit/gh": "/usr/bin/gh"}
		}, "are the same capability"},
		"already provided": {func(d *Descriptor) {
			d.Provides = []string{"python@3.0.0"}
			d.ProvidesFrom = map[string]string{"python": "/usr/bin/python3"}
		}, "already stated by provides[0]"},
		"relative":     {func(d *Descriptor) { d.ProvidesFrom = map[string]string{"python": "usr/bin/python3"} }, "not an absolute, canonical path"},
		"not clean":    {func(d *Descriptor) { d.ProvidesFrom = map[string]string{"python": "/usr/bin/../bin/python3"} }, "not an absolute, canonical path"},
		"root":         {func(d *Descriptor) { d.ProvidesFrom = map[string]string{"python": "/"} }, "not an absolute, canonical path"},
		"empty":        {func(d *Descriptor) { d.ProvidesFrom = map[string]string{"python": ""} }, "not an absolute, canonical path"},
		"arg in value": {func(d *Descriptor) { d.ProvidesFrom = map[string]string{"python": "/usr/bin/${{ kit.args.python }}"} }, "arg references are not allowed in providesFrom"},
	} {
		t.Run(name, func(t *testing.T) {
			d := base()
			d.ProvidesFrom = map[string]string{"python": "/usr/bin/python3"}
			tc.mutate(d)
			_, err := Validate(d)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestWithoutProvidesFrom(t *testing.T) {
	authored := []byte(`schemaVersion: "3"
kind: workload
# what the agent is
provides: ["claude@1.0.0"]
# what the base ships
providesFrom:
  python: /usr/bin/python3
  ssh: /usr/bin/ssh
version: "1.0.0"
build: |
  FROM scratch
`)
	out, err := WithoutProvidesFrom(authored)
	require.NoError(t, err)
	require.NotContains(t, string(out), "providesFrom")
	require.NotContains(t, string(out), "/usr/bin/python3")
	require.Contains(t, string(out), "# what the agent is")

	d, err := Decode(out)
	require.NoError(t, err)
	require.Empty(t, d.ProvidesFrom)
	require.Equal(t, []string{"claude@1.0.0"}, d.Provides)
	_, err = ValidatePublished(out, d)
	require.NoError(t, err)

	// A descriptor without the field is returned untouched, comments,
	// quoting and all.
	plain := []byte("schemaVersion: \"3\"\nkind: mixin   # overlay\n")
	out, err = WithoutProvidesFrom(plain)
	require.NoError(t, err)
	require.Equal(t, string(plain), string(out))
}

func TestPublishedRefusesProvidesFrom(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: workload
version: "1.0.0"
providesFrom:
  python: /usr/bin/python3
build: |
  FROM scratch
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, d)
	require.ErrorContains(t, err, "published descriptor still carries providesFrom")
}
