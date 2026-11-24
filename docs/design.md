# Why Kits, and the tenets behind them

This page is the argument for Kits and the principles that decided the
design. For a worked tour of a real Kit, see [kit-intro.md](kit-intro.md);
the normative rules are in [SPEC-v3.md](spec/SPEC-v3.md).

## From Dockerfile to Kit

A Dockerfile describes the software itself: how it is built, what gets
packaged, and how it starts (entrypoint, command, user, environment). A
decade of tooling grew up around that, because the same image builds,
pulls, and runs the same way on every machine that supports OCI images.

Agents don't fit that model. A container isolates an application, which
runs, does its job, and touches only what it was handed. An agent is an
actor: it decides what to do next and then does it to your filesystem,
your network, your databases, and your cloud account, using authority
you granted on purpose.

The agent needs that authority. One that cannot install a dependency,
reach an API, or hold a credential cannot do its job. But each grant
gives up some of the isolation you were counting on, so the more useful
an agent gets, the less its container isolates it.

So the boundary has to move out from the container to the
**containment**: the machine, the filesystem, the network, the
credentials, everything the agent can reach or change. A sandbox gives
the agent its own kernel, so that part of the boundary is enforced by
the machine instead of agreed to by the model. An empty sandbox still
isn't a working environment, though. Something has to say which harness
runs, which tools and MCP servers it gets, which skills and instructions
it loads, and exactly what it may touch.

A Dockerfile doesn't answer any of that. It specifies the inside of the
image completely and the outside not at all, so the rest has lived in
`docker run` flags, a Compose file, a CI config, an onboarding doc, and
whatever the person who set it up still remembers. None of it is in the
artifact, and none of it is versioned or reviewed. A Kit puts it there:
the content and the authority it asks for, in one image, under one
digest.

That makes authority **diffable**. When the next version of an agent
asks for another credential or another network destination, that is a
change in authority. It shows up in the diff, it can be held for
approval, and it travels with the Kit wherever the Kit runs. It is also
why Kits are a specification and not a product feature. A Kit has to
keep working when you run it somewhere else, or it is lock-in.

**Dockerfiles made software reproducible. Kits make authority reproducible.**

## Tenets

Most rules in the specification apply one of these principles to a
specific case.

**Ride the ecosystem, don't extend it.** A Kit adds no media type,
artifact type, or sidecar file, so every registry, scanner, signer, and
mirror already handles one correctly. A new artifact format is cheap to
write; the cost is the decade of tooling that does not understand it.

**One artifact, one digest.** Declarations live in the manifest of the
image they describe, so a Kit cannot be half-updated: pinning the digest
pins the policy, the content, and the metadata together. A separate file
describing an image is a second source of truth, and second sources
drift.

**Declare only what images cannot already express.** The descriptor has
no top-level identity name, no image reference, and no entrypoint or
env. A Kit's identity is the reference you consume it by, and
capability-entry names are display labels. The runtime contract is the
image config. Restating identity or runtime config would give two
answers to one question, and one of them would go stale.

**One model for every ask.** Resource grants and engine-executed
behaviors are the same kind of request: a typed, versioned entry in
`capabilities`. A host reviews one list to decide what a Kit may do, and
a Kit has one way to ask. Whether a host supports something is a single
question, not a patchwork of unrelated fields.

**Fail closed, and fail early.** Unknown descriptor fields are errors,
an unsatisfied requirement stops resolution, and a required capability
the host cannot grant refuses the launch. A permission dropped silently
looks the same as one never requested, and the failure would show up
later as wrong behavior instead of as an error.

**Composition is a function, not a sequence.** A resolved Kit set is
ordered by its dependency graph, not by the order the arguments were
typed, so the same set always composes to the same image. That is what
lets a composition be locked, reproduced, and cached.

**The grammar declares; runtimes behave.** The descriptor says what is
wanted and never how a host provides it. That is why the capability
pages, and not the grammar, are normative for runtime behavior. A host
that cannot implement a capability refuses it instead of approximating
it.

**Let types evolve on their own clock.** The `@1` in a capability type
versions its contract: the config schema and observable behavior,
including composition rules and permitted transitions against persistent
state. A capability can change without a descriptor grammar bump, and
hosts can support types the grammar has never heard of.
