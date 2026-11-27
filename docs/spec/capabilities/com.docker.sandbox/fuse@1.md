# `com.docker.sandbox/fuse@1`

A request to mount FUSE filesystems (sshfs, rclone, s3fs, gcsfuse)
inside the sandbox. Present or absent — there is nothing to configure,
and the host may refuse.

- **Shape**: singleton, **config-less** — any `config` value is a
  validation error.
- **Permission surface**: yes. FUSE access is a grant independent of
  container elevation.

## Config

```yaml
- type: com.docker.sandbox/fuse@1
  description: Mounts the team's bucket with gcsfuse
```

No `config`.

## Runtime behavior

A conforming runtime:

- **MUST** expose a FUSE character device at `/dev/fuse` that the <!-- tck: fuse@1/device-granted -->
  workload's processes can open read-write, when granted. The device is
  the sandbox kernel's; mounts made through it stay in the sandbox.
- **MUST** keep `CAP_SYS_ADMIN` in the bounding set of the workload's <!-- tck: fuse@1/mount-capability -->
  processes, when granted. A non-root process holds no effective
  capability until it executes a setuid-root program, or one whose file
  capabilities include `CAP_SYS_ADMIN`, as on a host. A setuid-root
  program gains the whole bounding set, so the grant makes
  `CAP_SYS_ADMIN` reachable to every setuid-root program in the sandbox,
  `sudo` and `su` as well as `fusermount3`. A file-capability program
  gains only the capabilities its file carries.
- **MUST** let a non-root process of the workload mount a FUSE <!-- tck: fuse@1/non-root-mount -->
  filesystem through a setuid-root `fusermount3` the image carries, when
  granted, with the mount visible to the workload's other processes.
  `no_new_privs` stays unset, and no seccomp filter or security module
  denies the helper its elevation or the mount.
- **MUST NOT** satisfy this request by running the sandbox in its <!-- tck: fuse@1/no-elevation -->
  platform's elevated-privilege mode: a composition requesting `fuse@1`
  without `privileged@1` or `dockerd@1` runs without that mode. The grant
  relaxes no other policy either; devices, network, and storage stay as
  their own requests set them.
- **MAY** refuse, for example when the sandbox kernel lacks FUSE.
  Refusal fails resolution for a required entry; an optional entry is
  skipped and recorded, and the Kit runs without FUSE.

## Composition and grant changes

One Kit requesting it grants FUSE to the composed sandbox. There is no
narrower scope: the device and the bounding set are per-sandbox.

A runtime **MUST** apply an accepted addition or withdrawal of this <!-- tck: fuse@1/grant-transition -->
request before reporting the changed effective grant. If it cannot change
the sandbox in place, it either recreates the sandbox before reporting
success or refuses the change while preserving the previous effective
composition. It does not report the grant active while `/dev/fuse` is
unusable, or report it withdrawn while the device remains usable.

When no accepted request or explicit operator grant remains, accepting <!-- tck: fuse@1/withdrawal -->
withdrawal **MUST** remove usable access to `/dev/fuse`. Requiring
recreation is an acceptable way to apply it. Withdrawal does not cover
`CAP_SYS_ADMIN`, which `privileged@1` and `dockerd@1` also grant.

Removing one contributor **MUST NOT** withdraw access still granted by <!-- tck: fuse@1/independent-grants -->
another accepted contribution or an explicit operator grant.

## Gate

Presence is permission surface, represented by the type in the services <!-- tck: fuse@1/widening-gates -->
list because there is no config. Adding it **MUST** stop for approval
even when `privileged@1` or `dockerd@1` was already approved.

The runtime **SHOULD** require explicit, non-default consent to grant <!-- tck: fuse@1/explicit-consent -->
FUSE access, and say when asking that the grant makes `CAP_SYS_ADMIN`
reachable to every setuid-root program in the sandbox, not only
`fusermount3`. A standing setting the user turned on counts as that
consent; one that is on by default does not.
