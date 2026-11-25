package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	capClipboard = spec.CapabilityClipboard

	fixtureClipboardWorkload = "clipboard-workload"
	// fixtureClipboard activates the capability alone: a normal clipboard.
	fixtureClipboard = "clipboard"
	// fixtureClipboardRead and fixtureClipboardWrite each turn the other
	// direction off, so a check can compose exactly the grant it judges.
	fixtureClipboardRead  = "clipboard-read"
	fixtureClipboardWrite = "clipboard-write"
	// fixtureClipboardFiltered admits images in and text out.
	fixtureClipboardFiltered = "clipboard-filtered"

	// clipboardImageFile and clipboardTextFile are the isolated host
	// clipboard conformance.md defines: present means the clipboard holds
	// that content, absent means it holds none.
	clipboardImageFile = "image.png"
	clipboardTextFile  = "text"
)

var clipboardChecks = []check{
	{requirement: "clipboard@1/read-helpers", capability: capClipboard, run: clipboardGuestHelpers("read")},
	{requirement: "clipboard@1/write-helpers", capability: capClipboard, run: clipboardGuestHelpers("write")},
	{requirement: "clipboard@1/text-delivered", capability: capClipboard, run: clipboardTextDelivered},
	{requirement: "clipboard@1/image-delivered", capability: capClipboard, run: clipboardImageDelivered},
	{requirement: "clipboard@1/text-replaced", capability: capClipboard, run: clipboardTextReplaced},
	{requirement: "clipboard@1/typed-write", capability: capClipboard, run: clipboardTypedWrite},
	{requirement: "clipboard@1/read-filtered", capability: capClipboard, run: clipboardReadFiltered},
	{requirement: "clipboard@1/write-filtered", capability: capClipboard, run: clipboardWriteFiltered},
	{requirement: "clipboard@1/read-withheld", capability: capClipboard, run: clipboardReadWithheld},
	{requirement: "clipboard@1/write-refused", capability: capClipboard, run: clipboardWriteRefused},
	{requirement: "clipboard@1/filters-union", capability: capClipboard, run: clipboardFiltersUnion},
}

// clipboardWriters are the text helper spellings the page names. Each
// reads the text from standard input.
var clipboardWriters = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
	{"clip.exe"},
}

// clipboardImageWriters are the typed spellings that put a PNG on the
// clipboard.
var clipboardImageWriters = [][]string{
	{"wl-copy", "--type", "image/png"},
	{"xclip", "-selection", "clipboard", "-t", "image/png"},
}

// testClipboard is the suite's side of an isolated host clipboard: the
// directory the adapter binds in place of the user's clipboard.
type testClipboard struct{ dir string }

func newTestClipboard() (*testClipboard, error) {
	dir, err := os.MkdirTemp("", "kit-tck-clipboard-*")
	if err != nil {
		return nil, err
	}
	return &testClipboard{dir: dir}, nil
}

func (c *testClipboard) remove() { _ = os.RemoveAll(c.dir) }

// put replaces one file by rename, so a runtime reading mid-update sees
// the old content or the new, never a prefix.
func (c *testClipboard) put(name string, data []byte) error {
	tmp, err := os.CreateTemp(c.dir, "."+name+"-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(c.dir, name))
}

func (c *testClipboard) clear(name string) error {
	err := os.Remove(filepath.Join(c.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// text reads the host text clipboard; absent reads as empty, which the
// suite never seeds, so it cannot be mistaken for a successful write.
func (c *testClipboard) text() (string, error) {
	b, err := os.ReadFile(filepath.Join(c.dir, clipboardTextFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// clipboardSandbox creates every clipboard sandbox bound to a fresh test
// clipboard, including those lacking a direction: a runtime that reaches a real
// clipboard by mistake must reach this one instead, where it is seen.
func clipboardSandbox(ctx context.Context, e *Env, kits []string) (string, *testClipboard, func(), *report.Finding) {
	clip, err := newTestClipboard()
	if err != nil {
		f := report.Failf("test clipboard: %v", err)
		return "", nil, func() {}, &f
	}
	id, cleanup, err := e.sandboxWith(ctx, kits, adapter.CreateOptions{ClipboardIsolated: clip.dir})
	done := func() { cleanup(); clip.remove() }
	if err != nil {
		f := report.Failf("create with isolated host clipboard: %v", err)
		return "", nil, done, &f
	}
	return id, clip, done, nil
}

// clipboardPNG encodes a small image whose pixels are random, so two
// seeded images never share a digest.
func clipboardPNG() ([]byte, string, error) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	seed := make([]byte, 3)
	if _, err := rand.Read(seed); err != nil {
		return nil, "", err
	}
	for i := 0; i < 16; i++ {
		img.Set(i%4, i/4, color.NRGBA{R: seed[0] + byte(i), G: seed[1], B: seed[2], A: 0xff})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

// clipboardMarker is text no runtime could produce by accident: unique per
// call, multi-line, and outside ASCII, so a lossy bridge is visible.
func clipboardMarker(label string) string {
	return fmt.Sprintf("kit-tck-clipboard-%s-%s\nsecond line ☕", label, rand.Text())
}

// digest returns a file's SHA-256, or "" when the clipboard does not hold
// that type.
func (c *testClipboard) digest(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(c.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func textDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// probe runs one probe mode and attributes a failure to what was being
// observed.
func probe(ctx context.Context, e *Env, id, what string, args ...string) *report.Finding {
	if _, f := execOutput(ctx, e, id, append([]string{"kit-tck-clipboard"}, args...)...); f != nil {
		f.Detail = what + ": " + f.Detail
		return f
	}
	return nil
}

func clipboardGuestHelpers(direction string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		id, _, cleanup, f := clipboardSandbox(ctx, e, []string{fixtureClipboardWorkload, fixtureClipboard})
		defer cleanup()
		if f != nil {
			return []report.Finding{*f}
		}
		if f := probe(ctx, e, id, "exec session", "interface", direction); f != nil {
			return []report.Finding{*f}
		}
		// Create may return before the entrypoint publishes its capture.
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for {
			result, f := execOutput(ctx, e, id, "cat", "/var/tmp/kit-tck-clipboard-"+direction)
			if f == nil {
				if strings.TrimSpace(result) != "0" {
					return []report.Finding{report.Failf("workload %s helper interface probe failed: %s", direction, result)}
				}
				return nil
			}
			select {
			case <-ctx.Done():
				return []report.Finding{report.Failf("read workload interface capture: %s", f.Detail)}
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

func clipboardTextDelivered(ctx context.Context, e *Env) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, []string{fixtureClipboardWorkload, fixtureClipboard})
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	marker := clipboardMarker("text")
	if err := clip.put(clipboardTextFile, []byte(marker)); err != nil {
		return []report.Finding{report.Failf("seed text: %v", err)}
	}
	if f := probe(ctx, e, id, "host text", "text", textDigest(marker), textDigest(marker+"\n")); f != nil {
		return []report.Finding{*f}
	}
	return nil
}

// clipboardImageDelivered seeds two images in turn and then empties the
// clipboard, so a bridge that caches its first read, or keeps serving a
// removed image, fails alongside one that alters bytes.
func clipboardImageDelivered(ctx context.Context, e *Env) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, []string{fixtureClipboardWorkload, fixtureClipboard})
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	for _, label := range []string{"first", "second"} {
		data, digest, err := clipboardPNG()
		if err != nil {
			return []report.Finding{report.Failf("encode %s image: %v", label, err)}
		}
		if err := clip.put(clipboardImageFile, data); err != nil {
			return []report.Finding{report.Failf("seed %s image: %v", label, err)}
		}
		if f := probe(ctx, e, id, label+" image", "image", digest); f != nil {
			return []report.Finding{*f}
		}
	}
	if err := clip.clear(clipboardImageFile); err != nil {
		return []report.Finding{report.Failf("empty the clipboard: %v", err)}
	}
	if f := probe(ctx, e, id, "after emptying the clipboard", "empty"); f != nil {
		return []report.Finding{*f}
	}
	return nil
}

// clipboardSeedBoth fills the clipboard with an image and a text, so a
// write that should replace the whole clipboard shows what it left behind.
func clipboardSeedBoth(clip *testClipboard, text string) (string, *report.Finding) {
	data, digest, err := clipboardPNG()
	if err != nil {
		f := report.Failf("encode image: %v", err)
		return "", &f
	}
	if err := clip.put(clipboardImageFile, data); err != nil {
		f := report.Failf("seed image: %v", err)
		return "", &f
	}
	if err := clip.put(clipboardTextFile, []byte(text)); err != nil {
		f := report.Failf("seed text: %v", err)
		return "", &f
	}
	return digest, nil
}

func clipboardTextReplaced(ctx context.Context, e *Env) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, []string{fixtureClipboardWorkload, fixtureClipboard})
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	type write struct {
		argv       []string
		input, out string
	}
	var writes []write
	for _, argv := range clipboardWriters {
		marker := clipboardMarker(argv[0])
		writes = append(writes, write{argv: argv, input: marker, out: marker})
	}
	for _, flag := range []string{"-n", "--trim-newline"} {
		marker := clipboardMarker("wl-copy" + flag)
		// One trailing newline is removed, and only one.
		writes = append(writes, write{argv: []string{"wl-copy", flag}, input: marker + "\n\n", out: marker + "\n"})
	}
	var findings []report.Finding
	for _, w := range writes {
		helper := strings.Join(w.argv, " ")
		if _, f := clipboardSeedBoth(clip, "kit-tck-clipboard-before-"+rand.Text()); f != nil {
			return []report.Finding{*f}
		}
		if f := probe(ctx, e, id, helper, append([]string{"pipe", w.input}, w.argv...)...); f != nil {
			findings = append(findings, *f)
			continue
		}
		got, err := clip.text()
		if err != nil {
			return []report.Finding{report.Failf("read host text: %v", err)}
		}
		if got != w.out {
			findings = append(findings, report.Failf("%s reported success but the host clipboard holds %q, want %q", helper, got, w.out))
		}
		if image, err := clip.digest(clipboardImageFile); err != nil {
			return []report.Finding{report.Failf("read host image: %v", err)}
		} else if image != "" {
			findings = append(findings, report.Failf("%s left the earlier image on the host clipboard", helper))
		}
	}
	return findings
}

func clipboardTypedWrite(ctx context.Context, e *Env) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, []string{fixtureClipboardWorkload, fixtureClipboard})
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	out, f := execOutput(ctx, e, id, "kit-tck-clipboard", "png-digest")
	if f != nil {
		return []report.Finding{*f}
	}
	want := strings.TrimSpace(out)
	var findings []report.Finding
	for _, argv := range clipboardImageWriters {
		helper := strings.Join(argv, " ")
		if _, f := clipboardSeedBoth(clip, clipboardMarker("before")); f != nil {
			return []report.Finding{*f}
		}
		if f := probe(ctx, e, id, helper, append([]string{"copy-image"}, argv...)...); f != nil {
			findings = append(findings, *f)
			continue
		}
		got, err := clip.digest(clipboardImageFile)
		if err != nil {
			return []report.Finding{report.Failf("read host image: %v", err)}
		}
		if got != want {
			findings = append(findings, report.Failf("%s reported success but the host image has sha256 %q, want %q", helper, got, want))
		}
		if text, err := clip.digest(clipboardTextFile); err != nil {
			return []report.Finding{report.Failf("read host text: %v", err)}
		} else if text != "" {
			findings = append(findings, report.Failf("%s left the earlier text on the host clipboard", helper))
		}
	}
	return findings
}

// clipboardReadFiltered seeds text under a filter admitting images only,
// first alone and then beside an image. Alone, nothing the sandbox may
// read is on the clipboard, so every text spelling must write nothing at
// all; beside the image, every spelling naming text must, and the image
// still has to come through.
func clipboardReadFiltered(ctx context.Context, e *Env) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, []string{fixtureClipboardWorkload, fixtureClipboardFiltered})
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	marker := clipboardMarker("secret")
	if err := clip.put(clipboardTextFile, []byte(marker)); err != nil {
		return []report.Finding{report.Failf("seed text: %v", err)}
	}
	var findings []report.Finding
	if f := probe(ctx, e, id, "text outside the read filter", "no-text", marker); f != nil {
		findings = append(findings, *f)
	}
	data, digest, err := clipboardPNG()
	if err != nil {
		return []report.Finding{report.Failf("encode image: %v", err)}
	}
	if err := clip.put(clipboardImageFile, data); err != nil {
		return []report.Finding{report.Failf("seed image: %v", err)}
	}
	if f := probe(ctx, e, id, "text outside the read filter, beside an image", "no-text-beside-image", marker); f != nil {
		findings = append(findings, *f)
	}
	if f := probe(ctx, e, id, "an image the read filter admits", "image", digest); f != nil {
		findings = append(findings, *f)
	}
	return append(findings, clipboardTextUnchanged(clip, marker, "reading")...)
}

// clipboardWriteFiltered expects a PNG write refused under a filter
// admitting text only, and a text write still to land.
func clipboardWriteFiltered(ctx context.Context, e *Env) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, []string{fixtureClipboardWorkload, fixtureClipboardFiltered})
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	before := clipboardMarker("before")
	if err := clip.put(clipboardTextFile, []byte(before)); err != nil {
		return []report.Finding{report.Failf("seed text: %v", err)}
	}
	var findings []report.Finding
	for _, argv := range clipboardImageWriters {
		res, err := e.Adapter.Exec(ctx, id, append([]string{"kit-tck-clipboard", "copy-image"}, argv...)...)
		if err != nil {
			return []report.Finding{report.Failf("exec %s: %v", strings.Join(argv, " "), err)}
		}
		if res.ExitCode == 0 {
			findings = append(findings, report.Failf("%s accepted an image the write filter does not admit", strings.Join(argv, " ")))
		}
	}
	if image, err := clip.digest(clipboardImageFile); err != nil {
		return []report.Finding{report.Failf("read host image: %v", err)}
	} else if image != "" {
		findings = append(findings, report.Failf("an image the write filter does not admit reached the host clipboard"))
	}
	findings = append(findings, clipboardTextUnchanged(clip, before, "a refused image write")...)
	marker := clipboardMarker("admitted")
	if f := probe(ctx, e, id, "text the write filter admits", "pipe", marker, "wl-copy"); f != nil {
		return append(findings, *f)
	}
	return append(findings, clipboardTextUnchanged(clip, marker, "a text write the filter admits")...)
}

// clipboardWithout lists the compositions lacking a direction: the other
// direction alone, where one entry must not leak into the direction it
// turns off, and the workload alone, with no entry at all.
func clipboardWithout(other string) [][]string {
	return [][]string{{fixtureClipboardWorkload, other}, {fixtureClipboardWorkload}}
}

// clipboardReadWithheld binds sandboxes admitting nothing to read to a
// clipboard holding text and an image: whatever is on their PATH must
// deliver neither.
func clipboardReadWithheld(ctx context.Context, e *Env) []report.Finding {
	var findings []report.Finding
	for _, kits := range clipboardWithout(fixtureClipboardWrite) {
		findings = append(findings, clipboardReadWithheldFrom(ctx, e, kits)...)
	}
	return findings
}

func clipboardReadWithheldFrom(ctx context.Context, e *Env, kits []string) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, kits)
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	marker := clipboardMarker("secret")
	if _, f := clipboardSeedBoth(clip, marker); f != nil {
		return []report.Finding{*f}
	}
	// Nothing is readable, an image included, so every spelling is silent.
	composed := "composing " + strings.Join(kits, " + ") + " without read"
	var findings []report.Finding
	if f := probe(ctx, e, id, composed, "no-image"); f != nil {
		findings = append(findings, *f)
	}
	if f := probe(ctx, e, id, composed, "no-text", marker); f != nil {
		findings = append(findings, *f)
	}
	return findings
}

// clipboardWriteRefused writes text and an image through every helper of
// sandboxes admitting nothing to write; the host clipboard must survive
// all of them. Exit status is not judged: a helper the image never
// shipped fails to run at all, which is as conforming as a refusal.
func clipboardWriteRefused(ctx context.Context, e *Env) []report.Finding {
	var findings []report.Finding
	for _, kits := range clipboardWithout(fixtureClipboardRead) {
		findings = append(findings, clipboardWriteRefusedFrom(ctx, e, kits)...)
	}
	return findings
}

func clipboardWriteRefusedFrom(ctx context.Context, e *Env, kits []string) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, kits)
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	before := clipboardMarker("before")
	image, f := clipboardSeedBoth(clip, before)
	if f != nil {
		return []report.Finding{*f}
	}
	var attempts [][]string
	for _, argv := range clipboardWriters {
		attempts = append(attempts, append([]string{"kit-tck-clipboard", "pipe", clipboardMarker(argv[0])}, argv...))
	}
	for _, argv := range clipboardImageWriters {
		attempts = append(attempts, append([]string{"kit-tck-clipboard", "copy-image"}, argv...))
	}
	for _, argv := range attempts {
		if _, err := e.Adapter.Exec(ctx, id, argv...); err != nil {
			return []report.Finding{report.Failf("exec %s: %v", strings.Join(argv, " "), err)}
		}
	}
	composed := "composing " + strings.Join(kits, " + ") + " without write, writing"
	findings := clipboardTextUnchanged(clip, before, composed)
	if got, err := clip.digest(clipboardImageFile); err != nil {
		return append(findings, report.Failf("read host image: %v", err))
	} else if got != image {
		findings = append(findings, report.Failf("%s changed the host image", composed))
	}
	return findings
}

// clipboardFiltersUnion composes Kits whose filters differ and expects
// each one's grant in the one sandbox: read-only with write-only, and the
// filtered fixture, which admits no text in, with read-only, which admits
// every type in. A runtime refusing the second entry as a singleton
// conflict, or keeping one Kit's filter over another's, fails one half or
// the create itself.
func clipboardFiltersUnion(ctx context.Context, e *Env) []report.Finding {
	var findings []report.Finding
	for _, kits := range [][]string{
		{fixtureClipboardWorkload, fixtureClipboardRead, fixtureClipboardWrite},
		{fixtureClipboardWorkload, fixtureClipboardFiltered, fixtureClipboardRead},
	} {
		findings = append(findings, clipboardFiltersUnionOf(ctx, e, kits)...)
	}
	return findings
}

func clipboardFiltersUnionOf(ctx context.Context, e *Env, kits []string) []report.Finding {
	id, clip, cleanup, f := clipboardSandbox(ctx, e, kits)
	defer cleanup()
	if f != nil {
		return []report.Finding{*f}
	}
	composed := "composing " + strings.Join(kits, " + ")
	marker := clipboardMarker("union")
	if err := clip.put(clipboardTextFile, []byte(marker)); err != nil {
		return []report.Finding{report.Failf("seed text: %v", err)}
	}
	var findings []report.Finding
	if f := probe(ctx, e, id, composed+", reading text", "text", textDigest(marker), textDigest(marker+"\n")); f != nil {
		findings = append(findings, *f)
	}
	written := clipboardMarker("union-write")
	if f := probe(ctx, e, id, composed+", writing text", "pipe", written, "wl-copy"); f != nil {
		return append(findings, *f)
	}
	return append(findings, clipboardTextUnchanged(clip, written, composed+", a text write")...)
}

// clipboardTextUnchanged reports when the host text is not want: changed
// by what should have left it alone, or not landed by a write.
func clipboardTextUnchanged(clip *testClipboard, want, after string) []report.Finding {
	got, err := clip.text()
	if err != nil {
		return []report.Finding{report.Failf("read host text: %v", err)}
	}
	if got != want {
		return []report.Finding{report.Failf("after %s the host clipboard holds %q, want %q", after, got, want)}
	}
	return nil
}
