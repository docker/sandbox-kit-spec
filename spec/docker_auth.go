package spec

import (
	"net"
	"regexp"
	"strings"
)

// DockerAuth grants host-only registry authentication using guest placeholders.
// Registries may delegate authentication through verified HTTPS challenges.
type DockerAuth struct {
	Registries []string `json:"registries"`
}

var dockerAuthDNS = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func validDockerAuthDNS(s string) bool {
	if len(s) > 253 || !dockerAuthDNS.MatchString(s) || net.ParseIP(s) != nil {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if len(label) > 63 {
			return false
		}
	}
	return true
}

func validateDockerAuthNeed(path string, i int, n Capability) error {
	fail := func(message string) error {
		return fieldErrorf(path+".config.registries", "capabilities[%d]: docker-auth %s", i, message)
	}
	var a DockerAuth
	if err := DecodeCapabilityConfig(n, &a); err != nil {
		return configErrorf(path, i, err)
	}
	if len(a.Registries) == 0 {
		return fail("requires a nonempty registries list")
	}
	seen := map[string]bool{}
	for _, registry := range a.Registries {
		if seen[registry] {
			return fail("lists a registry twice")
		}
		seen[registry] = true
		if ContainsArgRef(registry) || ContainsEnvRef(registry) {
			continue
		}
		if !validDockerAuthDNS(registry) {
			return fail("requires exact lowercase DNS registry names without schemes, ports, IP addresses or paths")
		}
	}
	return nil
}
