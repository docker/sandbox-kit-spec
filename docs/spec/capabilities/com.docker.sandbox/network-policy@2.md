# `com.docker.sandbox/network-policy@2`

Phase-scoped egress policy whose entries may bound a host to particular
HTTP requests: what the sandbox may reach while the Kit's install hooks
run, what the agent may reach in steady state, and what it may do there.

- **Shape**: singleton — at most one entry per descriptor, and exclusive
  with [`network-policy@1`](network-policy@1.md).
- **Permission surface**: yes — the hosts and the requests they admit,
  direction-aware
  ([SPEC-v3 §7.4](../../SPEC-v3.md#74-permission-surface-and-the-gate)).

`@2` keeps `@1`'s phases and its allow/deny pair, and lets an entry say
more than a host. A `@1` config is a `@2` whose entries are all bare,
which is what it means: every method and path on the hosts it allows.

## Execution plan

The default `order` is 100. The scheduled actions are
`install:policy`, `start:policy`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
- type: com.docker.sandbox/network-policy@2
  config:
    install:                          # open only while install hooks run
      allow:
        - registry.npmjs.org
    runtime:                          # the agent's steady state
      allow:
        - github.com                  # bare: the connection, any protocol
        - hosts: [api.github.com]     # bounded: only these requests
          methods: [GET, HEAD]
          paths: [/repos/**]
      deny:
        - telemetry.example.com
        - hosts: [api.github.com]
          methods: [DELETE]
```

An entry is either a **host string** or an **object**. The string form is
shorthand for an object naming that one host and nothing else, so a Kit
that bounds nothing writes the list `@1` always wrote.

| Field | Type | Rules |
|---|---|---|
| `install` | object | optional. `allow`/`deny` entries for the install phase. |
| `runtime` | object | optional. `allow`/`deny` entries for the runtime phase. |
| `*.allow` | list\<entry\> | Entries to permit. |
| `*.deny` | list\<entry\> | Entries to refuse. Deny wins. |

### Entries

| Field | Type | Rules |
|---|---|---|
| `hosts` | list\<string\> | REQUIRED, non-empty. Domain patterns. Literal on a bounded allow entry. |
| `methods` | list\<string\> | optional. Uppercase HTTP method tokens, or the single entry `ANY`. Stating any of them bounds the entry. |
| `paths` | list\<string\> | optional. Path globs, each starting with `/`. Requires `methods`; omitted alongside them, it means every path. |

Host patterns are `@1`'s: exact host, `host:port`, `*.example.com`, and
`*` or `**` for everything. Port suffixes are ignored for allow-list
membership.

An entry stating neither `methods` nor `paths` is **unbounded**: it grants
the connection, for any protocol, exactly as `@1` did. An entry stating
either is **bounded**: it grants matching HTTP requests and nothing else,
so the same host carries no other traffic through that entry.

Stating `paths` requires stating `methods`. A path entry that did not say
which methods it bounds would read as a restriction while granting every
verb on that path, so `ANY` is spelled out where it is meant:

```yaml
- hosts: [api.example.com]
  methods: [ANY]
  paths: [/v1/**]
```

`ANY` is the only method when present — it already covers every method,
including extension methods no list names, so putting it beside `GET`
says nothing more and hides which was intended.

## Validation

- Strict config decode; unknown keys are errors.
- A descriptor declares one network-policy version. `@1` and `@2`
  together is an error: both describe the same grant, and merging them
  would mean guessing which bounds the other.
- Methods are canonical uppercase. `get` is an error rather than
  normalized in, so a published entry reads as the one enforcement
  matches.
- An empty `methods` or `paths` list is an error. Wildcard meaning
  belongs to an omitted field alone, or a generated document stating
  `methods: []` would silently grant every method.
- A bounded allow entry MUST name its hosts literally: no `*` in them. A <!-- tck: network-policy@2/bounded-allow-hosts-literal -->
  pattern cannot be bounded and unbounded at once — an entry bounding
  `*.example.com` to `GET` overlaps any entry naming a host inside it, and
  ranking the two needs the wildcard matcher, which is runtime-owned.
  Unbounded entries keep `@1`'s patterns, and so do deny entries however
  they are bounded: a deny wins outright, so an overlap between two of
  them decides the same way.
- Cross-entry invariant: every [`credential@1`](credential@1.md) inject
  domain MUST appear among the matching phase's allowed hosts. <!-- tck: network-policy@2/inject-domain-in-allow -->

## Runtime behavior

A conforming runtime implements every `@1` requirement, reading an
unbounded entry exactly as it reads an `@1` host. In addition:

- **MUST** refuse a request matching any bounded `deny` entry, whatever <!-- tck: network-policy@2/http-deny-precedence -->
  the allow entries say, and refuse every connection to a host a bare
  `deny` entry names. Deny wins as it does in `@1`.
- **MUST** treat a host granted only by bounded entries as deny-by-default: <!-- tck: network-policy@2/bounded-host-refused-outside-rules -->
  a request is refused unless some bounded entry admits it by both method
  and path, and non-HTTP traffic to that host is refused outright. The
  entry grants those requests, not the host.
- **MUST** apply an entry with no `methods` to every method, and one with <!-- tck: network-policy@2/omitted-methods-paths-are-every -->
  `methods` but no `paths` to every path.
- **MUST** fail closed for a bounded host whose traffic it cannot inspect. <!-- tck: network-policy@2/fail-closed-on-uninspectable -->
  A runtime that cannot see the method and path — an opaque tunnel it does
  not terminate — refuses rather than falling back to a connection-level
  verdict, which would grant everything the bound was written to withhold.
- **MUST** enforce at a boundary the sandbox cannot bypass, as in `@1`. <!-- tck: network-policy@2/unbypassable-boundary -->
- **MUST** answer a request these entries refuse with HTTP status `403`, <!-- tck: network-policy@2/refusal-is-403 -->
  rather than dropping the connection or letting the origin's own status
  stand. The refusal happens at the boundary and the origin never sees the
  request, so the status is the only thing distinguishing an enforced bound
  from the origin answering `404` or `405` on its own. A caller that cannot
  tell those apart cannot tell an enforcing runtime from one that ignored
  the entries.
- **SHOULD** surface a refused request's entry observably, so a missing <!-- tck: network-policy@2/refused-rule-observable -->
  method or path is diagnosable beyond the status.

## Composition

Across the resolved set, allow entries union per phase and deny entries
union per phase; deny precedence applies to the merged result.

Union widens, which is what it must do: a host one Kit grants unbounded
stays unbounded however narrowly another Kit bounds it, because no Kit
controls what its neighbours were granted. Narrowing what another Kit
reaches is `deny`'s job, and one Kit's deny is not defeated by another
Kit's allow.

A Kit on `@1` contributes bare entries, so mixing versions across Kits
composes without a conversion step.

## Gate

Both the hosts and the requests they admit are permission surface, and
the request projection carries one entry per method, host, and path so
the gate compares grants as a set. `ANY` and an omitted method list both
render as `ANY`, which is the wildcard they are.

A new allow entry widens; a **removed deny entry also widens**. Bounding a
host that was unbounded is a narrowing, and dropping the bound is the
widening — which the projection shows, because an unbounded entry renders
as every method on every path.

Newly allowed hosts report once, against the host list. Their breadth
follows from the grant the gate has just shown, so re-reporting it per
method would bury the case this category exists for: a host already
granted losing the entries that bounded it.

Dropping a bare deny reports once, against the host list, for the same
reason: an unbounded entry refuses every request as well as the
connection, so the loss is one loss however many ways it projects. A
bounded deny has no host-list counterpart and is reported on its own.

A bare `*` or `**` covers every host, so bounding one inside it adds
nothing and is not reported. Narrower globs are not expanded: whether
`*.example.com` covers `api.example.com` is the runtime's matcher to
decide, and the gate does not own it. Those read as widenings, which
over-prompts rather than granting silently.
