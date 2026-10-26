# `com.docker.sandbox/port@1`

One in-container port the runtime should publish to the host. Inbound
service exposure — distinct from the outbound
[network policy](network-policy@1.md).

- **Shape**: instance — one entry per (container port, transport);
  duplicates rejected.
- **Permission surface**: yes — `port/transport`.

## Config

```yaml
- type: com.docker.sandbox/port@1
  config:
    name: devserver        # optional informational label
    container: 3000        # REQUIRED, 1..65535
    transport: tcp         # optional: "tcp" (default) | "udp"
    family: ipv4           # optional: "ipv4" (default) | "ipv6" | "dual"
```

| Field | Type | Rules |
|---|---|---|
| `name` | string | optional. Label surfaced in port listings. |
| `container` | int | REQUIRED. 1–65535. |
| `transport` | string | optional. `tcp` (default when empty) or `udp`. |
| `family` | string | optional. The address families the service answers on inside the container: `ipv4` or `ipv6` for one, `dual` for both. `ipv4` when omitted, once the set is composed. |

`family` describes the service, not the host binding. A service bound
to `0.0.0.0` cannot answer a connection forwarded to it over IPv6, and a
host client that reaches a binding with nothing behind it sees the
connection accepted and then reset, which gives it nothing to fall back
from. Declaring the family lets the runtime choose bindings that reach
the service; how it does so — binding one host family, or forwarding
across families — stays the runtime's answer.

`dual` tells the runtime that delivering over either family reaches the
service, so binding both host families is safe. It permits that binding
rather than requiring it.

An omitted family is `ipv4`: the family a service bound to `0.0.0.0`
answers on, and the one whose missing host binding a client sees as a
refusal it can fall back from rather than a reset. The default applies
to the composed request, so an entry that omits `family` takes another
Kit's stated family for the same port instead of contradicting it.

## Runtime behavior

A conforming runtime:

- **MUST** publish the container port to the host when the entry is <!-- tck: port@1/published-when-granted -->
  granted.
- **MUST** allocate the host side itself — a Kit cannot pin a host port <!-- tck: port@1/host-side-allocated-by-host -->
  (two Kits requesting the same one would collide). Host binding SHOULD be <!-- tck: port@1/host-binding-default -->
  loopback with an ephemeral port; users pin host ports through the
  runtime's own UX, not the descriptor.
- **MUST**, unless the composed `family` is `dual`, deliver all traffic <!-- tck: port@1/family-honored -->
  it accepts for the port to the service over that family — IPv4 when
  `family` is omitted — or refuse the entry.
- **SHOULD** surface `name` wherever published ports are listed. <!-- tck: port@1/name-surfaced -->

## Composition

Entries union across the set, deduplicated on (container, transport). Two
Kits publishing the same port is a single publication, not a conflict.
Their `family` describes that one service: a stated family holds over
an omitted one, and two different families — `dual` included — are a
composition error. A family still omitted once the set is composed is
`ipv4`.

## Gate

`port/transport` is permission surface. A new port widens. `family` is
not surface: it describes the service, so changing it grants nothing.
