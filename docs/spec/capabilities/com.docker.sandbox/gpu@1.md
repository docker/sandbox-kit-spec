# `com.docker.sandbox/gpu@1`

A request for GPUs inside the sandbox: which GPU interface the image is
built for, and how many GPUs of it. The host may refuse when its
hardware or policy cannot provide them.

- **Shape**: singleton.
- **Permission surface**: yes. The driver and the count are both part
  of the grant.

## Config

```yaml
- type: com.docker.sandbox/gpu@1
  description: Runs CUDA inference
  config:
    driver: nvidia.com/gpu   # required
    devices: "1"             # optional; "all" when omitted
```

| Field | Type | Rules |
|---|---|---|
| `driver` | string | REQUIRED. `<dns-domain>/<name>`: the GPU interface the image's GPU stack is built for. |
| `devices` | string | optional. `all`, or a positive decimal count with no leading zero (`1`, `2`). Omitted means `all`. |

`config` is required, because `driver` is.

## Validation

The domain part of `driver` is a lowercase DNS name: dot-separated
labels of `a-z`, `0-9` and `-`, each starting and ending with a letter
or digit, at most 253 characters. The name part starts and ends with a
letter or digit, may contain `.`, `_` and `-` between, and is at most 63
characters. There is exactly one `/`.

`devices`, when present, is `all` or a count. `0` and `01` are errors, so
a count always reads as one number. A device index or address is not a
count: which host GPU serves the sandbox is the operator's choice.

Both fields accept Kit argument and environment references; the
substituted value is held to the same rules.

## Drivers

This page documents two drivers. Each row states what the sandbox gets,
what the runtime supplies, and what the image brings:

| `driver` | The sandbox gets | The runtime supplies | The image brings |
|---|---|---|---|
| `nvidia.com/gpu` | `/dev/nvidiactl`, one `/dev/nvidia<N>` per selected GPU, and `/dev/nvidia-uvm` when the driver creates it | The kernel modules (`nvidia.ko`, `nvidia-uvm.ko`) built for the sandbox kernel, loaded with their GSP firmware; the user-mode driver libraries matched to them (`libcuda`, `libnvidia-ml`); and `nvidia-smi` | CUDA toolkits and frameworks |
| `mesa3d.org/venus` | A virtio-gpu render node (`/dev/dri/renderD*`) whose host side accepts Venus | The sandbox kernel's virtio-gpu DRM driver, built in or loaded | Mesa's Venus Vulkan driver |

Kernel drivers are always the runtime's: the sandbox kernel belongs to
the runtime, and the workload cannot load modules into it.

Any other `<dns-domain>/<name>` is runtime-defined. A runtime that
offers one documents what it provides under the same three headings.
This page can document further names without a version move, because
the grammar already admits them.

## Runtime behavior

A conforming runtime:

- **MUST** make the selected GPUs usable by the user the composed image <!-- tck: gpu@1/devices-usable -->
  declares. Usable means the devices the driver's row names open
  read-write. A device node that exists but refuses open does not
  satisfy this duty.
- **MUST** provide that access before startup hooks and workload <!-- tck: gpu@1/before-startup -->
  execution, including commands launched later through exec.
- **MUST** supply what the driver's row says the runtime supplies: the <!-- tck: gpu@1/driver-contract -->
  kernel-side driver for every driver, and for `nvidia.com/gpu` the
  user-mode libraries matched to it.
- **MUST** scope the grant to the request. Only GPUs of the requested <!-- tck: gpu@1/selection-scoped -->
  driver are exposed, never a mix of drivers. `all` means every GPU of
  that driver the host makes available to the runtime, and at least one.
  A count `N` means exactly `N` of them.
- **MUST** refuse an unsatisfiable required request, naming the type: a <!-- tck: gpu@1/unsatisfiable-refused -->
  driver it does not offer, no GPU of that driver for `all`, or fewer
  than `N` GPUs of that driver. On a host with one Intel and one NVIDIA
  GPU, `driver: nvidia.com/gpu` with `devices: "2"` is refused. It never
  grants fewer GPUs, or another driver, silently. An optional request
  is skipped and recorded under
  [SPEC-v3 §7.1.1](../../SPEC-v3.md#711-capability-groups)'s general
  rules.
- Whether the grant can change on a live sandbox is runtime-owned. A
  runtime that fixes it at create refuses the change rather than
  reporting it applied.

The contract does not promise any driver version or framework
compatibility.

## Composition

One sandbox gets one driver and one count. Requests with the same
driver and the same effective `devices` collapse; an omitted `devices`
equals `all`. A required request wins over an optional one. A differing
driver or count is a composition error naming both contributors.

## Gate

Adding gpu@1, changing its driver, or raising its count **MUST** stop <!-- tck: gpu@1/widening-gates -->
for approval. A granted request covers a candidate when the drivers are
equal and either the grant's `devices` is `all`, or both are counts and
the candidate's is not larger. Anything else is a widening.
