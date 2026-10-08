# Conformance

This document specifies how a runtime demonstrates that it implements
[SPEC-v3](SPEC-v3.md) and the [capability pages](capabilities/com.docker.sandbox/).

The key words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are to be
interpreted as in RFC 2119.

Conformance has two halves, and they are independent. A Kit is judged
against the artifact rules; a runtime is judged against the behavior its
capability pages require. A runtime that consumes Kits it did not build is
still responsible only for the second.

## 1. Kit conformance

An artifact conforms when it satisfies [§9](SPEC-v3.md#9-publishing) and
[§10](SPEC-v3.md#10-the-oci-layout). `kit-tck validate <reference>` judges a
published artifact; the reference implementation's BuildKit frontend runs
the same checks before it exports, so a Kit built by it cannot be
published malformed.

Running both matters. Build-time checks see the descriptor, the recipe,
and the filesystem about to be exported, and can point at the authored
line that is wrong. Only a published artifact shows what the exporter and
the registry did with it — and an artifact from a different producer has
no build to check.

## 2. Runtime conformance

A runtime demonstrates conformance by supplying an **adapter**: an
executable implementing the verbs below. The suite drives the adapter and
asserts observable behavior, so a runtime conforms by what it does, not by
how it is written. An adapter **MAY** be a shell script.

```sh
kit-tck runtime --adapter ./my-runtime-adapter
```

### 2.1 Invocation

The suite invokes the adapter as `<adapter> <verb> [arguments…]`. An
adapter **MUST** write diagnostics to stderr so a failure is attributable,
and anything a verb is specified to return to stdout.

Exit status carries meaning beyond success and failure:

| Exit | Means |
|---|---|
| `0` | The verb succeeded |
| `2` | The runtime **refused** the request, deliberately and by policy |
| any other non-zero | The verb failed |

The distinction is load-bearing. Several requirements are satisfied only
by refusing something — a Kit requiring a capability the runtime does not
implement, most importantly — and a suite that accepted any non-zero exit
as a refusal would pass an adapter that fails at everything, including one
that cannot find its fixtures. An adapter **MUST** exit `2` when it
declines a request it understood, and **MUST NOT** exit `2` for an error.

An adapter **MUST NOT** require interactive input.

### 2.2 Verbs

| Verb | Arguments | stdout | Purpose |
|---|---|---|---|
| `capabilities` | — | one capability type per line | What the runtime claims to implement |
| `create` | `<kit-ref>…`, zero or more `--arg name=value` and `--env name=value`, `--name <alias>`, `--skills-host-mode readonly\|off`, `--skills-host-store missing\|empty`, `--ssh-agent <socket>`, `--ssh-known-hosts <file>`, and `--git-identity-config <absolute-path>\|off` each at most once | one sandbox id | Compose the Kit set and start it |
| `exec` | `<id> -- <argv>…` | the command's stdout | Run a command inside |
| `stop` | `<id>` | — | Stop without discarding state |
| `start` | `<id>` | — | Start a stopped sandbox |
| `recreate` | `<id>`, optionally a replacement `<kit-ref>…` and zero or more `--arg name=value` | — | Replace the container within the same instance, preserving block-volume storage |
| `rm` | `<id>` | — | End the instance and delete all its volume storage |
| `volume-paths` | `<id>` | JSON array of cleaned destination paths | Observe allocated block storage, including unmounted destinations |
| `wait-idle` | `<id>` | — | Disconnect the final client session and wait beyond the normal auto-stop grace period |
| `status` | `<id>` | `running` or `stopped` | Observe sandbox state without starting it or attaching a session |
| `host-mounts` | `<kit-ref>` | JSON array of `{id, path, hostPath}` | List retained host directories for the resolved Kit identity |
| `host-mount-read` | `<mount-id> <relative-path>` | the file's bytes | Observe sandbox writes from the host |
| `host-mount-rm` | `<mount-id>` | — | Remove a runtime-owned directory by its opaque listing handle |

`create --env name=value` supplies a container environment override.
Adapters **MUST** apply it after image defaults and Kit argument exports,
before expanding `${{ kit.env.NAME }}` references. An empty value is a
present override; values are literal and are not shell-expanded. These
flags do not change the adapter process's own environment. The
`SPEC-v3 §6/env-expanded` check compares a declared file's content with
its final environment value: an image default without any argument
exports, a distinct argument export, a distinct runtime override, and
an empty override.

`capabilities` is what makes a partial implementation testable: the suite
skips the types a runtime does not claim, and asserts that a Kit
**requiring** an unclaimed type is refused rather than silently
under-provisioned.

`exec` **MUST** proxy the command's exit status as its own, and **MUST
NOT** allocate a TTY. The argv after `--` is passed through unmodified.
It **MUST** run the command as the sandbox's agent identity: several
requirements are about who the sandbox does things as, and an `exec` of
the runtime's choosing would answer for a user the agent never is.

`stop` followed by `start` **MUST** preserve the sandbox's writable layer
and block-volume contents; tmpfs contents are discarded.
This pair is what separates the lifecycle phases: `install` hooks run once
at create, `startup` hooks on every boot.

`recreate` **MUST** replace the container — discarding the writable layer —
while preserving the instance's block-volume storage, including paths
no longer selected. With replacement references, the adapter **MUST**
recreate the same instance with that composition; without them it reuses
the previous references. Argument overrides replace values by name;
other create-time inputs, including host bindings, remain unchanged.
The adapter **MUST** keep the suite-facing id usable after recreation,
even if the runtime changes its container handle. Deliberate refusal
returns `2` and leaves the previous composition and storage intact.
Because stop/start preserves the writable layer, it cannot distinguish
a block volume from an ordinary directory;
recreate is the observation that can, and the suite verifies the layer was
really discarded before crediting anything to the volume.

An adapter claiming `volume@1` **MUST** support `create --name`, changed
composition recreation, and `volume-paths`. The suite uses a fresh alias
to distinguish recreation from removal followed by creation under the
same name. `volume-paths` **MUST** report actual runtime-managed block
storage, including retained unmounted paths and allocations orphaned by
instance removal. The suite-facing observation handle **MUST** remain
usable after instance metadata is removed: an absent instance record
alone does not establish that its backing allocations were deleted.
An empty array means no associated block allocation remains. The verb
**MUST NOT** provision storage or merely echo a descriptor or
adapter-maintained request list. The adapter **MUST NOT**
simulate persistence by copying data around a runtime remove/create.
An adapter without an operation that preserves instance identity and
volume data on recreation cannot claim `volume@1`.

Volume data probes run as the agent. Mount-root accessibility is a SHOULD,
so contents observations report SKIP when the initial root's permissions
prevent seeding or reading test data. Only the fixture probe's explicit
permission-denied status permits that skip; missing roots and failed
probes remain failures. Errors after successfully seeding retained block
storage are not skipped, because they can violate state preservation.
Tmpfs discarded on stop or recreation receives a fresh root; initial
accessibility observations for that replacement root can also skip.

`wait-idle` and `status` are required only for adapters claiming
`com.docker.sandbox/long-running@1`. `wait-idle` **MUST** exercise a real
client session's connection and disconnection, leave no client sessions
attached, and return only after the runtime's ordinary session auto-stop
grace period has elapsed, with a margin for scheduling. The adapter knows
that period; a fixed suite delay cannot bound every runtime's policy.
A runtime with no session-based auto-stop needs no grace-period wait.

While waiting, the adapter **MUST NOT** use exec, attach, keepalives, or
other operations that reset the idle timer or restart the sandbox. It
**MUST NOT** disable auto-stop or force detached mode to make the check
pass: the runtime under test decides from the descriptor whether to keep
the workload running. Session types with different disconnect paths
**MUST** each be exercised before `wait-idle` returns, with a full idle
interval after each final disconnection.

`status` **MUST** read host-side state without starting, resuming, or
attaching to the sandbox. It reports `stopped` only when the sandbox has
finished stopping; a missing sandbox or a failed observation is an error,
not a stopped state. The suite reads status before probing the background
process, so an exec that implicitly starts a stopped sandbox cannot hide
auto-stop, and reads it again after explicit stop.

### Host-shared directory observation

The `host-mount-*` verbs are required only for adapters claiming
`com.docker.sandbox/host-mount@1`. They use the runtime's ordinary user
interfaces for discovering, reading, and removing host directories.
Adapters **MUST NOT** manufacture storage outside the runtime's
provisioning path to satisfy these checks.

`host-mounts` resolves the Kit reference to the same identity used by
`create`, and returns its retained directories even with no live sandbox.
Each record **MUST** contain a nonempty opaque `id`, its canonical
in-container `path`, and the host location `hostPath` a user can find.
There is one record per path; an empty listing is `[]`, not `null`.
Listing is an observation and **MUST NOT** allocate storage.

The `host-mount-version-v1` and `host-mount-version-v2` fixtures exercise
updates within one published Kit identity. Adapters claiming
`host-mount@1` **MUST** publish or import them as distinct versions of one
suite-owned repository, resolving their supplied fixture references to
those versions for `create` and `host-mounts`. Distinct tags or digests
identify the versions; neither version may be substituted with the
other. The suite writes through the first version, removes its sandbox,
and reads through the second, then verifies writes from the second are
visible when reopening the first. Testing two unrelated local Kit
identities cannot establish this repository identity guarantee.

`host-mount-read` **MUST** read from the host directory independently of
sandbox exec. The suite passes only a relative fixture filename; an
absent file returns a nonzero status. `host-mount-rm` **MUST** remove the
listed directory and its contents so a later create starts empty.

The suite uses fresh in-container paths for each check. It removes only
directories at those paths for the fixture Kits, after removing their
sandboxes; adapters **MUST NOT** interpret cleanup as a request to remove
other Kit directories or user content.

### Git identity binding

An adapter claiming `com.docker.sandbox/git-identity@1` **MUST** accept
`create --git-identity-config <absolute-path>|off`. The suite-owned file
is a transport for a test binding, not a requirement that the runtime
store or discover identity in files. It carries a known name/email pair
in Git config syntax, alongside unrelated settings used as leak probes.

The adapter **MUST** arrange for the runtime to provide that binding for
this create using its normal identity-provisioning interface. It can
translate the test input into runtime settings, an API request or another
binding mechanism. Incomplete values are unavailable, not filled from
other sources; `off` withholds the identity. The option does not grant
the capability: only a Kit requesting it receives the pair.

The adapter **MUST NOT** write a sandbox gitconfig itself to bypass the
runtime's materialization path, or alter the user's real settings. The
suite changes its input between create and restart/recreate; the adapter
**MUST** reflect that change in the runtime binding before those verbs,
so the suite can check that existing sandboxes retain their selection.
Sandbox edits **MUST** remain observable if they incorrectly write back
to that binding: an adapter translating the input into another store
reflects such writes in the suite-owned file before returning from exec.
Otherwise the adapter **MUST NOT** delete or rewrite the suite's input.

The fixture workload ships Git and `kit-tck-git-identity`, with distinct
image identity defaults and an unrelated sandbox alias. Its probe checks
global values, repository-local precedence through a real commit,
preservation of sandbox settings, exclusion of unrelated source settings,
and hook-time captures at creation, stop/start and recreation. Repeated
hook checks clear the startup capture first so an old observation cannot
hide a missing hook. Required and optional fixtures exercise missing
values and withheld identity. The suite never needs the user's real name,
email or signing keys.

After recreation, the hook check reads only the new startup capture:
install hooks do not repeat, and their writable-layer output is discarded.
Workload entrypoint captures are also checked after restart and recreation.
Identity and source-setting leak probes inspect effective Git behavior,
including environment and system configuration, not only global files.

The separate `git-identity@1/source-private` requirement is waived by the
suite: the adapter does not identify every guest path or backend through
which a runtime could expose its identity source. Effective Git probes
cannot detect an unconfigured readable copy at an arbitrary path. The
runtime prohibition still applies; passing these probes does not certify
source confidentiality.

An adapter claiming this capability also supports `selection <id>` using
the record format below. The suite checks that an unavailable optional
identity is recorded as skipped, with its rejected member, and is absent
from selected records.

### 2.3 Known values the suite arranges

Several requirements are about what must **not** appear, and absence
cannot be judged against an unknown value. The suite therefore places
three known values in the adapter's environment before it judges
anything:

| Variable | Meaning for the adapter |
|---|---|
| `KIT_TCK_HOST_SENTINEL` | A host-side value. A runtime that leaks its own environment into a lifecycle hook leaks this with it, which is how "a hook sees only its declared env" is judged. The adapter does nothing with it beyond letting it be inherited. The suite also decorates baseline variables in the adapter's environment — `TERM`, `HOSTNAME`, `OLDPWD`, `SHLVL`, and `_` carry the sentinel, and `PATH` gains a sentinel component — so a runtime copying a host baseline value into a hook is caught by the same scan; baseline values must derive from the image and sandbox. `HOME` and `PWD` are both: they point through a sentinel-named symlink to the real home, functional for credential helpers and relative paths while still betraying host provenance when copied. |
| `KIT_TCK_BOUND_SECRET` | The secret the adapter **MUST** bind for the fixture credential service `kit-tck`. The container **MUST NOT** see this value; a proxy-managed credential with a declared name presents a sentinel in that variable, and an inject-only credential (no name) presents nothing at all. |
| `KIT_TCK_SKILL_NAME` | A skill the adapter **MUST** place in the host's shared skills store for each `create` without `--skills-host-store`, when it claims `com.docker.sandbox/agent-skills@1`. The capability permits no mount when the store is empty or skills are off, so without a known entry the suite cannot tell a mounted store from an empty directory. Before the create rather than before the claim: `capabilities` observes nothing and writes nothing, and a query that seeded a user's store would leave an entry behind on every run that asked what a runtime implements. |

An adapter claiming `com.docker.sandbox/agent-skills@1` **MUST** default
skills to their **most permissive** setting. Access is the narrower of the
host's setting and the Kit's, so a restrictive default makes the Kit's
half unobservable: a read-only mount would prove nothing about whether the
runtime honored a Kit asking for read-only, or merely never offered write
to anyone.

The host's half is judged separately: when `create` carries
`--skills-host-mode`, the adapter **MUST** arrange that host setting for
that sandbox — `readonly` withholds write however much a Kit asked for,
and `off` withholds only the host store. Required and optional
`agent-skills@1` destinations remain selected, and the sandbox starts
without the host mount. Selected `agent-skill@1` bundles still appear at
those paths. Without this input the suite could never observe host-side
narrowing at all. The store the suite uses is its own; a
runtime **SHOULD NOT** point these fixtures at a store a user depends on.

Store availability is independent of sharing policy. For a `create` with
`--skills-host-store missing`, the adapter **MUST** arrange a nonexistent
host store directory; `empty` **MUST** arrange an existing directory with
no entries. These overrides suppress the marker seeding for that create,
not host sharing: the suite leaves sharing enabled to exercise the
runtime's missing/empty-store handling separately from its off setting.
The adapter **MUST** use isolated test state and preserve existing host
content, keep the isolated store until that sandbox is removed, and
restore the ordinary seeded store for subsequent creates without the
flag. Both required and optional discovery destinations remain selected,
and selected bundled skills remain available in either scenario.

The workload fixture declares its working directory as
`/home/agent/workspace`, and the suite reads the agent-context profile
beside it, at `/home/agent/AGENTS.md`. A runtime is free to place
workspaces wherever it likes for its own workloads; for THIS workload, the
declared workdir is the workspace, so the profile's place beside it is a
path the suite can name. The `context-workload` fixture instead declares
an explicit profile directory; the suite passes its `directory` argument
to test both `/home/agent/.codex/AGENTS.md` and
`/home/agent/.kit-tck/context/AGENTS.md`. The adapter passes that argument
through rather than assuming every profile is beside the workspace.
The fixture seeds existing instructions at both destinations; the runtime
preserves them while adding its guidance and Kit index. The
`context-profile` mixin declares an explicit `CLAUDE.md` destination
against the legacy workload, testing directory and filename precedence in
both input orders. The `context-conflict`
mixin declares a differing explicit profile, which composition refuses.

An adapter that cannot bind credentials **SHOULD NOT** claim
`com.docker.sandbox/credential@1`, in which case its checks are skipped.

When `create` carries `--ssh-agent <socket>`, the adapter **MUST** make
the SSH agent listening on that Unix socket the backing agent available
to that sandbox, and only to that sandbox. Without it, no backing agent is
available: a Kit requiring `com.docker.sandbox/ssh-agent@1` is then
refused, and an optional entry is skipped. How the adapter hands the
socket to its runtime is its own business — as the agent a client
forwards, or as the one a managed runtime would supply — so long as the
sandbox relays to this agent and no other. The agent is the suite's,
holding a key generated for the check, so the suite can inspect what
reached it; an adapter **MUST NOT** substitute an agent of its own, or
the user's. An adapter whose runtime cannot be pointed at a given agent
**SHOULD NOT** claim the type.

When `create` carries `--ssh-known-hosts <file>`, a file in OpenSSH's
`known_hosts` format, the adapter **MUST** make those the host keys the
runtime matches `authenticate` destinations against for that sandbox, and
trust no other keys for the names it lists. The suite generates them for a
test server under the reserved name `kit-tck.example`: nothing listens
there, and none is needed, because a session binding is a host key's
signature the suite can make itself. The page requires those keys to come
from outside the sandbox, which is where this file is.

The workload fixture's `kit-tck-ssh-agent` probe speaks the agent
protocol itself and needs `python3` in the image, which the fixture's base
provides.

### 2.4 What the suite guarantees

The suite **MUST** `rm` every sandbox it creates, including after a
failure. It **MUST NOT** assume any state carries between test cases, and
addresses sandboxes only by the ids `create` returned.

### 2.5 Kit references

The suite builds its fixture Kits through the runtime under test, because
a runtime that consumes Kits necessarily has a way to obtain them. A
`create` argument is therefore whatever reference form the runtime accepts
— a local directory, an image reference — and an adapter **MAY** pass it
through unchanged.

## 3. Reporting conformance

A runtime claiming conformance **SHOULD** state which capability types it
implements and publish the suite's output. A runtime implementing a subset
is conforming for the types it claims, provided it refuses what it cannot
provide: silently ignoring a required capability is the one failure the
model cannot tolerate, because the Kit's author declared it precisely
because the Kit does not work without it.

## Capability-group selection controls

Capability groups are descriptor grammar, not an independently claimed
capability. Group checks run when the runtime claims their member types.
The adapter provides these controls without prescribing host approval UI:

- `create ... --reject-capability <type>` arranges a false selection
  answer for that type; repeat the flag for multiple types. Other
  satisfiable claimed types are accepted for the fixture.
- `selection <id>` returns JSON with `selection` (`selected` and
  `skipped` records) and `surface` (the actual effective `spec.Surface`).
  Records carry `path`, `source` (`kit` and original `path`), `members`,
  aligned `memberSources` (each member's original `kit` and `path`), and
  `rejected` member paths. `members` and `rejected` locate entries in the
  consumed descriptor; `memberSources` retains original locations through
  set publication. Names alone are not identities. Paths use
  the descriptor's zero-based `capabilities[i].group.capabilities[j]`
  vocabulary. The adapter translates retained runtime records; it MUST
  NOT recompute selection to answer the observation.
- `selection-policy <id> --reject-capability <type>` changes the
  selection answer for the next recreation of this sandbox, without
  revoking its current grants. Stop/start MUST retain the original
  decision; `recreate` makes a fresh selection using the new answer.

The `groups` fixture distinguishes an optional volume-plus-lifecycle
feature from an independent group with the same label. The checks observe
files, hook ordering, skip attribution, and the effective storage grant.
`groups-required` checks refusal diagnostics; `groups-conflict` checks
that optional groups cannot be discarded to repair a selected conflict;
`groups-failure` checks that an accepted hook's exit is an execution
error, not a skip or preflight refusal.

The conflict checks observe refusal and its source diagnostics. A refused
`create` returns no sandbox ID, and the adapter exposes no effect trace
for a failed create. The suite therefore cannot inspect whether files or
hooks were applied before refusal. The before-application duty remains
required by the specification, with an explicit TCK coverage waiver until
the adapter can expose those effects.

Atomic-selection checks observe final state, so they cannot detect effects
applied during selection and rolled back before observation. The separate
`selection-before-application` duty has an explicit coverage waiver until
an adapter effect trace can judge it. `groups-expanded` checks validation
after create-time argument and environment expansion, including when
policy would reject the optional member. Lifetime checks exercise both initial acceptance and
initial rejection: restart retains the decision and recreation uses the
new policy. Calling `selection-policy <id>` without rejection flags clears
future rejections, allowing the inverse transition.
