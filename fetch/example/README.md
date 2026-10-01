# Assemble Kits through the Go API

[main.go](main.go) is a complete runnable consumer. It reads OCI
references and per-Kit create arguments from stdin, authenticates using
Docker's credential store, selects capabilities, composes the image and
declarations, and checks file collisions across the Kits' layers.

From the repository root, substitute references you can read. The tool
Kit below is assumed to declare a create-phase `team` argument; use the
argument names declared by your own Kits.

```sh
go run ./fetch/example <<'JSON'
[
  {"Reference": "docker.io/your-org/workload:1.0.0"},
  {
    "Reference": "docker.io/your-org/tool:1.0.0",
    "Args": {"team": "alpha"}
  }
]
JSON
```

The preview accepts library-known types by default. Use
`-supported-types=type1,type2` to supply a runtime's actual supported
types. Add `-allow-volumes=false` to simulate a host rejecting persistent
storage: an optional cache group then contributes neither its volume nor
its lifecycle configuration file. A required rejected entry or group
fails resolution. These flags control the preview; it does not inspect
the host or apply capabilities.

## One-call API

```go
result, err := fetch.Assemble(ctx, requests, fetch.Options{
    LayerValidator: fetch.DefaultLayerValidator,
    CapabilitySelector: spec.Supported(claimedTypes...),
    Overrides: fetch.Overrides{
        Env: map[string]string{"WORKSPACE_DIR": "/workspace"},
    },
    OnProgress: func(p fetch.Progress) {
        // Render p.Stage, p.State, p.Reference, and p.Layer in the UI.
    },
})
if err != nil {
    return err
}
manifest, err := result.Image.Manifest()
if err != nil {
    return err
}
```

`fetch.Options{}` loads registry metadata and accepts all capability
types the library knows, without reading or validating layers. That
default is not a claim that a runtime implements every type: supply its
actual supported types or a policy callback. A callback has signature
`func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision`.
It receives the operation context and the owning Kit's expanded
descriptor, including `DisplayName` and all declarations before
selection. Descriptor and capability inputs are passed by value. Use the
context for cancellable policy or approval calls. Return
`spec.CapabilityDecision{Accepted: true}` to accept, or
`spec.CapabilityDecision{Message: "reason"}` to reject. The zero value
rejects. Selection records retain each member's `Accepted` and `Message`
in `Decisions`, in the same order as `Members`. Messages also appear in
required-rejection errors. Selection must not apply effects. The library
validates even skipped declarations, selects groups atomically, and
validates the selected composition. Conflicts fail; optional groups are
not dropped to repair them.

Supply the complete dependency set, containing exactly one workload.
`Assemble` does not discover missing dependencies or publish an image.
It loads verified metadata, resolves arguments and capability decisions,
composes image defaults, and invokes `LayerValidator` when supplied.
`fetch.DefaultLayerValidator` reads layer inventories and rejects files
contributed by multiple Kits. Collision checks resolve each Kit's layers
with the shared overlay filesystem model before comparing them in image
order: cleaned paths, symlink aliases, whiteouts, opaque directories,
and file/directory replacements are included. Directories may overlap.
Files deleted within a Kit no longer claim paths, but its surviving
deletion effects cannot erase another Kit's content. A layer shared by
multiple inputs with the same expected diff ID is read once, but each
Kit retains its file ownership for the collision check. Skipping
capabilities removes neither layers nor argument environment exports.

Leave `Options.LayerValidator` nil for metadata-only assembly before
consent, including with a custom loader, or when the caller has already
checked the image. It preserves metadata and descriptor validation,
argument and `kit.env` expansion, atomic group selection, and the same
`Result`. It never calls `LayerLoader` and emits no `inventory` or
`collisions` progress events; `LayerLoader` may also be nil. The caller
remains responsible for layer integrity, safe extraction, resource
limits, and cross-Kit file collision checks before using the image.

`fetch.DefaultLayerValidator` limits assembly to 4,096 layer occurrences, 250,000
archive entries, 250,000 path components, and 32 MiB of combined path
and link-name bytes across all Kits. Components in both entry names and
link targets count before path cleaning, limiting implied directory
creation as well as retained strings. Repeated entries and cached layer
replays count toward every allowance; empty archives still consume the
layer allowance. Assembly fails when a limit is exceeded and closes any
open layer stream. Inventories retain compact extraction metadata rather
than full tar headers. These
limits prevent additional layers or Kits from multiplying the inventory
allowance.

The program prints the result plus its computed manifest:

- `Resolved`: the selected `Descriptor`, dependency-ordered per-Kit
  `Kits` with their `Env` exports, published descriptors and decisions
  in `Selections`, argument `ContainerEnv` exports, and validation
  `Warnings`.
- `Image`: typed OCI `Config` and ordered `Layers`. The workload's
  layers come first, followed by mixins in dependency order.
- `Environment`: the complete container environment, combining image
  defaults, argument exports, and `Overrides.Env`, in that precedence.
- `WorkingDir`: the workload image's working directory, or the explicit
  absolute `Overrides.WorkingDir` when supplied.
- `Manifest`: a snapshot computed from the image defaults and layers.

Environment and working-directory overrides do not modify the reusable
image. Apply the returned container settings at creation. Empty
variable values remain empty; missing override keys retain their values.
Capability configuration strings can use `${{ kit.env.HOME }}` and other
`${{ kit.env.NAME }}` references. Assembly composes the image, applies
argument exports and runtime overrides to the environment, then expands
configurations before validation and capability selection. Values stay
strings; missing names fail and explicitly empty values remain empty.
No shell expansion or host environment lookup occurs. `$HOME`, `${HOME}`,
and `~/` are unchanged. Environment references do not expand mapping keys,
capability metadata, or the environment values themselves.

For example, a mixin can declare a lifecycle file with
`path: "${{ kit.env.HOME }}/.config/tool/settings.json"` without hard-coding
the user's home. All group members expand and validate before selection;
optional declarations do not hide missing variables or malformed results.

Persist `Resolved.Descriptor` and `Resolved.Selections` with the sandbox
and reuse the decision on restart. A recreation selects afresh. Apply
hooks from `Resolved.Descriptor`; use
`spec.AgentContextsOf(kit.Descriptor.Capabilities)` for each selected
Kit's guidance bodies. Do not execute original, unselected declarations.
Use `resolve.Resolve(result.Resolved.Kits)` with the existing lock/gate
APIs to judge permissions before applying the result.

## Loading and progress

`Options.Loader` accepts a `fetch.KitLoader`. Its `LoadedKit` contains
verified digest identity, manifest, config, and a lazy `LayerLoader`
function. `Descriptor` optionally carries the original annotation from
an index; otherwise the platform manifest's annotation is used. A loader
must resolve metadata consistently to the returned digest and select a
platform. Assembly validates the metadata and declarations. When selected,
`fetch.DefaultLayerValidator` streams, verifies, and closes layer blobs without
buffering their bodies.

For source-form Kits, such as `./my-kit` or a git URL, a custom loader
builds the source and sets `LoadedKit.Image` to its runtime-resolvable
image reference. Assembly keeps the request's `Reference` as the Kit's
identity in diagnostics, selection records, and locks; `Image` identifies
the built content. The loader keeps that image reference consistent with
the returned digest and metadata. When `Image` is empty, assembly derives
a digest-pinned image from the request reference, which must then be an
OCI image reference. The default registry loader supplies its pinned
image reference directly. Lower-level callers can also set `Kit.Image`
before calling `fetch.Units`.

The default loader uses Docker credentials and Linux on the caller's
architecture. Customize registry behavior with the existing client:

```go
client, err := fetch.New(
    fetch.WithDockerCredentials(),
    fetch.WithPlatform(platform),
)
if err != nil {
    return err
}
result, err := fetch.Assemble(ctx, requests, fetch.Options{
    LayerValidator: fetch.DefaultLayerValidator,
    Loader: client.LoadKit,
})
```

For anonymous registries use `fetch.New()`. For a local content store,
supply a loader that opens blobs from that store. `LayerLoader` returns
fresh streams in the manifest's original compression and honors the
provided context. `fetch.DefaultLayerValidator` accepts tar, gzip, and zstd. It
verifies both the manifest's stored-blob digest and the config's
uncompressed diff ID,
including archive padding and compression trailers. Every opened stream
is closed on success or failure; read and close errors fail the operation.
An archive entry the shared extractor model refuses fails assembly.

`OnProgress` receives serialized stage transitions on the calling
goroutine: `started`, `completed`, or `failed`. The stages are `load`,
`compose` (image defaults), `resolve` (argument and environment expansion,
selection, and descriptor validation), `inventory`, and `collisions`.
Kit references and layer digests identify individual work. Inventory events include cached
layer replays, which consume the operation budget without reopening
blobs. Callbacks should return promptly; use the context to cancel. The
returned error remains authoritative, and events never contain
configuration values or file contents.

A custom `LayerValidator` receives the complete selected Kit set, a map
from consumption references to `LoadedKit` metadata, and the progress
callback. It runs once after composition and resolution so it can check
cross-Kit effects or consult the runtime's verified store. It treats the
inputs as read-only, honors cancellation, and returns errors to fail
assembly. Progress callbacks, when supplied, run synchronously.

## Migrating existing callers

`KitSelection.Original` is now `PublishedDescriptor` and retains the
published argument declarations and unexpanded references.
`KitSelection.Raw` is now `PublishedBytes`. Use `Resolved.Kits` for
expanded, selected per-Kit declarations.

`LoadedKit.OpenLayer` is now `LoadedKit.LayerLoader`, with the same stream
contract. Rename the field in custom loaders. Layer validation is now
opt-in: callers that previously used `Options{}` or supplied only a loader
add `LayerValidator: fetch.DefaultLayerValidator` to retain the previous checks.
Leaving it nil deliberately skips them. These are Go API changes; the
Kit descriptor grammar is unchanged.

## Storage and lower-level APIs

`result.Image.Manifest()` computes the config digest and size internally.
After changing the image config, call it again for a fresh snapshot.
`Image.WriteMetadata(ctx, destination)` accepts an ORAS `content.Pusher`
and writes config and manifest from one serialization snapshot. The
caller supplies layer transfer, naming/import, and container creation;
the destination must have access to all referenced layers.

For callers orchestrating those steps themselves, `Client.Resolve` and
`ResolvePartial` resolve only declarations. `Client.LoadImage` and
`assemble.Assemble` load and compose image metadata without downloading
layers. Pass `fetch.WithEnvironment(imageDefaults, runtimeOverrides)` to
`Resolve` or `ResolvePartial` after composing image defaults. Argument
exports replace defaults, and runtime overrides win last. Both maps are
copied when the option is constructed. Without this option, only argument
exports are available to `${{ kit.env.NAME }}` references; the resolver
never reads the host environment or image configs. `Resolved.ContainerEnv`
still reports only argument exports, so apply the same precedence when
creating the container.

Each `Resolved.Kits` entry's `Env` map retains that Kit's resolved
create-argument exports, including defaults and explicitly empty values.
It excludes image defaults and runtime overrides and is nil when no
arguments export variables. Use it when adding a Kit to an existing
sandbox so only that Kit's exports are applied. `ContainerEnv` remains
the conflict-checked union across Kits, including exports shared by
multiple Kits with the same value.

`KitSelection.PublishedDescriptor` retains the published descriptor,
including its argument declarations and unexpanded references.
`KitSelection.PublishedBytes` holds its exact source bytes. Apply the
expanded, selected declarations from `Resolved.Kits` or
`Resolved.Descriptor`.

With `LayerValidator: fetch.DefaultLayerValidator`, the one-call API also reads
and checks layer inventories. With a nil validator it reads metadata only.

Descriptor errors include original locations and source excerpts where
safe. Errors after environment expansion omit expanded values and source
excerpts that could disclose them; print the returned error directly.
Publishing a flattened Kit remains `spec.Merge`'s job, with staged
context bodies owned by the publisher. Runtime composition uses
`spec.Compose` and preserves each selected context source separately.
