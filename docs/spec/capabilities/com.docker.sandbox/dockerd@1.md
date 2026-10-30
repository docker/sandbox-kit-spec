# `com.docker.sandbox/dockerd@1`

A request for a Docker Engine inside the sandbox: elevation, a private
persistent store for the engine, and by default the engine started for
the Kit. The host may refuse.

- **Shape**: singleton.
- **Permission surface**: yes. It counts as
  [`privileged@1`](privileged@1.md) plus storage at `/var/lib/docker`:
  the engine needs the same elevation, and its store is persistent
  storage, as a [`volume@1`](volume@1.md) there would be.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/dockerd@1
    description: Builds and runs containers with the in-sandbox engine
    config:
      autoStart: false   # optional; default true
```

| Field | Type | Meaning |
|---|---|---|
| `autoStart` | boolean | optional, default `true`. Whether the runtime starts the engine. `false` leaves starting it to the Kit's own hook. |

No other field is defined; config decodes strictly.

Any kind may declare it: a workload, a mixin that brings container
tooling, or a set.

The capability asks for an engine. It does not install one: the
engine binaries come from the composed image, and the runtime starts
the `dockerd` the composed image config's `PATH` resolves. A Kit that needs the packaged
engine keeps `requires: ["deb/docker-ce"]` beside this entry, so
resolution refuses a workload that does not carry it.

Some runtimes still start an engine for an image labeled
`com.docker.sandboxes.start-docker`. That label is a transition
mechanism from before this capability, and where a runtime honors it,
it is how that image asks for an engine (see the exemption below). It
is not part of this specification's grammar; a Kit states `dockerd@1`
instead. The exemption is temporary: it goes away once runtimes stop
honoring the label.

The engine **answers** when `GET /_ping` on `/var/run/docker.sock`
returns HTTP 200. Every duty below that says the engine answers means
exactly that.

## Runtime behavior

A conforming runtime, whatever `autoStart` says:

- **MUST** run the sandbox with its platform's elevated-privilege mode <!-- tck: dockerd@1/elevation-granted -->
  when granted, as for `privileged@1`.
- **MUST** back `/var/lib/docker` with storage private to the sandbox <!-- tck: dockerd@1/storage-private-and-persistent -->
  that survives stop and start and recreation of the same sandbox
  (recreation as [`volume@1`](volume@1.md) defines it, including after
  Kit updates), so images, containers, and build cache outlive a reboot
  and an update, and no other sandbox reads or writes them.
- **MUST** delete that storage when the sandbox is removed, as for <!-- tck: dockerd@1/storage-removed-with-sandbox -->
  `volume@1` storage; removing the sandbox is how its build cache is
  reclaimed.
- **MUST NOT** start an engine for a composition that was not granted <!-- tck: dockerd@1/absent-without-grant -->
  `dockerd@1` with `autoStart` true, even when `dockerd` is on `PATH`.
  An optional declaration the runtime refused or skipped is not a
  grant.
  During the transition, a runtime MAY treat the
  `com.docker.sandboxes.start-docker` label on the workload image as
  such a request.
- **MAY** refuse. Refusal fails resolution for a required entry, as for
  a type the host does not provide
  ([§7.3](../../SPEC-v3.md#73-unknown-types)); an optional entry is
  skipped and recorded, and the Kit runs without an engine.
- **SHOULD** require explicit, non-default consent to grant, for the <!-- tck: dockerd@1/explicit-consent -->
  reason `privileged@1` gives.

With `autoStart` true or omitted, the runtime:

- **MUST** start the engine so that it answers before any `startup` <!-- tck: dockerd@1/engine-before-startup -->
  hook runs, on every boot, so a hook can use it without waiting.
- **MUST** fail the start when the engine does not answer within a <!-- tck: dockerd@1/fails-when-engine-unanswered -->
  bound the runtime chooses, rather than run hooks and the workload
  against an engine that is not there.
- **MUST** name that bound in the failure. <!-- tck: dockerd@1/unanswered-bound-named -->
- **MUST** make `/var/run/docker.sock` usable by the user the image <!-- tck: dockerd@1/socket-usable-by-image-user -->
  config declares.
- **MUST** leave the engine reading the composed image's <!-- tck: dockerd@1/daemon-json-honored -->
  `/etc/docker/daemon.json`, and pass `dockerd` no flag for a directive
  that file can also set: `dockerd` refuses to start when one directive
  comes from both a flag and the file.
  That rules out `--group` too, so a runtime makes the socket usable by
  the image user by changing its group or mode once the engine answers,
  or relies on the image's `daemon.json` setting `group`.

No engine is promised during `install` hooks: the duty above covers
`startup` hooks only. This page orders no `lifecycle@1` `files` write
relative to the engine start, so a file the engine reads when it
starts, such as `daemon.json`, belongs in the image rather than in
`files`.

With `autoStart: false`, the Kit starts the engine from its own hook
(a [`lifecycle@1`](lifecycle@1.md) `startup` hook with `user: "0"`, for
example). That hook owns what the runtime does in the default mode:
waiting until the engine answers and making the socket usable by the
image user. A later hook that uses the engine waits for `GET /_ping` to
return 200 itself, because nothing orders it after the engine is ready.
The runtime:

- **MUST** still run the sandbox elevated, as when the engine is <!-- tck: dockerd@1/manual-elevation-granted -->
  started for the Kit.
- **MUST** still back `/var/lib/docker` with the private, persistent <!-- tck: dockerd@1/manual-storage-private-and-persistent -->
  storage described above.
- **MUST NOT** start the engine itself, on any boot. <!-- tck: dockerd@1/manual-engine-not-started -->

The merged entry decides, not each declaration (see Composition): when
another Kit in the composition leaves `autoStart` true, the runtime
starts the engine.

- A Kit that declares `autoStart: false` **MUST** therefore tolerate an <!-- tck: dockerd@1/manual-tolerates-running-engine -->
  engine that is already running: its start hook checks first, for
  example with `GET /_ping` on the socket, and does not start a second
  daemon. Its own daemon flags may not apply to the engine it finds.

## Composition

One Kit requesting it gives the whole sandbox an engine. Several Kits
may declare it: the merged entry starts the engine (`autoStart` true)
when any declaration says true or omits it, and leaves it to the Kits
only when every declaration says `false`. A Kit that starts the engine
itself still finds a running one, while a Kit relying on the runtime
would find none if `false` won. Identical declarations collapse; a
required declaration wins over an optional one.

`dockerd@1` owns `/var/lib/docker`. A descriptor or composition that <!-- tck: dockerd@1/engine-store-owned -->
declares it, optional or not, **MUST NOT** also declare a `volume@1` or
`host-mount@1` at that path or under it, compared on the cleaned path
the storage key uses ([§7.1](../../SPEC-v3.md#71-arity)) one path
component at a time: `/var/lib/docker/` and `/var/lib/docker/volumes`
are refused, `/var/lib/docker2` and `/var/lib/docker-cache` are
ordinary storage. Validation refuses the pair within one descriptor's
top-level entries, and composition refuses it across Kits. When either
entry sits in a group, or the storage path references an arg, the pair
is refused no later than create, once the args are expanded and
selection has settled the groups, as `volume@1` judges its own
conflicts. An arg in `autoStart` defers nothing here: it decides who
starts the engine, not who owns the store. Validation accepts an arg
reference in `autoStart`, but a merge that has to read the value
refuses it until the arg is pinned in `kits[].args`.

## Gate

The privileged bit, shared with `privileged@1`, plus `/var/lib/docker` <!-- tck: dockerd@1/widening-gates -->
on the storage surface. Absent to present is a widening that MUST stop
for approval. A Kit already granted `privileged@1` and a `volume@1` at
`/var/lib/docker` that moves to `dockerd@1` widens nothing; one granted
`privileged@1` alone widens storage, because the engine's store is
persistent storage it did not have. `autoStart` grants nothing: it
decides who starts the engine, not what the sandbox may do, so changing
it in either direction widens nothing.
