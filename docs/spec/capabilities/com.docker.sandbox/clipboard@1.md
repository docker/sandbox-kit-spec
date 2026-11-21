# `com.docker.sandbox/clipboard@1`

Share the runtime host's clipboard with the sandbox, so it behaves like
the clipboard the user already has: what they copy on the host can be
pasted inside, and what the workload copies inside can be pasted on the
host. Activating the capability is enough. An optional filter narrows
each direction to the media types the Kit needs. The host is the
machine running the runtime, not necessarily the CLI client machine. A
remote runtime does not silently substitute the client's clipboard.

- **Shape**: singleton.
- **Permission surface**: yes — each direction's admitted media types,
  without per-request confirmation, while the grant holds.

## Config

```yaml
capabilities:
  # A normal clipboard: every media type, both directions.
  - type: com.docker.sandbox/clipboard@1
```

```yaml
capabilities:
  # Paste screenshots in, copy text out, nothing else.
  - type: com.docker.sandbox/clipboard@1
    config:
      read: [image/*]
      write: [text/*]
```

| Field | Type | Default | Meaning |
|---|---|---|---|
| `read` | list of media types | every type | What the sandbox may read from the host clipboard |
| `write` | list of media types | every type | What the sandbox may put on the host clipboard |

An entry in a list is a media type (`image/png`), a whole top-level
type (`text/*`), or every type (`*/*`), in lowercase, without
parameters, and by its canonical name, never an alias such as
`image/x-png` (see [Media types](#media-types)). An empty list turns
that direction off. Use
`optional: true` when the workload runs without a clipboard.

- The entry **MUST NOT** turn both directions off, or state an entry <!-- tck: clipboard@1/config-valid -->
  outside that grammar, under an alias, or more than once. An entry granting nothing is
  refused rather than read as a request. A list entry may hold a kit
  reference, judged after expansion; a direction itself cannot.

A runtime can apply further measures of its own, such as user settings
that narrow a direction or ask before a read. Those never widen what
the filter admits, and a type a runtime withholds behaves as one the
filter does not admit.

## Media types

Clients name clipboard types in more than one way. A request is judged
by its canonical media type: lowercase, without parameters, with the
X11 names `UTF8_STRING`, `STRING`, `TEXT`, and `COMPOUND_TEXT` meaning
`text/plain`, and `image/x-png` meaning `image/png`. A conforming
runtime:

- **MUST** judge each request by its canonical media type, so an alias <!-- tck: clipboard@1/type-aliases -->
  admits exactly what the type it names admits. Otherwise a filter
  admitting `text/plain` refuses the same text requested as
  `UTF8_STRING`, and one refusing it leaks it under that name.

## Reading

For a direction its filter admits, a conforming runtime:

- **MUST** supply executable helpers as `wl-paste`, `xclip`, `xsel`, and <!-- tck: clipboard@1/read-helpers -->
  `pbpaste` on the workload's `PATH`, including in later exec sessions.
  The Kit need not install them. Agents pick a helper by the platform
  they believe they run on, so each name a terminal agent calls is
  present whatever the guest really is.
- **MUST** deliver host clipboard text through every helper, byte for <!-- tck: clipboard@1/text-delivered -->
  byte, as UTF-8: `wl-paste --no-newline`, `xclip -selection clipboard -o`,
  `xsel --clipboard --output`, and `pbpaste` print it, and
  `wl-paste --list-types` and `xclip -selection clipboard -t TARGETS -o`
  list `text/plain`. Plain `wl-paste` appends one newline, as it does
  on any host.
- **MUST** deliver a host clipboard image as PNG through `wl-paste` and <!-- tck: clipboard@1/image-delivered -->
  `xclip`: listing shows `image/png`, and `wl-paste --type image/png`
  and `xclip -selection clipboard -t image/png -o` write the image
  unmodified.
- **SHOULD** deliver every other type the host clipboard offers, <!-- tck: clipboard@1/other-types -->
  unmodified and under the name the host offers it, for clients asking
  by that name.
- **MUST** read the host clipboard when each request arrives. <!-- tck: clipboard@1/current-contents -->
  A runtime does not cache an earlier answer. When the host clipboard
  is empty, listing shows nothing and a read writes nothing to standard
  output.
- **SHOULD** serve the same contents to clients that read the clipboard <!-- tck: clipboard@1/native-clients -->
  through a native protocol rather than a helper, such as a Wayland
  data-control socket named by `WAYLAND_DISPLAY` under
  `XDG_RUNTIME_DIR`. Agents built on a clipboard library do not shell
  out, so the helpers alone do not reach them.
- **MUST** withhold every type the read filter does not admit: it is <!-- tck: clipboard@1/read-filtered -->
  not listed, and a read of it writes nothing of the host clipboard to
  the sandbox, as if the host clipboard did not hold it. The types the
  filter admits are delivered as before.
- **MUST** bound read size and request duration. Content over the <!-- tck: clipboard@1/read-bounded -->
  runtime's size limit reads as absent, never as truncated bytes. Exact
  limits are runtime policy.

The exit status of a read that finds nothing is not specified, because
agents judge a paste by its output.

## Writing

For a direction its filter admits, a conforming runtime:

- **MUST** supply executable helpers as `wl-copy`, `xclip`, `xsel`, <!-- tck: clipboard@1/write-helpers -->
  `pbcopy`, and `clip.exe` on the workload's `PATH`, including in later
  exec sessions, for the same reason as the read helpers.
- **MUST** replace the host clipboard with exactly the text a helper <!-- tck: clipboard@1/text-replaced -->
  reads from standard input to end of file, for `wl-copy`,
  `xclip -selection clipboard`, `xsel --clipboard --input`, `pbcopy`,
  and `clip.exe`. Line breaks and non-ASCII UTF-8 survive unchanged,
  and `wl-copy --trim-newline` (`-n`) removes one trailing newline. A
  write replaces the whole clipboard: what it held before, an image
  included, is no longer offered.
- **MUST** write other types when the helper names one: <!-- tck: clipboard@1/typed-write -->
  `wl-copy --type image/png` and
  `xclip -selection clipboard -t image/png` put the PNG they read on
  the host clipboard, unmodified, in place of what it held.
- **MUST** return zero from a helper only when the host clipboard was <!-- tck: clipboard@1/write-result -->
  replaced, and nonzero on denial, validation, transport, or host
  failure. A denied or failed write leaves the host clipboard as it was.
- **MUST** refuse a write of a type the write filter does not admit, <!-- tck: clipboard@1/write-filtered -->
  leaving the host clipboard unchanged. Writes of the types it admits
  succeed as before.
- **MUST** bound write size and request duration. A write over the <!-- tck: clipboard@1/write-bounded -->
  runtime's size limit fails and leaves the host clipboard unchanged,
  never truncated. Exact limits are runtime policy.

A helper's diagnostic on denial can say how the user enables the grant,
but never repeats the content.

## Availability and scope

A conforming runtime:

- **MUST** make the helpers available before install hooks and to <!-- tck: clipboard@1/guest-interface -->
  install hooks, startup hooks, and the workload.
- **MUST** refuse a required entry it cannot provide in full during <!-- tck: clipboard@1/required-unsatisfiable-refused -->
  capability preflight, by type name, before starting the workload. An
  entry is provided whole or not at all: a runtime that bridges only
  one direction treats an entry admitting anything in the other as
  unavailable. An optional unavailable entry is skipped and recorded. A
  runtime without access to a host clipboard does not advertise the
  type.
- **MUST NOT** serve the host clipboard to a sandbox whose effective <!-- tck: clipboard@1/read-withheld -->
  entry admits nothing to read, including when the sandbox has no entry
  at all and when helpers of the same names are present for another
  reason.
- **MUST NOT** change the host clipboard for a sandbox whose effective <!-- tck: clipboard@1/write-refused -->
  entry admits nothing to write, under the same conditions.
- **MUST** act on the clipboard of the host session the runtime acts for. <!-- tck: clipboard@1/host-session -->
  When it cannot identify that session, a read answers as an empty
  clipboard and a write fails; neither reaches another session's
  clipboard.
- **MUST** stop serving the clipboard when the grant is revoked or the <!-- tck: clipboard@1/revoked -->
  sandbox stops. The grant is consulted on every request, so revocation
  takes effect in a running sandbox; a withheld read answers as an
  empty clipboard and a withheld write fails. Revocation does not
  restore content already written.
- **MUST NOT** record clipboard contents in runtime diagnostics or audit <!-- tck: clipboard@1/contents-private -->
  records. Directions, type names, and byte counts are enough to triage
  a failure.

The runtime chooses the helper location and guest-to-host transport.
The capability supplies no primary selection and no clipboard history.

## Safety

The workload decides when to use the clipboard, not the user. While
the grant holds it can read whatever the user last copied at any
moment, not only when they paste: passwords, tokens, and private
messages included, and screenshots of anything on screen. And it can
replace what the user copied with content of its choosing, which they
paste believing it to be their own; pasted into a host terminal, that
text can run commands the workload chose.

Filter to what the workload uses. A Kit that only pastes screenshots
states `read: [image/*]` and `write: []`, which keeps host text out of
the sandbox and the host clipboard out of its reach. Only grant either
direction to trusted workloads.

## Composition

A workload, mixin, or enclosing set can request this capability.

- A conforming runtime **MUST** grant, in each direction, every type any <!-- tck: clipboard@1/filters-union -->
  composed Kit's filter admits. A Kit that pastes screenshots and a
  mixin that copies answers compose into a sandbox that does both,
  never into a conflict or into one Kit's filter alone.

A direction any requester leaves unfiltered admits every type in the
composed entry. A required declaration wins over an optional one; the
composed request is optional only when every requester can tolerate
its absence, and it is selected or refused as one entry.

## Gate

The permission surface lists, per direction, the media types the
filter admits: `*/*` for an unfiltered direction, nothing for one
turned off. A type not admitted before widens the grant; narrowing a
filter, such as from every type to `image/*`, does not. Optionality and
display metadata do not change that permission. There is no implied
network-policy or port grant.
