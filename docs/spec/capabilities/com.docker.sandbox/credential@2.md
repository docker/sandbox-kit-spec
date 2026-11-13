# `com.docker.sandbox/credential@2`

A host-managed service credential with an explicit missing-binding policy
and, optionally, a native OpenAI ChatGPT identity presentation. This
version does not change [`credential@1`](credential@1.md). Publishing a
new contract does not implement it in a runtime or migrate a sandbox.

- **Shape**: instance, one owner per (service, phase), across both versions.
- **Permission surface**: service per phase, plus the identity profile
  when present.

## Config

The generic fields retain the shapes and presentation rules of `@1`.
`@2` adds `onMissing` and `oauth.profile`:

| Field | Type | Rules |
|---|---|---|
| `onMissing` | string | `error` (default) or `unauthenticated`. Applies when a supported, granted request has no usable host binding. |
| `oauth.profile` | string | Omitted for generic OAuth, or `openai-chatgpt` for the native identity contract below. |

```yaml
- type: com.docker.sandbox/credential@2
  config:
    service: openai
    phase: runtime
    onMissing: unauthenticated
    oauth:
      profile: openai-chatgpt
      tokenEndpoint: {host: auth.openai.com, path: /oauth/token}
      resourceHosts: [chatgpt.com]
      sentinels:
        accessToken: openai-access-proxy-managed
        refreshToken: openai-refresh-proxy-managed
      credentialFile:
        path: ~/.codex/auth.json
        structure:
          auth_mode: chatgpt
          tokens:
            id_token: "{{.IDToken}}"
            access_token: "{{.AccessToken}}"
            refresh_token: "{{.RefreshToken}}"
            account_id: "{{.AccountID}}"
          last_refresh: "{{.LastRefresh}}"
```

This is a credential request, not a complete runnable Kit or a verified
Remote Control recipe. Resource hosts also need network grants. Relay
registration, pairing responses, SSH transport, and app feature support
are outside this capability.

### Support, permission, and missing credentials

Entry-level `optional` continues to govern capability selection. A
required `@2` request fails on a runtime that does not support `@2`; an
optional request may be skipped under the normal selection rules.
`onMissing: unauthenticated` does not make capability support optional.
It lets a compatible runtime start a logged-out workload without claiming
to have wired authentication.

A runtime **MUST** apply `onMissing` independently of entry optionality: <!-- tck: credential@2/missing-binding-policy -->
`error` fails resolution without a usable binding; `unauthenticated`
continues without credential variables, identity files, or outbound
credential injection for this request. In particular, a required request
may run unauthenticated, and an optional request that was selected may
still fail for a missing binding. Permission denial never becomes a grant
because a binding is available.

### Native profile validation

For `oauth.profile: openai-chatgpt`, the descriptor **MUST** use all of <!-- tck: credential@2/native-profile-shape -->
the following:

- `service: openai` and
  `tokenEndpoint: {host: auth.openai.com, path: /oauth/token}`;
- distinct, nonempty access and refresh sentinel strings containing only
  ASCII letters, digits, `_`, and `-`;
- no `passthrough: true` and no nonstandard `responseFields` mapping;
- `proxyManaged: true` if it also declares an API-key presentation.

The sentinel strings **MUST** be nonsecret markers, never copied provider <!-- tck: credential@2/nonsecret-markers -->
credentials. Grammar checks constrain their syntax; they cannot establish
where a Kit author obtained a string.

This profile accepts only the string placeholders `{{.IDToken}}`,
`{{.AccessToken}}`, `{{.RefreshToken}}`, `{{.AccountID}}`, and
`{{.LastRefresh}}`. Generic `@2` retains the original `@1` placeholders
and types. An `@1` entry or a generic `@2` entry cannot request native
identity by including one of the three new placeholders.

## Runtime behavior

A supporting runtime **MUST** implement `@1`'s runtime obligations, <!-- tck: credential@2/inherited-obligations -->
except `required-without-binding-fails`, which the missing-binding rule
above replaces. The native profile further restricts OAuth presentation
as follows, without opting any generic request into those restrictions.

### Identity presentation

The runtime **MUST** select the account and OAuth credentials from the <!-- tck: credential@2/host-authoritative-identity -->
authorized host binding. Sandbox-supplied identity claims, account IDs,
or sentinels cannot select another host account or authorize a request.
The guest identity is descriptive metadata, not proof of authentication.

A usable native binding includes host-managed OAuth credentials and a
decodable ID-token/account snapshot. An API-key-only binding does not
satisfy the native profile; it follows `onMissing` instead of silently
switching the client to API authentication.

The runtime **MUST** keep usable OAuth credentials outside the sandbox: <!-- tck: credential@2/native-secrets-outside -->
`AccessToken` and `RefreshToken` render the declared markers; `IDToken`
is reconstructed as the non-verifying JWT described below. A missing or
malformed host ID token cannot fall back to exposing the original token.
There is no passthrough fallback for this profile.

`AccountID` is the account selected by the host binding. `LastRefresh`
is the presentation time in RFC 3339 format; it does not extend the real
host token's lifetime. The runtime **MUST** render these values from one <!-- tck: credential@2/coherent-presentation -->
host identity snapshot, and refuse a presentation whose retained account
claim conflicts with the bound account.

The runtime **MUST** reconstruct `IDToken` with the fixed JWT header <!-- tck: credential@2/identity-allowlist -->
`{"alg":"RS256"}`, a payload containing only the typed fields below, and
a signature segment equal to the unpadded base64url encoding of the
literal `proxy-managed`. This signature is deliberately not valid. The
original header and signature are never copied. Unknown claims are
discarded, and a present allowlisted claim of the wrong type fails
presentation instead of being coerced.

| Claim path | Type |
|---|---|
| `email` | string |
| `exp` | integer |
| `https://api.openai.com/profile.email` | string |
| `https://api.openai.com/auth.chatgpt_plan_type` | string |
| `https://api.openai.com/auth.chatgpt_user_id` | string |
| `https://api.openai.com/auth.user_id` | string |
| `https://api.openai.com/auth.chatgpt_account_id` | string |
| `https://api.openai.com/auth.chatgpt_account_user_id` | string |
| `https://api.openai.com/auth.chatgpt_account_is_fedramp` | boolean |

The URL prefixes identify JSON object keys; the suffix after the final
dot identifies a field within that object. Fields absent in the host
token remain absent. This allowlist bounds disclosure; it does not claim
every field is necessary for every client feature. In particular, routing
metadata such as `chatgpt_account_is_fedramp` is not merely display text.

Changing the allowlist changes the identity disclosure contract and needs
an explicit versioned extension, permission review, and client
compatibility tests. A runtime cannot copy new provider fields merely
because a newer client starts using them. Supporting a new client may
therefore require a runtime and Kit update; this contract is not a promise
of compatibility with arbitrary future client versions.

### Token refresh

The runtime **MUST** restrict native access-token injection to declared <!-- tck: credential@2/native-resource-origins -->
HTTPS resource origins independently authorized by host provider policy
for the bound OpenAI identity, with the upstream peer verified. A Kit's
`resourceHosts` list or network grant alone is insufficient authorization.
Redirects cannot carry the credential to another origin without an
independent authorization check. The host owns provider routing; account
metadata supplied by the guest cannot expand the eligible origins.

At the canonical token endpoint, the runtime **MUST** intercept a native <!-- tck: credential@2/native-refresh-request -->
refresh request carrying the declared refresh marker and
`grant_type=refresh_token`, and use the bound host credentials for any
upstream refresh. Other grants and unexpected tokens are refused without
forwarding them. A guest-provided token or account selector is not a host
credential lookup key. Only declared resource hosts receive the real
access token at the boundary.

The upstream refresh is an HTTPS POST to the canonical endpoint,
constructed from host-owned credentials and client parameters. Guest
request bodies, headers, and extra parameters such as `client_id`,
`audience`, `resource`, or `redirect_uri` are not forwarded as upstream
refresh inputs.

After a successful refresh, the runtime **MUST** construct a fresh JSON <!-- tck: credential@2/native-refresh-response -->
response containing sentinel `access_token` and `refresh_token`, the
allowlisted `id_token`, bound `account_id`, `token_type: Bearer`, and a
positive integer `expires_in` equal to the remaining whole seconds before
the host token expires. Missing, invalid, or less-than-one-second remaining
expiry is a failure. No other upstream response body
fields, cookies, or provider response headers are forwarded; transport
headers are generated locally. The ID token uses the same filtering and
snapshot rules as the credential file.

The runtime **MUST** fail closed when refresh fails, the binding is <!-- tck: credential@2/native-refresh-failure -->
revoked, or usable host credentials are unavailable: no real token or
stale-success response is returned. Error responses expose only local
error codes and fixed descriptions, never upstream bodies or credentials.
Authentication recovery belongs on the host. `onMissing: unauthenticated`
permits logged-out startup; it does not turn a failed refresh into success.

## Composition and gate

Each listed phase participates independently. Entries from `@1` and `@2`
share one (service, phase) ownership space: overlapping requests conflict,
even if identical or declared by different versions. Distinct phases may
use different versions. Composition preserves the selected version,
profile, and missing-binding policy; it does not translate `@2` to `@1`.

The permission surface includes the service per phase. A native profile
also grants disclosure of the allowlisted account metadata per phase.
Adding that profile to an existing service widens the surface; a runtime
cannot treat it as an unchanged service-only grant. Skipping the request
contributes neither credential access nor identity disclosure.

## Compatibility

`credential@1` retains its schema, placeholder vocabulary, and runtime
behavior. Generic `@2` retains generic OAuth behavior, with the explicit
missing-binding policy above. Neither a refresh sentinel nor a host
OpenAI login activates native mode by itself.

A runtime advertises support only after implementing these obligations;
upgrading its spec library alone is insufficient. Kits that need native
identity use a required `@2` request so older runtimes reject them rather
than silently launching a partially authenticated client. An existing
persisted request stays at its selected version until an explicit,
supported adoption operation changes it; this contract defines no
in-place migration operation.
