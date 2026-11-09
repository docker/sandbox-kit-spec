module github.com/docker/sandbox-kit-spec/v3/cmd/frontend

go 1.26.10

require (
	github.com/containerd/platforms v1.0.0-rc.5
	github.com/distribution/reference v0.6.0
	github.com/docker/sandbox-kit-spec/v3 v3.0.0-m.10
	github.com/moby/buildkit v0.34.2
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/stretchr/testify v1.12.1
	github.com/tonistiigi/fsutil v0.0.0-20261103125000-e075fd9305eb
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/containerd/v2 v2.4.2 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/log v0.2.0 // indirect
	github.com/containerd/log/otel v0.1.0 // indirect
	github.com/containerd/ttrpc v1.2.10 // indirect
	github.com/containerd/typeurl/v2 v2.3.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/in-toto/attestation v1.2.0 // indirect
	github.com/in-toto/in-toto-golang v0.11.0 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/go-archive v0.3.3 // indirect
	github.com/moby/locker v1.0.1 // indirect
	github.com/moby/patternmatcher v0.6.1 // indirect
	github.com/moby/sys/signal v0.7.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/secure-systems-lab/go-securesystemslib v0.11.1 // indirect
	github.com/shibumi/go-pathspec v1.3.0 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/tonistiigi/go-csvvalue v0.0.0-20240814133006-030d3b2625d0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261104224025-d1e00e9f33ab // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	oras.land/oras-go/v2 v2.7.0 // indirect
)

// containerd still requires v0.17.3, which has CVE-2026-10722. Replace all
// versions so a transitive upgrade cannot bypass the fixed release. Remove
// this replacement once containerd requires v0.22.0 or later.
replace github.com/cilium/ebpf => github.com/cilium/ebpf v0.22.0

// Build the frontend against the library in this checkout, including releases.
replace github.com/docker/sandbox-kit-spec/v3 => ../..

// containerd's graph still requires v4.1.5 (GHSA-p9j4-2j95-j4rv), but no
// frontend package imports it, so tidy drops a require override. Remove
// this replacement once containerd requires v4.1.7 or later.
replace github.com/go-jose/go-jose/v4 => github.com/go-jose/go-jose/v4 v4.1.9

// These graph-only dependencies provide no imported packages, so tidy drops
// require overrides. Remove each replacement once upstream requires at least
// the fixed version named below.
replace (
	// v0.19.0 fixes CVE-2026-77395 and CVE-2026-71482, respectively.
	github.com/containerd/stargz-snapshotter => github.com/containerd/stargz-snapshotter v0.19.1
	github.com/containerd/stargz-snapshotter/estargz => github.com/containerd/stargz-snapshotter/estargz v0.19.1
	// Revision bd33e0b11a3d fixes GHSA-cj86-7jch-72f4 and includes the
	// earlier BER parsing fix for GHSA-3xqc-rpq8-wgp9.
	github.com/digitorus/pkcs7 => github.com/digitorus/pkcs7 v0.0.0-20261104125046-a476de3098e4
	// v1.0.2 fixes GHSA-cqr7-r6x2-9cqf and GHSA-m8w9-vj7g-gxgg.
	github.com/go-openapi/jsonpointer => github.com/go-openapi/jsonpointer v1.0.2
	// v1.0.1 fixes GHSA-57xm-4xm7-hqm3.
	github.com/go-openapi/validate => github.com/go-openapi/validate v1.0.1
)
