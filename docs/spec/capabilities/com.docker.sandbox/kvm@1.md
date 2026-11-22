# `com.docker.sandbox/kvm@1`

A request to run hardware-accelerated Linux virtual machines inside the
sandbox. The runtime supplies usable KVM and vhost-vsock devices and
configures its own VM for nested virtualization when that is necessary.
The host may refuse when hardware, its hypervisor, or policy cannot
provide them.

- **Shape**: singleton, **config-less**.
- **Permission surface**: yes. Hardware virtualization access is a grant
  independent of container elevation.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/kvm@1
    description: Runs hardware-accelerated inner sandboxes
```

The entry **MUST NOT** carry `config`, including an empty or null <!-- tck: kvm@1/no-config -->
value. The composed image already identifies the user; the device paths
and behavior below are fixed by this contract.

Use `optional: true` only when the Kit works without hardware
virtualization. Installing a VM tool does not make its runtime
prerequisites optional.

## Runtime behavior

A conforming runtime:

- **MUST** make `/dev/kvm` and `/dev/vhost-vsock` usable by the user the <!-- tck: kvm@1/devices-usable -->
  composed image declares when granting this request. Usable KVM means
  opening the device read-write, obtaining KVM API version 12 through
  `KVM_GET_API_VERSION`, and successfully creating a VM with
  `KVM_CREATE_VM`. Usable vhost-vsock means opening the device
  read-write and successfully querying its features with
  `VHOST_GET_FEATURES`. A device node alone does not satisfy either
  duty. The runtime enables nested virtualization in its backing VM
  when needed for these operations.
- **MUST** provide that access before startup hooks and workload <!-- tck: kvm@1/before-startup -->
  execution, including commands launched later through exec.
- **MUST** refuse an unsatisfiable required request, naming the type. <!-- tck: kvm@1/required-unsatisfiable-refused -->
  Hardware and host policy determine whether the runtime can grant the
  request; merely recognizing its name is insufficient.
- **MUST** make that refusal during capability preflight, before startup <!-- tck: kvm@1/required-preflight -->
  hooks or workload execution.
- **MUST** skip and record an unavailable optional request without <!-- tck: kvm@1/optional-unavailable-skipped -->
  enabling virtualization access on behalf of that request. An
  independently accepted request or explicit operator grant can still
  supply the access. A later operational failure after acceptance is an
  error, not an optional skip.
- **MUST** preserve the accepted device access across stop and start <!-- tck: kvm@1/restart -->
  within the same sandbox, with no new selection on restart.

The capability does not install a VM tool or start a daemon. It supplies
no container elevation, FUSE access, Docker Engine, persistent storage,
credentials, host Docker socket, or network grant. Those concerns use
their own declarations. In particular, neither `privileged@1` nor
`dockerd@1` substitutes for this request, and this request implies neither.
The contract does not promise that an arbitrary guest kernel or VM tool
works with the runtime's CPU, kernel, security policy, or resource limits.

## Composition and grant changes

A workload, mixin, or enclosing set can request this capability.
Identical declarations collapse. A required declaration wins over an
optional one; the composition's request is optional only when every
requester can tolerate its absence.

A runtime **MUST** apply an accepted addition or withdrawal before <!-- tck: kvm@1/grant-transition -->
reporting the changed effective grant. If its existing VM or device
configuration cannot change in place, the runtime either recreates the
backing resources before reporting success or refuses the change while
preserving the previous effective composition. It does not report a
grant active while the VM lacks nesting, or report it withdrawn while
its devices remain usable.

When no accepted request or explicit operator grant remains, accepting <!-- tck: kvm@1/withdrawal -->
withdrawal **MUST** remove usable KVM and vhost-vsock access. Requiring
recreation is an acceptable way to apply that withdrawal.

Removing one contributor **MUST NOT** withdraw access still granted by <!-- tck: kvm@1/independent-grants -->
another accepted contribution or an explicit operator request. A
separate policy decision can revoke those grants too; removing one
contribution alone is not such a decision.

## Gate

Presence is permission surface, represented by the type in the services <!-- tck: kvm@1/widening-gates -->
list because there is no config. Adding it **MUST** stop for approval
even when `privileged@1` or `dockerd@1` was already approved. Optionality
and display metadata do not change that permission.

The runtime **SHOULD** require explicit, non-default consent to grant <!-- tck: kvm@1/explicit-consent -->
hardware virtualization access. It expands the sandbox's hardware and
kernel interfaces independently of ordinary container privilege.
