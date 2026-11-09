# `com.docker.sandbox/kit-registry@1`

Access to the runtime's Kit registry — the runtime-hosted registry endpoint
over its own image store, where Kit builds push results and pull the Kit
frontend. Declaring this capability is the only thing that makes the
registry reachable: the typed request literally is the enforcement.

- **Shape**: singleton, **config-less** — any `config` value is a
  validation error.
- **Permission surface**: yes — as a service entry (the bare type).

## Execution plan

The default `order` is 200. The scheduled actions are
`setup:provide`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
- type: com.docker.sandbox/kit-registry@1
  description: Pushes built kits into the runtime's image store
```

No `config`. Reaching the registry is the host's answer — routing, address,
transport — never an address the Kit guesses.

## Runtime behavior

A conforming runtime:

- **MUST NOT** route the sandbox to the registry unless the capability is <!-- tck: kit-registry@1/no-route-unless-requested -->
  declared and granted. Absent declaration, the route does not exist; a
  connection attempt fails at the boundary, not with a permission error
  from the registry.
- **MUST** announce the endpoint to the sandbox itself (an environment <!-- tck: kit-registry@1/endpoint-announced -->
  variable, a runtime-managed alias hostname) rather than requiring the
  Kit to know one. Kit content reads the announced address and MUST NOT <!-- tck: kit-registry@1/content-must-not-assume-address -->
  hardcode any.
- **MUST** scope the grant to the registry endpoint: it is image-store <!-- tck: kit-registry@1/grant-scoped-to-endpoint -->
  access, not general egress, and it does not widen the
  [network policy](network-policy@1.md).
- **SHOULD** enforce the registry's own namespace/tag discipline <!-- tck: kit-registry@1/namespace-discipline -->
  server-side; the capability grants reachability, not arbitrary writes.
- **MUST** scope what the registry serves to Kit content — the Kit <!-- tck: kit-registry@1/serves-kit-content-only -->
  namespace and the frontend — rather than the runtime's whole image
  store. Reads need this as much as writes do: a store-wide read path
  turns any name a sandbox guesses into a served image.
- **MAY** restrict which Kit images are allowed to hold the capability,
  keyed on the published repository the Kit was consumed by. Any Kit can
  write this request into its own descriptor, so declaring it is not
  standing to hold it; a runtime that refuses simply routes nothing, the
  same position as a Kit that never asked.

## Composition

One declaration anywhere in the set routes the sandbox; the grant is
per-sandbox.

## Gate

Surfaces as a service entry (`com.docker.sandbox/kit-registry@1`). Absent →
present widens: image-store access is a grant, not a side effect of
network egress.
