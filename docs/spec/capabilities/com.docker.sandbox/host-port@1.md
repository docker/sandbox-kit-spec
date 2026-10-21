# `com.docker.sandbox/host-port@1`

One host IPv4 loopback TCP or UDP service made available at an IPv4 loopback
endpoint inside the sandbox. This reverses [port@1](port@1.md), which
publishes a sandbox service to the host. It grants access to a specific
host service, not general network egress.

The host is the machine running the sandbox, not necessarily the client
machine. A remote runtime cannot substitute the client's loopback. A
runtime unable to provide this contract does not advertise the type;
normal required/optional selection rules apply.

- **Shape**: instance — one entry per (effective container port, transport);
  duplicates within a descriptor rejected.
- **Permission surface**: yes — host port, effective container port and
  transport, separately from published ports and network policy.

## Config

```yaml
- type: com.docker.sandbox/host-port@1
  config:
    host: 5432
    container: 15432
    transport: tcp
```

The sandbox connects to `127.0.0.1:15432` to reach the service at
`127.0.0.1:5432` on the host.

| Field | Type | Rules |
|---|---|---|
| `host` | int | REQUIRED. 1–65535. Host IPv4 loopback port. |
| `container` | int | optional. 1–65535; omitted or zero defaults to `host`. Sandbox IPv4 loopback port. |
| `transport` | string | optional. `tcp` (default when empty) or `udp`. |

A descriptor **MUST** reject invalid ports, unsupported transports and <!-- tck: host-port@1/config-valid -->
unknown config fields. There is no address, hostname, wildcard, range or
phase selector. Services listening only on `::1` are not reachable.

## Runtime behavior

A conforming runtime:

- **MUST** forward traffic from the configured sandbox endpoint to <!-- tck: host-port@1/forwarded -->
  the granted host endpoint throughout workload execution, without
  requiring a network-policy grant.
- **MUST** make the mapping available before install hooks and retain it <!-- tck: host-port@1/before-hooks -->
  through startup hooks and workload execution.
- **MUST** restrict both endpoints to `127.0.0.1`. The sandbox cannot <!-- tck: host-port@1/loopback-only -->
  select a different host address or port through the relay, and the
  relay does not listen on a sandbox non-loopback interface.
- **MUST NOT** expose a host service without a grant. <!-- tck: host-port@1/no-extra-access -->
- **MUST NOT** grant other host services or general network egress as a <!-- tck: host-port@1/no-egress-widening -->
  side effect.
- **MUST** forward to the host on demand. An absent host service does <!-- tck: host-port@1/on-demand -->
  not prevent admission; failed TCP connections or unanswered UDP
  datagrams do not redirect to another destination, and a service started
  later becomes reachable.
- **MUST** withdraw the mapping when the sandbox stops. <!-- tck: host-port@1/withdrawn -->

For UDP, forwarding preserves datagram boundaries and contents in both
directions and returns replies to the originating sandbox socket.

No NIC, NAT mechanism or relay protocol is prescribed. The mapping lasts
for the sandbox's running lifetime. Clients bypass HTTP proxy settings
for this endpoint, for example `curl --noproxy '*' http://127.0.0.1:15432/`.
Applications cannot bind an endpoint already occupied by the relay.

## Composition

Mappings union across a set. A runtime **MUST** deduplicate identical <!-- tck: host-port@1/composition -->
mappings, including equivalent explicit defaults, and support one host
service exposed at multiple sandbox ports and distinct host services at
distinct sandbox ports.

Different host ports claiming the same (effective container port, transport) conflict. <!-- tck: host-port@1/conflict -->
A runtime **MUST** reject the composition rather than choose by declaration
order.

TCP and UDP mappings can use the same numeric container port.

A runtime **MUST** reject collisions with runtime-owned listeners. <!-- tck: host-port@1/runtime-listener-conflict -->

## Gate

The permission surface **MUST** distinguish host-service mappings from <!-- tck: host-port@1/permission-surface -->
published ports, network egress and other service grants. Each entry is
`host->container/transport`, with defaults applied. Adding a mapping or
changing either port or the transport widens the grant.

This is raw TCP or UDP access, not an authenticated or filtered application
proxy. A database, daemon or API can expose sensitive host data and
operations. Network policy does not inspect or constrain traffic through
this explicitly granted relay. Grant only services the sandbox is
trusted to use.
