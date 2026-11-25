# `com.docker.sandbox/openai-chatgpt@1`

Native ChatGPT authentication for Codex, including Remote Control over
SSH, with usable credentials held by the host. The sandbox receives a
local account presentation and placeholder tokens; the host authorizes
their use at the network and SSH boundaries.

- **Shape**: instance, sharing the credential owner key `(openai, runtime)`.
- **Permission surface**: runtime access to the `openai` service and
  disclosure of the complete identity-token payload.

## Config

```yaml
- type: com.docker.sandbox/openai-chatgpt@1
  description: Use the host ChatGPT account with Codex over SSH
  config:
    path: ~/.codex/auth.json
```

| Field | Type | Rules |
|---|---|---|
| `path` | string | Required destination for the generated Codex authentication file, absolute in the sandbox or relative to the workload user's home with `~/`; no control characters or trailing slash. |

The config **MUST** contain only a nonempty `path` in one of those forms. <!-- tck: openai-chatgpt@1/config-valid -->
The service is always `openai` and the phase is always `runtime`.
There is no API-key mode, passthrough mode, configurable token endpoint,
identity-field list, or `onMissing` policy in this capability.

This is a separate capability from
[`credential@1`](credential@1.md). Understanding generic OAuth or
updating a specification library does not establish native ChatGPT
support. Existing credential declarations keep their behavior.

## Support and authorization

- A runtime **MUST** explicitly support this exact capability before <!-- tck: openai-chatgpt@1/explicit-support -->
  accepting a required request. Support for `credential@1` alone does
  not satisfy it. An unsupported optional request is skipped and
  recorded under the ordinary capability-selection rules.
- A supporting runtime **MUST** resolve the current OpenAI authorization <!-- tck: openai-chatgpt@1/host-binding -->
  from the host credential store. A required request without usable
  host OAuth credentials fails; an optional request without them is
  skipped and recorded. An API key is not a native ChatGPT binding.
  An incomplete identity or a refresh failure is reported as such,
  rather than treated as a request to sign in inside the sandbox.
- A runtime **MUST** authorize the selected host account and native <!-- tck: openai-chatgpt@1/permission-surface -->
  identity disclosure before presenting them. Permission to inject
  a generic OpenAI credential alone does not authorize disclosure of
  its identity-token payload. The native grant exposes every current
  and future claim in that payload, including nested and unknown
  fields; it does not limit disclosure to a fixed list of claims.
- The host **MUST** remain authoritative for the account, sandbox and <!-- tck: openai-chatgpt@1/host-authoritative -->
  provider destinations. A guest-supplied account ID, identity claim,
  endpoint, or token cannot select another authorization. Network
  permission alone cannot redirect a credential: destination approval
  is checked independently of the Kit's network policy. Unsupported
  account routing or organization policy is refused explicitly.

This capability grants neither network access nor arbitrary access to
host tools. The Kit requests its network policy and other permissions
separately. It applies only at runtime, not during install hooks.

## Guest presentation

The runtime **MUST** place a native Codex authentication presentation at <!-- tck: openai-chatgpt@1/guest-presentation -->
`path` before the managed Codex session starts. Its `auth_mode` is
`"chatgpt"`, it has no usable OpenAI API key, and it presents:

- `tokens.access_token` and `tokens.refresh_token`: placeholders;
- `tokens.id_token`: the identity presentation described below;
- `tokens.account_id`: the account selected by the host;
- `last_refresh`: the time the host prepared this presentation.

The runtime creates and recognizes its placeholders. A Kit does not
supply token values, host secret locations or an authentication-file
template. The file is for the workload user; host paths are never
interpreted from it. The runtime also prevents API-key settings from
overriding this native mode in the managed Codex process.

The identity presentation **MUST** preserve the original JWT's encoded <!-- tck: openai-chatgpt@1/identity-payload -->
header and payload segments exactly and replace its signature with a
nonempty, deliberately invalid signature. No identity-claim allowlist,
redaction, reordering, or rewriting is applied, including to nested or
newly added claims. The original signed token is not returned to the
sandbox. A malformed or unsupported source identity is refused rather
than converted into a usable token or a partial identity.

This ID token is a local account presentation, not the access token
used to call OpenAI. The proxy uses the real host access token after
checking the request's authorization. Guest-visible identity fields
never constitute evidence that the host authorized the account.

## Credential boundary and SSH compatibility

- The runtime **MUST** keep usable credentials managed by this <!-- tck: openai-chatgpt@1/managed-credentials -->
  capability outside the sandbox: OAuth access and refresh tokens,
  original signed identity tokens, Remote Control enrollment and relay
  credentials, pairing tokens, Desktop attestation proofs and Desktop
  tool credentials. It mediates both network exchanges and credential-
  bearing SSH requests and responses. Blocking an OAuth HTTP endpoint
  alone is insufficient because login and refresh messages can carry
  credentials directly over SSH. A managed process cannot establish
  an independent guest-local OAuth login or accept usable tokens
  through those managed protocol paths. Pairing information required
  by the host Desktop client is restored only toward that authorized
  client, never by disclosing the usable token to the guest.
- The runtime **MUST** perform OAuth refresh on the host and return <!-- tck: openai-chatgpt@1/host-refresh -->
  only the guest presentation. The host constructs the provider's
  refresh request from the bound authorization; guest request bodies
  cannot supply a replacement account, refresh token or endpoint.
  Refresh and credential replacement remain distinct authorization
  events, and a stale response cannot restore a revoked authorization.
- The runtime **MUST** preserve the existing functionality of supported <!-- tck: openai-chatgpt@1/ssh-compatible -->
  Desktop SSH sessions, including chats, filesystem operations, tool
  registration and calls, and permitted forwarding. Credential-bearing
  tool configuration is mediated with host-held credentials and guest
  placeholders, rather than dropped wholesale. Ordinary SSH commands
  retain their stdin, environment and forwarding behavior; enabling
  this capability does not impose the managed Codex protocol on them.
- The runtime **MUST** report unsupported managed protocol versions or <!-- tck: openai-chatgpt@1/unsupported-protocol -->
  credential exchanges clearly and refuse that operation, without
  falling back to an unprotected SSH or network path. A runtime
  advertises the client and guest versions it supports. Unknown
  identity claims alone are not an unsupported protocol version.
- The runtime **MUST** bind managed connections and derived credentials <!-- tck: openai-chatgpt@1/revocation -->
  to the current host authorization and sandbox permission. Logout,
  account replacement, permission withdrawal or sandbox teardown
  cancels pending work, invalidates mappings, closes managed relay
  connections and rejects renewal or reconnect under the old grant.
  Ordinary SSH remains independent of OpenAI authorization.

These duties cover credentials managed by this capability. They do
not claim to identify every unrelated secret a user deliberately puts
in a prompt, file, process environment or third-party tool setting.
Preserving existing SSH functionality does not mean automatically
forwarding host environment variables or host credentials.

## Composition and adoption

The native request **MUST** have one owner of `(openai, runtime)`, <!-- tck: openai-chatgpt@1/credential-owner -->
shared with `credential@1`. Two native requests or a native request
and a generic runtime OpenAI credential conflict, even when optional
or identical. An install-only generic OpenAI credential does not
overlap. Changing `path` does not create a second credential owner.

A Kit selects this capability explicitly. Existing Kits and sandboxes
are not converted by upgrading the specification or runtime. A
runtime may require a new sandbox for adoption; this contract promises
no in-place conversion. A required request on an older runtime is
refused under the existing unknown-capability rules.

The reference implementation defines the grammar, permission surface,
ownership and identity projection. It does not implement the host
broker or SSH adapter. Runtime conformance observations and their
current gaps are described in [conformance](../../conformance.md).
