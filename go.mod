module github.com/docker/sandbox-kit-spec/v3

go 1.26.10

require (
	github.com/containerd/platforms v1.0.0-rc.5
	github.com/distribution/reference v0.6.0
	github.com/klauspost/compress v1.20.3
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2
	golang.org/x/term v0.47.0
	gopkg.in/yaml.v3 v3.0.1
	oras.land/oras-go/v2 v2.6.4
)

require (
	github.com/containerd/log v0.2.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

// These modules are in the graph but provide no imported packages, so tidy
// drops require overrides. Keep graph scans clear of GO-2026-6179/6180 and
// CVE-2026-78661/97045 until go-internal and x/crypto require fixed versions.
// Consumers do not inherit these replacements; neither module is compiled
// into the library. The frontend pins its own x/net requirement.
replace (
	golang.org/x/mod => golang.org/x/mod v0.40.0
	golang.org/x/net => golang.org/x/net v0.62.0
)
