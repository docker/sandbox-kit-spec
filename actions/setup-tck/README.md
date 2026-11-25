# Set up kit-tck

Install a released `kit-tck` executable and add it to `PATH` for subsequent
GitHub Actions steps. The action supports Linux, macOS, and Windows on
amd64 and arm64 runners.

## Use the action

Replace `<action-commit-sha>` with a full commit SHA containing this action.
Pin the action separately from the TCK release because they version
independently.

```yaml
steps:
  - uses: docker/sandbox-kit-spec/actions/setup-tck@<action-commit-sha>
    with:
      version: v3.0.0-m.13
  - name: Validate a published kit
    env:
      IMAGE: docker.io/example/my-kit@sha256:<image-digest>
    run: kit-tck validate "$IMAGE"
```

Log in to the registry before validation if the kit needs authentication.
The installer downloads public release assets without a GitHub token.
It does not run validation or log in to a registry.

## Inputs and outputs

| Input | Required | Meaning |
|---|---|---|
| `version` | Yes | Exact release version, such as `v3.0.0-m.13` or `3.0.0-m.13`. Aliases such as `latest` and version ranges are rejected. |
| `sha256` | No | Expected SHA256 of the selected platform's archive, in addition to the checksum published with the release. |

| Output | Meaning |
|---|---|
| `version` | Installed release version, including the leading `v`. |
| `path` | Absolute path to the installed executable. |

The action selects the archive from `RUNNER_OS` and `RUNNER_ARCH`.
Each OS and architecture combination has a different archive checksum.
For example, this pin selects the Linux amd64 m.13 archive:

```yaml
- uses: docker/sandbox-kit-spec/actions/setup-tck@<action-commit-sha>
  with:
    version: v3.0.0-m.13
    sha256: 9aaf699a662e55c35a2930dd4778a7f4e2317b563d64cc9d21622e6bac6f744c
```

## Verification and runner requirements

The installer checks the archive against the release's `checksums.txt`
before extracting the executable. An optional `sha256` pin must match
as well. PATH and outputs are written only after extraction succeeds.
Each invocation installs into its own temporary directory.

The checksum file and archive come from the same GitHub release. This
check detects corrupt or mismatched downloads; it does not provide an
independent signature or prevent a publisher from replacing both files.
A caller-provided SHA256 pin rejects changed release bytes even if the
published checksum also changes.

The action uses the runner's Node.js 24 action runtime and system `tar`.
Self-hosted runners need a runner version that supports Node.js 24 and
network access to GitHub release downloads. Windows runners need the
system `tar.exe` supplied with current Windows versions. Linux and macOS
runners need `tar` on PATH. No Python or npm installation is required.
