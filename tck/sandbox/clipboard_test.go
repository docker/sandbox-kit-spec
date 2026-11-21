package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// clipboardStub is a bridge over $CLIP granting $GRANTS (rt, ri, wt, wi:
// read and write text and images), broken one way at a time by $BROKEN.
// Like the real tools, xclip and xsel write standard input unless asked
// for output, so a probe that runs a writing spelling while looking for
// text replaces the text it is looking for.
const clipboardStub = `#!/bin/sh
name=$(basename "$0")
has() { case " $GRANTS " in *" $1 "*) return 0 ;; esac; return 1; }
out=0 list=0 trim=0 type=""
while [ $# -gt 0 ]; do
  case "$1" in
  -o | --output) out=1 ;;
  -l | --list-types) list=1 ;;
  -n | --no-newline | --trim-newline) trim=1 ;;
  -t | --type) shift; type="$1" ;;
  -selection) shift ;;
  esac
  shift
done
case "$name" in wl-paste | pbpaste) out=1 ;; esac
if [ "$type" = TARGETS ]; then list=1; fi
alias=0
case "$type" in UTF8_STRING | STRING | TEXT | COMPOUND_TEXT | "text/plain;"*) alias=1 ;; esac
case "$type" in image/*) kind=image ;; *) kind=text ;; esac
if [ "$out" = 1 ]; then
  if [ "$list" = 1 ]; then
    [ "$BROKEN" != list-omits ] || exit 0
    if [ "$BROKEN" = leaks-list-stderr ] && [ -f "$CLIP/text" ]; then cat "$CLIP/text" >&2; exit 1; fi
    if [ "$BROKEN" = leaks-list-image-stderr ] && [ -f "$CLIP/image.png" ]; then cat "$CLIP/image.png" >&2; exit 1; fi
    if has rt && [ -f "$CLIP/text" ]; then echo text/plain; fi
    if has ri && [ -f "$CLIP/image.png" ]; then echo image/png; fi
    exit 0
  fi
  if [ "$kind" = image ]; then
    if [ "$type" = image/x-png ] && [ "$BROKEN" = misses-x-png ]; then exit 0; fi
    if [ "$type" = image/x-png ] && [ "$BROKEN" = leaks-x-png ] && [ -f "$CLIP/image.png" ]; then cat "$CLIP/image.png"; exit 0; fi
    if [ "$BROKEN" = leaks-image-stderr ] && [ -f "$CLIP/image.png" ]; then cat "$CLIP/image.png" >&2; exit 1; fi
    if [ "$type" = image/x-png ] && [ "$BROKEN" = leaks-x-png-stderr ] && [ -f "$CLIP/image.png" ]; then cat "$CLIP/image.png" >&2; exit 1; fi
    if ! has ri && [ "$BROKEN" = denial-diagnostic ]; then echo "clipboard read denied: target $type not available" >&2; exit 1; fi
    has ri || exit 0
    if [ "$BROKEN" = corrupt ]; then printf 'not the image'; exit 0; fi
    if [ -f "$CLIP/image.png" ]; then cat "$CLIP/image.png"; elif [ "$BROKEN" = serves-after-removal ]; then cat "$CLIP/stale.png"; fi
    exit 0
  fi
  # Like the real tool, a typeless wl-paste serves whatever admitted type
  # is offered when text is not.
  if [ "$name" = wl-paste ] && [ -z "$type" ] && ! has rt && has ri && [ -f "$CLIP/image.png" ]; then cat "$CLIP/image.png"; exit 0; fi
  if [ "$BROKEN" = leaks-stderr ] && [ -f "$CLIP/text" ]; then printf 'denied: %s\n' "$(cat "$CLIP/text")" >&2; exit 1; fi
  if ! has rt && [ "$BROKEN" = denial-diagnostic ]; then echo "clipboard read denied: enable com.docker.sandbox/clipboard@1" >&2; exit 1; fi
  if [ "$alias" = 1 ] && [ "$BROKEN" = leaks-prefix ] && [ -f "$CLIP/text" ]; then cut -c1-12 "$CLIP/text"; exit 0; fi
  if [ "$type" = COMPOUND_TEXT ] && [ "$BROKEN" = leaks-compound ] && [ -f "$CLIP/text" ]; then cat "$CLIP/text"; exit 0; fi
  if [ "$alias" = 1 ] && [ "$BROKEN" = misses-alias ]; then exit 0; fi
  if has rt || { [ "$alias" = 1 ] && [ "$BROKEN" = leaks-alias ]; }; then
    [ -f "$CLIP/text" ] || exit 0
    if [ "$BROKEN" = corrupt-text ]; then printf 'not the text'; exit 0; fi
    cat "$CLIP/text"
    if [ "$name" = wl-paste ] && [ "$trim" = 0 ] && [ "$BROKEN" != drops-newline ]; then echo; fi
  fi
  exit 0
fi
cat >"$CLIP/.write"
if [ "$kind" = image ]; then
  has wi || exit 1
  mv "$CLIP/.write" "$CLIP/image.png"
  rm -f "$CLIP/text"
  exit 0
fi
has wt || exit 1
if [ "$trim" = 1 ]; then
  body=$(cat "$CLIP/.write"; printf x)
  body=${body%x}
  case "$body" in *'
') body=${body%?} ;; esac
  printf '%s' "$body" >"$CLIP/.write"
fi
mv "$CLIP/.write" "$CLIP/text"
rm -f "$CLIP/image.png"
`

const clipboardAllGrants = "rt ri wt wi"

func clipboardFixturePNG() string {
	return filepath.Join(FixtureDir, fixtureClipboardWorkload, "clipboard.png")
}

// clipboardProbe runs the workload fixture's probe with the stub helpers
// first on PATH.
func clipboardProbe(t *testing.T, clip, grants, broken string, omit []string, args ...string) ([]byte, error) {
	t.Helper()
	bin := t.TempDir()
	omitted := map[string]bool{}
	for _, name := range omit {
		omitted[name] = true
	}
	for _, name := range []string{"wl-paste", "xclip", "xsel", "pbpaste", "wl-copy", "pbcopy", "clip.exe"} {
		if !omitted[name] {
			require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(clipboardStub), 0o700))
		}
	}
	// The probe sees the stubs and the few tools it runs, never the
	// host's PATH: a real pbpaste or xclip there would answer for a stub
	// a case omitted, and read the developer's own clipboard doing it.
	for _, tool := range []string{"sh", "sha256sum", "cut", "grep", "wc", "tr", "cat", "basename", "mv", "rm", "false"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("probe tests need %s on PATH", tool)
		}
		require.NoError(t, os.Symlink(path, filepath.Join(bin, tool)))
	}
	cmd := exec.CommandContext(t.Context(), filepath.Join(bin, "sh"), append([]string{filepath.Join(FixtureDir, fixtureClipboardWorkload, "probe")}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+bin, "CLIP="+clip, "GRANTS="+grants, "BROKEN="+broken,
		"KIT_TCK_CLIPBOARD_PNG="+clipboardFixturePNG())
	return cmd.CombinedOutput()
}

func writeClip(t *testing.T, clip, name string, data []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(clip, name), data, 0o600))
}

func TestClipboardInterfaceProbe(t *testing.T) {
	clip := t.TempDir()
	for _, direction := range []string{"read", "write"} {
		out, err := clipboardProbe(t, clip, clipboardAllGrants, "", nil, "interface", direction)
		require.NoError(t, err, "%s", out)
	}
	_, err := clipboardProbe(t, clip, clipboardAllGrants, "", []string{"clip.exe"}, "interface", "write")
	require.Error(t, err, "a missing write helper must fail discovery")
	_, err = clipboardProbe(t, clip, clipboardAllGrants, "", []string{"pbpaste"}, "interface", "read")
	require.Error(t, err, "a missing read helper must fail discovery")
	out, err := clipboardProbe(t, clip, clipboardAllGrants, "", []string{"clip.exe"}, "interface", "read")
	require.NoError(t, err, "read discovery does not depend on write helpers: %s", out)
}

func TestClipboardTextProbe(t *testing.T) {
	marker := clipboardMarker("text")
	for _, broken := range []string{"", "corrupt-text", "misses-alias", "list-omits", "drops-newline"} {
		t.Run(broken, func(t *testing.T) {
			clip := t.TempDir()
			writeClip(t, clip, clipboardTextFile, []byte(marker))
			out, err := clipboardProbe(t, clip, clipboardAllGrants, broken, nil, "text", textDigest(marker), textDigest(marker+"\n"))
			if broken == "" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err, "probe must detect %s", broken)
			}
		})
	}
}

func TestClipboardImageProbe(t *testing.T) {
	for _, broken := range []string{"", "list-omits", "corrupt", "misses-x-png"} {
		t.Run(broken, func(t *testing.T) {
			clip := t.TempDir()
			data, digest, err := clipboardPNG()
			require.NoError(t, err)
			writeClip(t, clip, clipboardImageFile, data)
			out, err := clipboardProbe(t, clip, clipboardAllGrants, broken, nil, "image", digest)
			if broken == "" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err, "probe must detect %s", broken)
			}
		})
	}
}

func TestClipboardEmptyProbe(t *testing.T) {
	data, _, err := clipboardPNG()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, broken string
		leftover     string
	}{
		{name: "empty"},
		{name: "serves a removed image", broken: "serves-after-removal"},
		{name: "text left behind", leftover: clipboardTextFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clip := t.TempDir()
			writeClip(t, clip, "stale.png", data)
			if tc.leftover != "" {
				writeClip(t, clip, tc.leftover, []byte("left behind"))
			}
			for _, mode := range []string{"empty", "no-image"} {
				out, err := clipboardProbe(t, clip, clipboardAllGrants, tc.broken, nil, mode)
				switch {
				case tc.broken == "" && tc.leftover == "":
					require.NoError(t, err, "%s: %s", mode, out)
				case mode == "empty" || tc.broken != "":
					require.Error(t, err, "%s must detect %s", mode, tc.name)
				default:
					require.NoError(t, err, "no-image does not judge text: %s", out)
				}
			}
		})
	}
}

// Without a read grant the image is withheld under its alias and on
// stderr too, while non-sensitive denial diagnostics remain allowed.
func TestClipboardNoImageAliasProbe(t *testing.T) {
	for _, tc := range []struct {
		broken string
		leaks  bool
	}{
		{broken: ""},
		{broken: "denial-diagnostic"},
		{broken: "leaks-x-png", leaks: true},
		{broken: "leaks-image-stderr", leaks: true},
		{broken: "leaks-x-png-stderr", leaks: true},
		{broken: "leaks-list-image-stderr", leaks: true},
	} {
		t.Run(tc.broken, func(t *testing.T) {
			clip := t.TempDir()
			data, _, err := clipboardPNG()
			require.NoError(t, err)
			writeClip(t, clip, clipboardImageFile, data)
			out, err := clipboardProbe(t, clip, "wt wi", tc.broken, nil, "no-image")
			if tc.leaks {
				require.Error(t, err, "probe must detect %s", tc.broken)
			} else {
				require.NoError(t, err, "%s", out)
			}
		})
	}
}

func TestClipboardNoTextProbe(t *testing.T) {
	for _, tc := range []struct {
		name, grants, broken string
		// image seeds an image beside the text.
		image bool
		mode  string
		leaks bool
	}{
		{name: "filtered", grants: "ri wt", mode: "no-text"},
		{name: "no grant", grants: "", mode: "no-text"},
		{name: "no grant beside an image", grants: "", image: true, mode: "no-text"},
		{name: "non-sensitive denial diagnostic on stderr", grants: "ri wt", broken: "denial-diagnostic", mode: "no-text"},
		{name: "non-sensitive denial diagnostic beside an image", grants: "ri wt", broken: "denial-diagnostic", image: true, mode: "no-text-beside-image"},
		{name: "unfiltered", grants: clipboardAllGrants, mode: "no-text", leaks: true},
		{name: "alias leaks", grants: "ri wt", broken: "leaks-alias", mode: "no-text", leaks: true},
		// A search for the seeded text misses a truncated disclosure.
		{name: "prefix leaks", grants: "ri wt", broken: "leaks-prefix", mode: "no-text", leaks: true},
		{name: "compound text leaks", grants: "ri wt", broken: "leaks-compound", mode: "no-text", leaks: true},
		{name: "compound text leaks beside an image", grants: "ri wt", broken: "leaks-compound", image: true, mode: "no-text-beside-image", leaks: true},
		{name: "prefix leaks beside an image", grants: "ri wt", broken: "leaks-prefix", image: true, mode: "no-text-beside-image", leaks: true},
		{name: "stderr leaks", grants: "ri wt", broken: "leaks-stderr", mode: "no-text", leaks: true},
		{name: "stderr leaks beside an image", grants: "ri wt", broken: "leaks-stderr", image: true, mode: "no-text-beside-image", leaks: true},
		{name: "stderr leaks without grant", grants: "", broken: "leaks-stderr", mode: "no-text", leaks: true},
		{name: "list stderr leaks", grants: "ri wt", broken: "leaks-list-stderr", mode: "no-text", leaks: true},
		// A typeless wl-paste serving the admitted image is not a leak,
		// which is why that mode exists.
		{name: "admitted image beside the text", grants: "ri wt", image: true, mode: "no-text-beside-image"},
		{name: "admitted image under the strict mode", grants: "ri wt", image: true, mode: "no-text", leaks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clip := t.TempDir()
			marker := "kit-tck-clipboard-secret-marker"
			writeClip(t, clip, clipboardTextFile, []byte(marker))
			if tc.image {
				data, _, err := clipboardPNG()
				require.NoError(t, err)
				writeClip(t, clip, clipboardImageFile, data)
			}
			out, err := clipboardProbe(t, clip, tc.grants, tc.broken, nil, tc.mode, marker)
			if tc.leaks {
				require.Error(t, err, "%s must fail", tc.mode)
			} else {
				require.NoError(t, err, "%s", out)
			}
			got, err := os.ReadFile(filepath.Join(clip, clipboardTextFile))
			require.NoError(t, err)
			require.Equal(t, marker, string(got), "searching for text must not run a spelling that writes it")
		})
	}
}

func TestClipboardPipeProbe(t *testing.T) {
	clip := t.TempDir()
	marker := clipboardMarker("pipe")
	for _, argv := range clipboardWriters {
		data, _, err := clipboardPNG()
		require.NoError(t, err)
		writeClip(t, clip, clipboardImageFile, data)
		out, err := clipboardProbe(t, clip, clipboardAllGrants, "", nil, append([]string{"pipe", marker}, argv...)...)
		require.NoError(t, err, "%v: %s", argv, out)
		got, err := os.ReadFile(filepath.Join(clip, clipboardTextFile))
		require.NoError(t, err)
		require.Equal(t, marker, string(got), "%v must receive the text byte for byte", argv)
		require.NoFileExists(t, filepath.Join(clip, clipboardImageFile), "the stub replaces the whole clipboard")
	}
	_, err := clipboardProbe(t, clip, clipboardAllGrants, "", nil, "pipe", marker, "false")
	require.Error(t, err, "the helper's failure is the probe's result")
}

func TestClipboardCopyImageProbe(t *testing.T) {
	png, err := os.ReadFile(clipboardFixturePNG())
	require.NoError(t, err)
	sum := sha256.Sum256(png)
	want := hex.EncodeToString(sum[:])

	clip := t.TempDir()
	out, err := clipboardProbe(t, clip, clipboardAllGrants, "", nil, "png-digest")
	require.NoError(t, err, "%s", out)
	require.Equal(t, want, strings.TrimSpace(string(out)))
	for _, argv := range clipboardImageWriters {
		writeClip(t, clip, clipboardTextFile, []byte("before"))
		out, err := clipboardProbe(t, clip, clipboardAllGrants, "", nil, append([]string{"copy-image"}, argv...)...)
		require.NoError(t, err, "%v: %s", argv, out)
		got, err := os.ReadFile(filepath.Join(clip, clipboardImageFile))
		require.NoError(t, err)
		require.Equal(t, png, got, "%v must receive the PNG byte for byte", argv)
	}
	_, err = clipboardProbe(t, clip, "rt ri wt", "", nil, "copy-image", "wl-copy", "--type", "image/png")
	require.Error(t, err, "a refused image write is the probe's result")
}

func TestClipboardProbesPass(t *testing.T) {
	var selected []check
	for _, c := range checks {
		if c.capability == capClipboard {
			selected = append(selected, c)
		}
	}
	require.Len(t, selected, len(clipboardChecks))
	rep := runAgainstFake(t, "", selected...)
	require.False(t, rep.Failed(), "%s", rep)
}
