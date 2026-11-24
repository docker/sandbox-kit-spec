# `com.docker.sandbox/agent-skills@1`

A directory this Kit's agent scans for skills. It receives selected
Kit-bundled skills and, when available and enabled, the host's shared
skills store. Declare it on the Kit that supplies the agent, whether it
is a workload or a mixin.

- **Shape**: instance — one entry per path.
- **Permission surface**: `readonly` permits host sharing at the path,
  and `readwrite` separately permits write access. `off` grants no host
  access. Bundled content itself grants no host access.

## Config

```yaml
- type: com.docker.sandbox/agent-skills@1
  config:
    path: /home/agent/.claude/skills   # REQUIRED, absolute
    mode: off                        # optional: "off" (default) | "readonly" | "readwrite"
```

| Field | Type | Rules |
|---|---|---|
| `path` | string | REQUIRED. Absolute, canonical in-container path where the agent reads skills: no `.` or `..` segments, no trailing slash, not `/` itself. An alias such as `/x/../skills` for a declared `/skills` would evade the duplicate check, so canonical form is validated rather than normalized in. |
| `mode` | string | optional. `off` (default), `readonly`, or `readwrite`. The most access the Kit is willing to take to the host store, not a demand. Does not constrain bundled content. |

Two entries naming one path are rejected: identical ones as a duplicate
request, differing ones as a contradiction about the same mount.

## Access

Host sharing is supplemental. A missing or empty store, or a host setting
of off, leaves the discovery destination available for bundled skills.
A Kit mode of `off`, including an omitted mode, also withholds host
content without disabling the discovery destination.
The entry does not require the user to have host skills, even when it is
required. Required/optional selection still governs runtime support for
the capability; it does not make host content a startup prerequisite.

Both sides bound the result, and neither can exceed the other. The host
decides how much access it is prepared to give, and the Kit declares how
much it is prepared to take:

| Host setting | Kit `mode` | Effective |
|---|---|---|
| off | anything | no host mount; bundled skills remain available |
| anything | `off` (or unset) | no host mount; bundled skills remain available |
| readonly | `readonly` | read-only |
| readonly | `readwrite` | read-only — the host withholds write |
| readwrite | `readonly` | read-only — the Kit never asked for write |
| readwrite | `readwrite` | read-write |

The Kit opts into host sharing by declaring `readonly` or `readwrite`.
An omitted mode grants no access to the host store. An agent that only
reads host skills declares `readonly`, so a permissive host cannot grant
it write access.

## Why the Kit declares the path

A runtime cannot know where an arbitrary agent reads skills. It can know
for the agents it ships, and a runtime **MAY** keep such a mapping for
them, but a Kit that runs an agent behind a wrapper — or one the runtime
has never heard of — reads from a path no host-side table predicts. The
declaration is what lets the store reach those Kits at all.

It also composes. A sandbox built from a shell workload plus two agent
mixins has two skills paths, one per mixin, which a single sandbox-wide
agent identity cannot express.

## Runtime behavior

A conforming runtime:

- **MUST** use every selected `path` as a destination for selected <!-- tck: agent-skills@1/destination -->
  [agent-skill@1](agent-skill@1.md) requests, independently of host sharing.
  This is where the runtime links or otherwise exposes each bundle at
  `<path>/<effective-name>`. Every selected directory receives every
  selected bundled skill. A destination with no skills requires no
  filesystem change.
- **MUST** mount the shared skills store at `path` when the store exists <!-- tck: agent-skills@1/store-mounted-at-declared-path -->
  and is nonempty and neither the host's skills setting nor the Kit's
  effective `mode` is `off`.
- **MUST** mount it before lifecycle hooks run, so an install hook can <!-- tck: agent-skills@1/mounted-before-hooks -->
  read what the user shared.
- **MUST** resolve access as the narrower of the host's setting and the <!-- tck: agent-skills@1/access-narrower-of-both -->
  Kit's `mode`, and **MUST NOT** exceed either. A host that withholds the
  mount withholds it; a Kit that asks for `off` receives no host content,
  and a Kit that asks for `readonly` gets read-only however permissive the
  host is. Neither case disables bundled skills.
- **MUST NOT** refuse or skip an entry merely because the host store is <!-- tck: agent-skills@1/host-store-optional -->
  missing, empty, or disabled by either side, even when the entry is
  required. These conditions withhold host content, not the discovery
  destination.
- **MUST** default an omitted `mode` to `off`. Declaring a discovery <!-- tck: agent-skills@1/off-default-honored -->
  destination does not opt the Kit into host sharing. Selected bundled
  skills remain available at that destination.
- **MUST NOT** treat the store's contents as trusted input to the runtime <!-- tck: agent-skills@1/store-untrusted -->
  itself. Like [agent-context](agent-context@1.md), this is material for
  the agent, not instructions for the host.

What the store contains, where it lives on the host, and how a user fills
it are runtime concerns outside this specification. The runtime chooses
how to combine host-shared and bundled content while honoring their
access and conflict rules. Exposing bundled content does not grant
permission to modify the host store.

## Composition

Paths union across the set, and every selected path receives the same
bundled skills. Host content appears only where the effective mode and
host policy permit it. Required wins over optional.
Two Kits naming one path ask for the same content in the same place,
which is satisfied once. Matching [volume@1](volume@1.md) requests also
merge; differing volume storage configurations conflict.

When two Kits name one path with different modes, the composition resolves
to the **widest declared mode**, ordered `off`, `readonly`, `readwrite`,
still bounded by the host. A single mount cannot be read-only and writable
at once, and the narrower declaration is not an isolation boundary that
widening would breach: a sandbox is one filesystem and one process tree,
so a Kit that declared `readonly` was
never protected from a mount another Kit legitimately obtained. `mode`
states what one Kit asks for; the union is what the composition asks for,
which is exactly what the gate shows — the merged request surfaces the
write grant, so raising a path to `readwrite` by composing is a widening
the user approves, never a silent escalation. Within a **single** Kit the
same situation is a contradiction and is rejected by validation.

## Gate

With `readonly` or `readwrite`, the path is permission surface, under
its own `skills` category rather than `storage`. The two grant different
things — a volume is space the sandbox is given, while this hands a Kit a
host directory the user populated — and a gate naming both the same would
not say which was gained.

An `off` destination grants no host access and adds no skills permission.
Enabling `readonly` on it widens, as does adding a new enabled path or
raising an existing path from `readonly` to `readwrite`. Reading the
user's shared skills and being able to rewrite them for every later
sandbox are different grants. Write is the larger grant and includes
read, so a writable request surfaces as both entries and giving write up
is not a widening.
