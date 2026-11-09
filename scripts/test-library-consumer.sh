#!/bin/sh
# A separate main module exercises MVS as a runtime consumer sees it. Replaces
# in either repository module must not hide a dependency upgrade in this check.
set -eu
export GOWORK=off
cd "$(dirname "$0")/.."
library_root=$(pwd -P)
consumer_dir=$(mktemp -d)
trap 'rm -rf "$consumer_dir"' EXIT HUP INT TERM

cp scripts/testdata/consumer/consumer_test.go "$consumer_dir/"
cd "$consumer_dir"
go mod init example.com/kit-library-consumer
go mod edit \
  -require=github.com/containerd/containerd/v2@v2.3.7 \
  -require=github.com/containerd/containerd/api@v1.11.1 \
  -require=github.com/docker/sandbox-kit-spec/v3@v3.0.0-m.10 \
  "-replace=github.com/docker/sandbox-kit-spec/v3=$library_root"
# Graph-only pins use replaces because tidy removes unused requirements.
go mod edit \
  -require=github.com/Microsoft/hcsshim@v0.15.0 \
  -require=golang.org/x/net@v0.62.0 \
  -replace=github.com/go-jose/go-jose/v4=github.com/go-jose/go-jose/v4@v4.1.9 \
  -replace=github.com/goccy/go-json=github.com/goccy/go-json@v0.11.0 \
  -replace=github.com/lestrrat-go/jwx/v3=github.com/lestrrat-go/jwx/v3@v3.3.0 \
  -replace=github.com/pelletier/go-toml/v2=github.com/pelletier/go-toml/v2@v2.4.3 \
  -replace=go.opentelemetry.io/otel/exporters/otlp/otlptrace=go.opentelemetry.io/otel/exporters/otlp/otlptrace@v1.47.0 \
  -replace=go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc=go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@v1.47.0 \
  -replace=go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp=go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.47.0 \
  -replace=golang.org/x/mod=golang.org/x/mod@v0.42.0
go mod tidy

# A build alone can pass after MVS upgrades containerd. Check both selected
# versions without replacing or excluding them, which would mask the regression.
selected=$(go list -m -f '{{.Path}} {{.Version}}' github.com/containerd/containerd/v2 github.com/containerd/containerd/api)
expected='github.com/containerd/containerd/v2 v2.3.7
github.com/containerd/containerd/api v1.11.1'
if [ "$selected" != "$expected" ]; then
  printf 'library changed the consumer containerd versions:\n%s\n' "$selected" >&2
  exit 1
fi
go test -mod=readonly -v ./...
