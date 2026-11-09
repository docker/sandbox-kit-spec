# `com.docker.sandbox/usb-device@1`

One USB passthrough request, matched by vendor/product identity or by
device class. Hardware access is host-answerable and permission-gated like
any other grant.

- **Shape**: instance; exact-duplicate entries rejected.
- **Permission surface**: yes — `vendor:product` or `class:<class>`.

## Execution plan

The default `order` is 100. The scheduled actions are
`configure:configure`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
- type: com.docker.sandbox/usb-device@1
  optional: true
  description: YubiKey for signing
  config:
    vendorId: "1050"       # with productId; exclusive with class
    productId: "0407"
```

```yaml
- type: com.docker.sandbox/usb-device@1
  config:
    class: smart-card      # exclusive with vendorId/productId
```

| Field | Type | Rules |
|---|---|---|
| `vendorId` | string | With `productId`. The pair goes together. |
| `productId` | string | With `vendorId`. |
| `class` | string | Device class match. Exclusive with the ID pair — declare exactly one form. |

## Validation

Exactly one of (`vendorId` + `productId`) or `class`; an ID without its
partner is an error.

## Runtime behavior

A conforming runtime:

- **MUST** pass matching devices through to the sandbox when granted, and <!-- tck: usb-device@1/matching-devices-passed -->
  refuse the Kit (required) or skip the entry (optional) when it cannot or
  will not.
- **MUST** treat the grant as scoped to the match: a `class` grant does not <!-- tck: usb-device@1/grant-scoped-to-match -->
  admit unrelated devices, an ID grant admits only that vendor/product.
- **MAY** prompt per attach. Hotplug behavior (devices appearing after
  create) is runtime-owned; a runtime that supports it applies the same
  match.
- Runtimes on hosts without USB brokering (e.g. remote VMs) refuse rather
  than emulate.

## Composition

Entries union across the set.

## Gate

Each match is permission surface. A new match widens; broadening an ID
match to a class match is a new entry and widens.
