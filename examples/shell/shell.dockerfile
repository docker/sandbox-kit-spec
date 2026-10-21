# syntax=docker/dockerfile:1
# Keep the hardened Debian base and its platform floor (bash, the agent
# user, git, a CA store and Docker). Until DHI republishes shell-docker,
# forward-port the certificate-trust fixes from shell template 0.10.0.
FROM dhi.io/sbx-templates:shell-docker

ARG GO_VERSION="1.27.3"
ARG UV_VERSION="0.13.4"
ARG GH_MIN_VERSION="2.102.1"
ARG PIP_VERSION="26.2.2"
ARG CLIPBOARD_BRIDGE_VERSION="v0.3.1"
ARG TARGETARCH

USER root
RUN <<EOF
set -eu
# DHI already publishes fixed Go, uv and pip packages. GitHub CLI needs
# its upstream signed repository until DHI publishes the fixed release.
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    -o /etc/apt/keyrings/githubcli.gpg
chmod a+r /etc/apt/keyrings/githubcli.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list
apt-get update
# APT replaces golang-1.26 and its dependent OCI-config package, so the
# old compiler is removed and the inferred deb/* provides stay accurate.
apt-get install -y --no-install-recommends \
    ca-certificates \
    golang-1.27=${GO_VERSION}-0 \
    uv=${UV_VERSION}-0 \
    gh \
    python3-pip=${PIP_VERSION}-0
apt-get clean
rm -rf /var/lib/apt/lists/*

# Replace the template's embedded bridge with the Go 1.27.3 rebuild.
base="https://github.com/docker/clipboardbridge/releases/download/${CLIPBOARD_BRIDGE_VERSION}"
asset="clipboard-bridge-linux-${TARGETARCH}"
tmp=$(mktemp -d)
curl -fsSL "${base}/${asset}" -o "${tmp}/${asset}"
curl -fsSL "${base}/SHA256SUMS" -o "${tmp}/SHA256SUMS"
(cd "${tmp}" && grep " ${asset}\$" SHA256SUMS | sha256sum -c -)
install -m 0755 "${tmp}/${asset}" /usr/local/bin/clipboard-bridge
rm -rf "${tmp}"

# Check the binaries on PATH, not just the versions APT recorded.
go version | grep -F "go${GO_VERSION} "
uv --version | grep -F "uv ${UV_VERSION}"
# GitHub's APT index only retains its latest release.
dpkg --compare-versions "$(dpkg-query -W -f='${Version}' gh)" ge "${GH_MIN_VERSION}"
python3 -m pip --version | grep -F "pip ${PIP_VERSION} "
clipboard-bridge --version | grep -F "${CLIPBOARD_BRIDGE_VERSION}"
go version -m /usr/local/bin/clipboard-bridge | grep -F 'go1.27.3'
dpkg --compare-versions "$(dpkg-query -W -f='${Version}' ca-certificates)" ge '20250419+deb13u1'
EOF

USER 1000
ENTRYPOINT ["bash"]
CMD ["-l"]
