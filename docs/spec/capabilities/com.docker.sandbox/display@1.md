# `com.docker.sandbox/display@1`

Present windows from Wayland applications in the sandbox on the runtime
host's local graphical session. The workload connects to a Wayland
socket at `/run/display/wayland-0` inside the sandbox.

- **Shape**: config-less singleton.
- **Permission surface**: the sandbox can draw windows on the user's
  desktop, including windows that could impersonate trusted host UI.

## Declaration

```yaml
capabilities:
  - type: com.docker.sandbox/display@1
```

The entry **MUST NOT** carry `config`, including `{}` or `null`. <!-- tck: display@1/no-config -->

A workload, mixin, or enclosing set can request display. Selected
requests compose into one grant for the sandbox. A required request
takes precedence over an optional one. Use `optional: true` for an
application that can also run headless.

## Runtime behavior

- The runtime **MUST** obtain explicit authorization for the individual <!-- tck: display@1/explicit-consent -->
  sandbox before granting access to the host's graphical session.
  The Kit declares the request. The host chooses how the user or
  operator authorizes it.
- When it grants the request, the runtime **MUST** provide a usable <!-- tck: display@1/socket-usable -->
  Wayland socket at `/run/display/wayland-0` to the workload user.
  A client can connect there and discover the `wl_compositor` global.
- The runtime **MUST** let a Wayland application using that socket <!-- tck: display@1/host-presentation -->
  present its windows in the authorized local host session.
- The runtime **MUST** withhold the Wayland endpoint it supplies at <!-- tck: display@1/no-grant -->
  `/run/display/wayland-0` unless the sandbox has an accepted display
  request or an independent explicit display grant.
- The display grant alone **MUST NOT** allow clipboard or drag-and-drop <!-- tck: display@1/clipboard-isolation -->
  payloads to pass between the sandbox and the host in either direction.
  [clipboard@1](clipboard@1.md) governs a separate clipboard grant.
- If the host session is unavailable or access is denied, the runtime <!-- tck: display@1/required-unsatisfiable-refused -->
  **MUST** refuse a required request by type name.
- This refusal **MUST** occur during capability preflight, before the <!-- tck: display@1/required-preflight -->
  workload starts.
- The runtime **MUST** skip and record an unavailable optional request. <!-- tck: display@1/optional-unavailable-skipped -->

A Kit can direct an application to the socket by setting
`WAYLAND_DISPLAY=/run/display/wayland-0` for that process.

## Gate

The permission surface records the type in `services`. Adding display <!-- tck: display@1/widening-gates -->
to a recorded grant **MUST** stop for approval under
[the permission gate](../../SPEC-v3.md#74-permission-surface-and-the-gate).
