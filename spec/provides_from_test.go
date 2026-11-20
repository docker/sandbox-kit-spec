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
			"Package: libfoo\nStatus: install ok installed\nArchitecture: arm64\nMulti-Arch: same\nVersion: 1.2.3-1\n",
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

// A merge key hands the root mapping another mapping's keys, so the
// field can reach a descriptor without being written at the top level.
// Publishing has to remove it from wherever it came, and leave the rest
// of what was merged in place.
func TestWithoutProvidesFromReachesMergedMappings(t *testing.T) {
	const head = "schemaVersion: \"3\"\nkind: workload\nversion: \"1.0.0\"\n"
	const tail = "build: |\n  FROM scratch\n"
	for name, tc := range map[string]struct {
		body        string
		displayName string
		keeps       string
	}{
		"inline": {
			body:        "<<: {providesFrom: {python: /usr/bin/python3}, displayName: Merged}\n",
			displayName: "Merged",
		},
		"inline with nothing else": {
			body: "<<: {providesFrom: {python: /usr/bin/python3}}\n",
		},
		"sequence": {
			body:        "<<: [{providesFrom: {python: /usr/bin/python3}}, {displayName: Second}]\n",
			displayName: "Second",
		},
		"nested": {
			body:        "<<: {displayName: Outer, <<: {providesFrom: {python: /usr/bin/python3}}}\n",
			displayName: "Outer",
		},
		"beside the plain spelling": {
			body:        "providesFrom: {ssh: /usr/bin/ssh}\n<<: {providesFrom: {python: /usr/bin/python3}, displayName: Merged}\n",
			displayName: "Merged",
		},
		// The anchored mapping is a capability's config, which is not
		// this rewrite's to change: the root merges a copy without the
		// field and the config keeps every key it had.
		"alias to a mapping defined elsewhere": {
			body: "capabilities:\n  - type: com.example/thing@1\n    config: &shared\n      providesFrom: {python: /usr/bin/python3}\n      displayName: Shared\n" +
				"<<: *shared\n",
			displayName: "Shared",
			keeps:       "/usr/bin/python3",
		},
	} {
		t.Run(name, func(t *testing.T) {
			authored := []byte(head + tc.body + tail)
			d, err := Decode(authored)
			require.NoError(t, err)
			require.NotEmpty(t, d.ProvidesFrom, "the case must reach the field through the merge")

			out, err := WithoutProvidesFrom(authored)
			require.NoError(t, err)
			pd, err := Decode(out)
			require.NoError(t, err, "%s", out)
			require.Empty(t, pd.ProvidesFrom, "%s", out)
			require.Equal(t, tc.displayName, pd.DisplayName, "what was merged beside the field stays merged")
			require.Equal(t, "FROM scratch\n", pd.Build)
			_, err = ValidatePublished(out, pd)
			require.NoError(t, err, "%s", out)
			if tc.keeps != "" {
				require.Contains(t, string(out), tc.keeps)
				require.Len(t, pd.Capabilities, 1)
			}
		})
	}
}

// Removing the field removes any anchor defined under it. An alias
// elsewhere would then name nothing and the document would stop
// parsing, so each one is given a copy of what it pointed at.
func TestWithoutProvidesFromKeepsAliasesIntoWhatItRemoves(t *testing.T) {
	const head = "schemaVersion: \"3\"\nkind: workload\nversion: \"1.0.0\"\n"
	const tail = "build: |\n  FROM scratch\n"

	scalar := []byte(head + "providesFrom: {python: &command /usr/bin/python3}\ndescription: *command\n" + tail)
	out, err := WithoutProvidesFrom(scalar)
	require.NoError(t, err)
	d, err := Decode(out)
	require.NoError(t, err, "%s", out)
	require.Empty(t, d.ProvidesFrom)
	require.Equal(t, "/usr/bin/python3", d.Description)
	require.NotContains(t, string(out), "*command")
	_, err = ValidatePublished(out, d)
	require.NoError(t, err)

	// The whole value anchored, and used twice more: once as a config,
	// once through an alias nested in another config.
	whole := []byte(head + "providesFrom: &paths\n  python: &py /usr/bin/python3\n  ssh: /usr/bin/ssh\n" +
		"capabilities:\n  - type: com.example/a@1\n    config: *paths\n  - type: com.example/b@1\n    config: {interpreter: *py, all: *paths}\n" + tail)
	out, err = WithoutProvidesFrom(whole)
	require.NoError(t, err)
	d, err = Decode(out)
	require.NoError(t, err, "%s", out)
	require.Empty(t, d.ProvidesFrom)
	require.Len(t, d.Capabilities, 2)
	paths := map[string]any{"python": "/usr/bin/python3", "ssh": "/usr/bin/ssh"}
	require.Equal(t, paths, d.Capabilities[0].Config)
	require.Equal(t, map[string]any{"interpreter": "/usr/bin/python3", "all": paths}, d.Capabilities[1].Config)

	// The other direction needs nothing: an alias inside the field,
	// to an anchor that stays, simply goes with the field.
	inward := []byte(head + "description: &text /usr/bin/python3\nprovidesFrom: {python: *text}\n" + tail)
	out, err = WithoutProvidesFrom(inward)
	require.NoError(t, err)
	d, err = Decode(out)
	require.NoError(t, err, "%s", out)
	require.Empty(t, d.ProvidesFrom)
	require.Equal(t, "/usr/bin/python3", d.Description)
}

// The published rule is that the key is absent, not that it is empty:
// a reader built before the field refuses the key whatever is under it,
// and an empty map or a null decodes exactly like no field at all.
func TestPublishedRefusesProvidesFromInAnySpelling(t *testing.T) {
	const yamlHead = "schemaVersion: \"3\"\nkind: workload\nversion: \"1.0.0\"\n"
	for name, raw := range map[string]string{
		"entries":        yamlHead + "providesFrom: {python: /usr/bin/python3}\n",
		"empty map":      yamlHead + "providesFrom: {}\n",
		"null":           yamlHead + "providesFrom:\n",
		"merged":         yamlHead + "<<: {providesFrom: {python: /usr/bin/python3}}\n",
		"merged empty":   yamlHead + "<<: {providesFrom: {}}\n",
		"json":           `{"schemaVersion":"3","kind":"workload","version":"1.0.0","providesFrom":{}}`,
		"json null":      `{"schemaVersion":"3","kind":"workload","version":"1.0.0","providesFrom":null}`,
		"quoted the key": yamlHead + "\"providesFrom\": {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(raw))
			require.NoError(t, err)
			_, err = ValidatePublished([]byte(raw), d)
			require.ErrorContains(t, err, "published descriptor still carries providesFrom")

			// And publishing's own rewrite clears every one of them.
			out, err := WithoutProvidesFrom([]byte(raw))
			require.NoError(t, err)
			pd, err := Decode(out)
			require.NoError(t, err, "%s", out)
			_, err = ValidatePublished(out, pd)
			require.NoError(t, err, "%s", out)
		})
	}

	// A descriptor that never had the field is not caught by the word
	// appearing somewhere else in it.
	elsewhere := []byte(yamlHead + "description: see providesFrom in the docs\ncapabilities:\n  - type: com.example/a@1\n    config: {providesFrom: {x: /y}}\n")
	d, err := Decode(elsewhere)
	require.NoError(t, err)
	_, err = ValidatePublished(elsewhere, d)
	require.NoError(t, err)
	out, err := WithoutProvidesFrom(elsewhere)
	require.NoError(t, err)
	require.Equal(t, string(elsewhere), string(out))
}

// A co-installable package keeps one file list per architecture, and
// one architecture can be removed while another stays installed. The
// removed one's list still holds the conffiles it left behind; it must
// not own them on the strength of the installed one's name.
func TestProvidesFromPathsJudgesEachArchitecturesList(t *testing.T) {
	image := fakeImage{
		DpkgStatusPath: strings.Join([]string{
			"Package: libfoo\nStatus: install ok installed\nArchitecture: amd64\nMulti-Arch: same\nVersion: 2.0-1\n",
			"Package: libfoo\nStatus: deinstall ok config-files\nArchitecture: i386\nMulti-Arch: same\nVersion: 1.0-1\n",
			"Package: libbar\nStatus: install ok installed\nArchitecture: amd64\nMulti-Arch: same\nVersion: 3.1-2\n",
			"Package: libbar\nStatus: install ok installed\nArchitecture: i386\nMulti-Arch: same\nVersion: 3.1-2\n",
			"Package: plain\nStatus: install ok installed\nArchitecture: amd64\nVersion: 4.2-1\n",
		}, "\n"),
		DpkgInfoDir + "/libfoo:amd64.list": "/usr/lib/x86_64-linux-gnu/libfoo.so.2\n",
		DpkgInfoDir + "/libfoo:i386.list":  "/etc/libfoo-i386.conf\n",
		DpkgInfoDir + "/libbar:amd64.list": "/usr/lib/x86_64-linux-gnu/libbar.so.3\n/usr/share/doc/libbar/copyright\n",
		DpkgInfoDir + "/libbar:i386.list":  "/usr/lib/i386-linux-gnu/libbar.so.3\n/usr/share/doc/libbar/copyright\n",
		// A list named without an architecture belongs to the package
		// of that name, whatever it is installed for.
		DpkgInfoDir + "/plain.list": "/usr/bin/plain\n",
		// Named for an architecture the package is not installed for.
		DpkgInfoDir + "/plain:i386.list":        "/usr/bin/plain32\n",
		"/usr/lib/x86_64-linux-gnu/libfoo.so.2": "elf",
		"/etc/libfoo-i386.conf":                 "conf",
		"/usr/lib/x86_64-linux-gnu/libbar.so.3": "elf",
		"/usr/lib/i386-linux-gnu/libbar.so.3":   "elf",
		"/usr/share/doc/libbar/copyright":       "text",
		"/usr/bin/plain":                        "elf",
		"/usr/bin/plain32":                      "elf",
	}

	got, err := ProvidesFromPaths(context.Background(), map[string]string{
		"foo":     "/usr/lib/x86_64-linux-gnu/libfoo.so.2",
		"bar":     "/usr/lib/i386-linux-gnu/libbar.so.3",
		"bar-doc": "/usr/share/doc/libbar/copyright",
		"plain":   "/usr/bin/plain",
	}, one(image))
	require.NoError(t, err)
	require.Equal(t, []string{"bar-doc@3.1", "bar@3.1", "foo@2.0", "plain@4.2"}, got,
		"a file both architectures of one package list has one owner")

	for _, file := range []string{"/etc/libfoo-i386.conf", "/usr/bin/plain32"} {
		_, err = ProvidesFromPaths(context.Background(), map[string]string{"x": file}, one(image))
		require.ErrorContains(t, err, file+" belongs to no installed package")
	}
}

// One top-level link can lead to another. A path a package recorded and
// a path an author wrote have to end up spelled the same way however
// many links stand between them and the file.
func TestProvidesFromPathsFollowsChainedTopLevelLinks(t *testing.T) {
	image := fakeImage{
		"/bin":                     "-> compat/bin",
		"/compat":                  "-> usr",
		"/sbin":                    "-> /bin",
		DpkgStatusPath:             "Package: tool\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1.2.3-1\n",
		DpkgInfoDir + "/tool.list": "/bin/tool\n/sbin/admin\n",
		"/usr/bin/tool":            "elf",
		"/usr/bin/admin":           "elf",
	}
	got, err := ProvidesFromPaths(context.Background(), map[string]string{
		"direct":   "/usr/bin/tool",
		"recorded": "/bin/tool",
		"middle":   "/compat/bin/tool",
		"twice":    "/sbin/admin",
	}, one(image))
	require.NoError(t, err)
	require.Equal(t, []string{"direct@1.2.3", "middle@1.2.3", "recorded@1.2.3", "twice@1.2.3"}, got)

	// Links that lead in a circle reach no file. The lookup ends, and
	// says the path does not exist rather than never returning.
	circle := fakeImage{
		"/a":                       "-> b",
		"/b":                       "-> a",
		DpkgStatusPath:             "Package: tool\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1.2.3-1\n",
		DpkgInfoDir + "/tool.list": "/a/tool\n",
	}
	_, err = ProvidesFromPaths(context.Background(), map[string]string{"x": "/a/tool"}, one(circle))
	require.ErrorContains(t, err, "/a/tool does not exist in the image")
}

// A directory on the way to a file can be a symbolic link — a default
// JDK, a current release. The package recorded the file where it really
// is, so the directories of a requested path are resolved before the
// file lists are consulted.
func TestProvidesFromPathsResolvesDirectoryLinks(t *testing.T) {
	image := fakeImage{
		"/bin": "-> usr/bin",
		DpkgStatusPath: strings.Join([]string{
			"Package: jdk\nStatus: install ok installed\nArchitecture: arm64\nVersion: 21.0.9-1\n",
			"Package: tool\nStatus: install ok installed\nArchitecture: arm64\nVersion: 1.2.3-1\n",
			"Package: odd\nStatus: install ok installed\nArchitecture: arm64\nVersion: 7.0-1\n",
		}, "\n"),
		DpkgInfoDir + "/jdk.list":  "/usr/lib/jvm/java-21/bin/java\n",
		DpkgInfoDir + "/tool.list": "/opt/tool-1.2/bin/tool\n",
		// Recorded through a directory link, as a package that ships
		// into another package's link would be.
		DpkgInfoDir + "/odd.list":       "/usr/lib/jvm/default/bin/odd\n",
		"/usr/lib/jvm/java-21/bin/java": "elf",
		"/usr/lib/jvm/java-21/bin/odd":  "elf",
		"/usr/lib/jvm/default":          "-> java-21",
		"/usr/lib/jvm/absolute":         "-> /usr/lib/jvm/default",
		"/opt/tool-1.2/bin/tool":        "elf",
		"/opt/tool/current":             "-> ../tool-1.2",
		"/opt/tool/README":              "text",
		// An alternatives-style link in a directory reached through a
		// link: its relative target is relative to the real directory.
		"/usr/lib/jvm/java-21/bin/jvm": "-> java",
		"/usr/bin/java":                "-> /usr/lib/jvm/absolute/bin/jvm",
		"/opt/loop/a":                  "-> b",
		"/opt/loop/b":                  "-> a",
	}
	_, err := ProvidesFromPaths(context.Background(), map[string]string{
		"real":      "/usr/lib/jvm/java-21/bin/java",
		"default":   "/usr/lib/jvm/default/bin/java",
		"chained":   "/usr/lib/jvm/absolute/bin/java",
		"climbing":  "/opt/tool/current/bin/tool",
		"command":   "/bin/java",
		"as-listed": "/usr/lib/jvm/default/bin/odd",
		"listed":    "/usr/lib/jvm/java-21/bin/odd",
	}, one(image))
	require.ErrorContains(t, err, "providesFrom.listed on linux/arm64: /usr/lib/jvm/java-21/bin/odd belongs to no installed package",
		"a recorded path is compared as recorded: only the spelling the package used finds it")

	got, err := ProvidesFromPaths(context.Background(), map[string]string{
		"real":      "/usr/lib/jvm/java-21/bin/java",
		"default":   "/usr/lib/jvm/default/bin/java",
		"chained":   "/usr/lib/jvm/absolute/bin/java",
		"climbing":  "/opt/tool/current/bin/tool",
		"command":   "/bin/java",
		"as-listed": "/usr/lib/jvm/default/bin/odd",
	}, one(image))
	require.NoError(t, err)
	require.Equal(t, []string{
		"as-listed@7.0",
		"chained@21.0.9",
		"climbing@1.2.3",
		"command@21.0.9",
		"default@21.0.9",
		"real@21.0.9",
	}, got)

	for file, want := range map[string]string{
		"/usr/lib/jvm/default/bin/nope": "/usr/lib/jvm/default/bin/nope does not exist in the image",
		"/usr/lib/jvm/missing/bin/java": "/usr/lib/jvm/missing/bin/java does not exist in the image",
		"/usr/lib/jvm/default/bin":      "/usr/lib/jvm/default/bin is a directory",
		"/opt/loop/a/tool":              "/opt/loop/a/tool does not exist in the image",
	} {
		_, err := ProvidesFromPaths(context.Background(), map[string]string{"x": file}, one(image))
		require.ErrorContains(t, err, want)
	}
}

// Resolution has to end at the file that is actually there. Two ways of
// reading a path as text end somewhere else, and each of them states
// the version of a package that does not own the file.
func TestProvidesFromPathsEndsAtTheRealFile(t *testing.T) {
	status := func(packages ...string) string {
		var out []string
		for i := 0; i+1 < len(packages); i += 2 {
			out = append(out, "Package: "+packages[i]+"\nStatus: install ok installed\nArchitecture: arm64\nVersion: "+packages[i+1]+"\n")
		}
		return strings.Join(out, "\n")
	}

	// A list can hold a path from before a directory on it became a
	// link. The file there now is another package's.
	stale := fakeImage{
		DpkgStatusPath:                status("old", "1.0-1", "new", "2.0-1"),
		DpkgInfoDir + "/old.list":     "/opt/app/current/bin/tool\n",
		DpkgInfoDir + "/new.list":     "/opt/app/release-2/bin/tool\n",
		"/opt/app/release-2/bin/tool": "elf",
		"/opt/app/current":            "-> release-2",
	}
	got, err := ProvidesFromPaths(context.Background(), map[string]string{"tool": "/opt/app/current/bin/tool"}, one(stale))
	require.NoError(t, err)
	require.Equal(t, []string{"tool@2.0"}, got, "the owner of the real file, not of the spelling")

	// ".." after a linked directory is that directory's parent. Read as
	// text, sub/../real-tool is the neighbour of sub; in the image it
	// is the neighbour of wherever sub leads.
	climb := fakeImage{
		DpkgStatusPath:             status("near", "1.0-1", "far", "2.0-1"),
		DpkgInfoDir + "/near.list": "/opt/app/bin/real-tool\n",
		DpkgInfoDir + "/far.list":  "/srv/real-tool\n",
		"/opt/app/bin/real-tool":   "elf",
		"/srv/real-tool":           "elf",
		"/srv/other/keep":          "x",
		"/opt/app/bin/sub":         "-> /srv/other",
		"/opt/app/bin/tool":        "-> sub/../real-tool",
		"/opt/app/bin/dotted":      "-> ./sub/./../real-tool",
		// The same shape in a directory link rather than a file link.
		"/opt/app/via":          "-> bin/sub/..",
		"/opt/app/bin/up":       "-> ..",
		"/opt/app/bin/updir":    "-> sub/..",
		"/opt/app/bin/straight": "-> ../bin/real-tool",
	}
	got, err = ProvidesFromPaths(context.Background(), map[string]string{
		"tool":     "/opt/app/bin/tool",
		"dotted":   "/opt/app/bin/dotted",
		"via":      "/opt/app/via/real-tool",
		"straight": "/opt/app/bin/straight",
	}, one(climb))
	require.NoError(t, err)
	require.Equal(t, []string{"dotted@2.0", "straight@1.0", "tool@2.0", "via@2.0"}, got)

	for _, file := range []string{"/opt/app/bin/up", "/opt/app/bin/updir"} {
		_, err = ProvidesFromPaths(context.Background(), map[string]string{"x": file}, one(climb))
		require.ErrorContains(t, err, "is a directory", "%s leads to a directory", file)
	}
}

// A top-level link is an alias for the directory it finally reaches,
// worked out the same way: through as many links as it takes, with ".."
// meaning the parent of what was reached.
func TestProvidesFromPathsResolvesTopLevelAliasesFully(t *testing.T) {
	image := fakeImage{
		"/bin":                     "-> compat/../usr/bin",
		"/compat":                  "-> srv/elsewhere",
		"/srv/elsewhere/keep":      "x",
		"/srv/usr/bin/tool":        "elf",
		"/usr/bin/tool":            "elf",
		"/dangling":                "-> nowhere",
		DpkgStatusPath:             "Package: tool\nStatus: install ok installed\nArchitecture: arm64\nVersion: 1.2.3-1\n\nPackage: other\nStatus: install ok installed\nArchitecture: arm64\nVersion: 9.9-1\n",
		DpkgInfoDir + "/tool.list": "/bin/tool\n",
		// What reading /bin's target as text would have reached.
		DpkgInfoDir + "/other.list": "/usr/bin/tool\n/dangling/thing\n",
	}
	got, err := ProvidesFromPaths(context.Background(), map[string]string{
		"recorded": "/bin/tool",
		"real":     "/srv/usr/bin/tool",
		"textual":  "/usr/bin/tool",
	}, one(image))
	require.NoError(t, err)
	require.Equal(t, []string{"real@1.2.3", "recorded@1.2.3", "textual@9.9"}, got)
}

// A walk that cannot be completed names no file. The components left
// over are not a path to fall back on: a ".." among them would step out
// of a directory that was never entered and land on a file that exists.
func TestProvidesFromPathsRefusesAWalkThatDoesNotArrive(t *testing.T) {
	image := fakeImage{
		DpkgStatusPath:             "Package: tool\nStatus: install ok installed\nArchitecture: arm64\nVersion: 1.2.3-1\n",
		DpkgInfoDir + "/tool.list": "/opt/tool\n/opt/dir/inner\n",
		"/opt/tool":                "elf",
		"/opt/dir/inner":           "elf",
		"/opt/plain":               "a regular file",
		"/opt/missing-then-up":     "-> missing/../tool",
		"/opt/file-then-up":        "-> plain/../tool",
		"/opt/file-then-down":      "-> plain/tool",
		"/opt/deep-missing":        "-> missing/also/../../tool",
		"/opt/dirlink-missing":     "-> missing/..",
		"/opt/dirlink-file":        "-> plain/..",
		"/opt/good":                "-> dir/../tool",
		"/opt/file-with-slash":     "-> tool/",
		"/opt/file-with-dot":       "-> tool/.",
		"/opt/dir-with-slash":      "-> dir/",
	}
	for file, want := range map[string]string{
		"/opt/missing-then-up":      "/opt/missing-then-up leads to /opt/missing/../tool, which does not exist in the image",
		"/opt/file-then-up":         "/opt/file-then-up leads to /opt/plain/../tool, which does not exist in the image",
		"/opt/file-then-down":       "/opt/file-then-down leads to /opt/plain/tool, which does not exist in the image",
		"/opt/deep-missing":         "which does not exist in the image",
		"/opt/dirlink-missing/tool": "/opt/dirlink-missing/tool does not exist in the image",
		"/opt/dirlink-file/tool":    "/opt/dirlink-file/tool does not exist in the image",
		"/opt/plain/tool":           "/opt/plain/tool does not exist in the image",
		"/opt/missing/tool":         "/opt/missing/tool does not exist in the image",
		// A slash after a file asks for a directory that is not one.
		"/opt/file-with-slash": "/opt/file-with-slash leads to /opt/tool/, which does not exist in the image",
		"/opt/file-with-dot":   "/opt/file-with-dot leads to /opt/tool/., which does not exist in the image",
		"/opt/dir-with-slash":  "/opt/dir/ is a directory",
	} {
		_, err := ProvidesFromPaths(context.Background(), map[string]string{"x": file}, one(image))
		require.ErrorContains(t, err, want, file)
	}

	// The same shape through a directory that is there still resolves.
	got, err := ProvidesFromPaths(context.Background(), map[string]string{"x": "/opt/good"}, one(image))
	require.NoError(t, err)
	require.Equal(t, []string{"x@1.2.3"}, got)
}

// Everything the removal changes in place is something an alias can
// name. Whatever else uses that alias — here a capability's config,
// which is opaque to this rewrite — has to read what it read before.
func TestWithoutProvidesFromLeavesSharedNodesAsTheyWere(t *testing.T) {
	const head = "schemaVersion: \"3\"\nkind: workload\nversion: \"1.0.0\"\n"
	const tail = "build: |\n  FROM scratch\n"
	uses := func(alias string) string {
		return "capabilities:\n  - type: com.example/thing@1\n    config: {uses: *" + alias + "}\n"
	}
	paths := map[string]any{"python": "/usr/bin/python3"}

	for name, tc := range map[string]struct {
		body        string
		config      any
		displayName string
	}{
		// The sequence a merge key lists its sources in.
		"anchored merge sequence": {
			body:        "<<: &shared [{providesFrom: {python: /usr/bin/python3}}, {displayName: Second}]\n" + uses("shared"),
			config:      []any{map[string]any{"providesFrom": paths}, map[string]any{"displayName": "Second"}},
			displayName: "Second",
		},
		// A source inside an unanchored sequence.
		"anchored element of a merge sequence": {
			body:        "<<: [&shared {providesFrom: {python: /usr/bin/python3}, displayName: First}, {description: Second}]\n" + uses("shared"),
			config:      map[string]any{"providesFrom": paths, "displayName": "First"},
			displayName: "First",
		},
		// A single anchored source.
		"anchored merge mapping": {
			body:        "<<: &shared {providesFrom: {python: /usr/bin/python3}, displayName: Only}\n" + uses("shared"),
			config:      map[string]any{"providesFrom": paths, "displayName": "Only"},
			displayName: "Only",
		},
		// The field reaches the anchored mapping through a merge of its
		// own, so the mapping is edited one level further in.
		"anchored mapping with a nested merge": {
			body:        "<<: &shared {displayName: Outer, <<: {providesFrom: {python: /usr/bin/python3}}}\n" + uses("shared"),
			config:      map[string]any{"providesFrom": paths, "displayName": "Outer"},
			displayName: "Outer",
		},
		// An anchored sequence whose only source goes away entirely.
		"anchored sequence left empty": {
			body:   "<<: &shared [{providesFrom: {python: /usr/bin/python3}}]\n" + uses("shared"),
			config: []any{map[string]any{"providesFrom": paths}},
		},
		// The field's own value.
		"anchored value": {
			body:   "providesFrom: &shared {python: /usr/bin/python3}\n" + uses("shared"),
			config: paths,
		},
	} {
		t.Run(name, func(t *testing.T) {
			authored := []byte(head + tc.body + tail)
			before, err := Decode(authored)
			require.NoError(t, err)
			require.NotEmpty(t, before.ProvidesFrom)
			require.Equal(t, map[string]any{"uses": tc.config}, before.Capabilities[0].Config, "the case is not what it says")

			out, err := WithoutProvidesFrom(authored)
			require.NoError(t, err)
			after, err := Decode(out)
			require.NoError(t, err, "%s", out)
			require.Empty(t, after.ProvidesFrom, "%s", out)
			require.Equal(t, before.Capabilities[0].Config, after.Capabilities[0].Config,
				"the config reads what it read before:\n%s", out)
			require.Equal(t, tc.displayName, after.DisplayName)
			_, err = ValidatePublished(out, after)
			require.NoError(t, err, "%s", out)
		})
	}
}

// The kind rule is about the field being written, not about it having
// entries: the schema refuses the key on a mixin or a set whatever is
// under it, and validation must not accept a descriptor an editor
// already marked wrong — nor let the build drop the field in silence.
func TestProvidesFromKindRuleAppliesToAnEmptyField(t *testing.T) {
	const mixin = "schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\n"
	const set = "schemaVersion: \"3\"\nkind: set\nkits:\n  - ref: docker.io/example/base:1.0.0\n"
	const workload = "schemaVersion: \"3\"\nkind: workload\nversion: \"1.0.0\"\nbuild: |\n  FROM scratch\n"

	for name, tc := range map[string]struct{ raw, want string }{
		"mixin, empty map":   {mixin + "providesFrom: {}\n", "providesFrom is for kind: workload only"},
		"mixin, null":        {mixin + "providesFrom:\n", "providesFrom is for kind: workload only"},
		"mixin, merged":      {mixin + "<<: {providesFrom: {}}\n", "providesFrom is for kind: workload only"},
		"mixin, merged null": {mixin + "<<: {providesFrom: }\n", "providesFrom is for kind: workload only"},
		"set, empty map":     {set + "providesFrom: {}\n", "a set builds none"},
		"set, null":          {set + "providesFrom:\n", "a set builds none"},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(tc.raw))
			require.NoError(t, err)
			_, err = ValidateDeclarations([]byte(tc.raw), d)
			require.ErrorContains(t, err, tc.want)
			// Once, however the field was spelled.
			require.Equal(t, 1, strings.Count(err.Error(), tc.want), "%v", err)
		})
	}

	// On a workload an empty field asks for nothing, and is dropped at
	// publish like a full one.
	for _, body := range []string{"providesFrom: {}\n", "providesFrom:\n"} {
		raw := []byte(workload + body)
		d, err := Decode(raw)
		require.NoError(t, err)
		_, err = ValidateDeclarations(raw, d)
		require.NoError(t, err)
		out, err := WithoutProvidesFrom(raw)
		require.NoError(t, err)
		require.NotContains(t, string(out), "providesFrom")
	}

	// A descriptor with no such field is untouched by any of this.
	plain := []byte(mixin)
	d, err := Decode(plain)
	require.NoError(t, err)
	_, err = ValidateDeclarations(plain, d)
	require.NoError(t, err)
}
