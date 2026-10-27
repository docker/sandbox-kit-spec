#!/bin/sh
# Imported-package checks miss requirements that still affect consumers via MVS.
set -eu
export GOWORK=off
cd "$(dirname "$0")/.."

modules=$(go list -mod=readonly -m -f '{{.Path}}' all)
for module in $modules; do
  case "$module" in
    github.com/moby/buildkit|github.com/moby/buildkit/*|github.com/containerd/containerd|github.com/containerd/containerd/*|github.com/tonistiigi/fsutil|github.com/tonistiigi/fsutil/*)
      echo "library module must not require $module; keep it in cmd/frontend" >&2
      exit 1
      ;;
  esac
done
