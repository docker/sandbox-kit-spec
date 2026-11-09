# `com.docker.sandbox/open-url@1`

Request HTTP or HTTPS navigation in the runtime host's default browser.
This is an explicit host interaction grant, not browser automation or
sandbox network access. The host is the machine running the runtime,
not necessarily the CLI client machine. A remote runtime does not
silently substitute the client's browser.

- **Shape**: singleton, **config-less**.
- **Permission surface**: yes — permission to request host browser
  navigation without per-URL confirmation.

## Execution plan

The default `order` is 200. The scheduled action is `setup:provide`.
Completion supplies the helper and its host request route before hooks.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/open-url@1
```

Use `optional: true` when printing a URL for the user to open is an
acceptable fallback.

- The entry **MUST NOT** carry `config`, including an empty or null <!-- tck: open-url@1/no-config -->
  value.

## Guest interface

A conforming runtime:

- **MUST** supply an executable helper as `xdg-open` on the workload's <!-- tck: open-url@1/guest-helper -->
  `PATH`, and set `BROWSER` to its absolute path, including in later
  exec sessions. The Kit need not install a helper. Both `xdg-open URL`
  and `"$BROWSER" URL` accept exactly one URL argument.
- **MUST** make this interface available before install hooks and to <!-- tck: open-url@1/guest-interface -->
  install hooks, startup hooks, and the workload. Hooks using `BROWSER`
  declare it in `env`, following [lifecycle@1](lifecycle@1.md)'s
  environment filtering; `xdg-open` remains available on `PATH`.
- **MUST** return zero from the helper only when the host opener accepts <!-- tck: open-url@1/opener-result -->
  the request, and nonzero on validation, transport, or opener failure.
  No shell interpretation of the URL occurs.
- **MUST** refuse a required entry it cannot provide during capability <!-- tck: open-url@1/required-unsatisfiable-refused -->
  preflight, by type name, before starting the workload. An optional
  unavailable entry is skipped and recorded. A runtime without access
  to a desktop browser does not advertise the type.

The runtime chooses the helper location and guest-to-host transport.
Acceptance does not mean that a tab opened or a page finished loading;
browser and desktop failures can still happen after acceptance.

## Scope and safety

A conforming runtime:

- **MUST** reject invalid requests through both guest helper entry <!-- tck: open-url@1/invalid-requests-rejected -->
  points using the URL and argument rules below.
- **MUST** validate requests on the host, accepting only absolute <!-- tck: open-url@1/url-validation -->
  HTTP/HTTPS URLs with a nonempty host. It rejects other schemes,
  filesystem paths, embedded credentials, whitespace, control
  characters, backslashes, malformed URLs, and requests with anything
  other than one URL argument. Guest-side validation alone is not a
  security boundary.
- **MUST NOT** interpret a URL as a shell command or let the sandbox <!-- tck: open-url@1/fixed-opener -->
  select the host executable or additional opener arguments. The host
  uses a fixed opener with the URL as one argument, protected from
  option interpretation.
- **MUST** bound request size, request duration, and concurrent pending <!-- tck: open-url@1/bounded-requests -->
  requests, and rate-limit browser launches. Exact limits are
  runtime policy; exceeding them fails rather than accumulating
  unbounded work or launching unbounded browser processes.
- **MUST NOT** log raw URLs in runtime diagnostics or audit records. <!-- tck: open-url@1/url-private -->
  Query parameters and fragments can contain authentication codes.
- **MUST** stop admitting requests when the grant is revoked or the <!-- tck: open-url@1/revoked -->
  sandbox stops. Revocation does not close tabs already opened.

This permission can navigate to malicious sites and use the browser's
cookies, authenticated sessions, and local-network access. Sandbox
network policy does not govern browser traffic. Validation applies to
the initial URL, not redirects, downloads, or external-app prompts the
browser subsequently handles. Only grant it to trusted workloads.

The capability supplies no browser control, DOM access, page contents,
cookies, clipboard access, or host file opening. Localhost URLs refer
to the host, not the sandbox. Use a separately published port for a
sandbox web server; OAuth callback listeners are not automatically
forwarded.

## Composition

A workload, mixin, or enclosing set can request this capability.
Identical declarations collapse. A required declaration wins over an
optional one; the composed request is optional only when every
requester can tolerate its absence.

## Gate

Presence is permission surface, represented as the type in the services
list because there is no config. Adding the capability widens the
grant. Optionality and display metadata do not change that permission.
There is no implied network-policy or port grant.
