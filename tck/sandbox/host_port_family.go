package sandbox

import (
	"context"
	"crypto/rand"
	"errors"
	"net"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func hostPortFamily(ctx context.Context, e *Env) []report.Finding {
	for _, family := range []string{"ipv4", "ipv6", "dual"} {
		for _, transport := range []string{"tcp", "udp"} {
			network, address, ip := "4", "127.0.0.1:0", net.IPv4(127, 0, 0, 1)
			otherNetwork, otherHost, otherIP := "6", "::1", net.IPv6loopback
			if family != "ipv4" {
				network, address, ip = "6", "[::1]:0", net.IPv6loopback
				otherNetwork, otherHost, otherIP = "4", "127.0.0.1", net.IPv4(127, 0, 0, 1)
			}
			var port string
			var stop func()
			var probe func(string) []report.Finding
			if transport == "tcp" {
				s, err := listenHostPortTCP("tcp"+network, address)
				if err != nil {
					return []report.Finding{report.Failf("%s TCP host listener: %v", family, err)}
				}
				port, stop = s.port(), s.start()
				// Single-family grants must select the right service even when
				// the other loopback has a different listener at the same port.
				other, err := listenHostPortTCP("tcp"+otherNetwork, net.JoinHostPort(otherHost, port))
				if err != nil {
					stop()
					return []report.Finding{report.Failf("%s opposite-family TCP listener: %v", family, err)}
				}
				if family == "dual" {
					other.key = s.key
				}
				stopFirst, stopOther := stop, other.start()
				stop = func() { stopFirst(); stopOther() }
				probe = func(id string) []report.Finding {
					out, failure := execOutput(ctx, e, id, "kit-tck-host-port-tcp", "18432")
					if failure != nil {
						return []report.Finding{*failure}
					}
					if err := hostPortTCPVerify(out, s); err != nil && (family != "dual" || hostPortTCPVerify(out, other) != nil) {
						return []report.Finding{report.Failf("%s TCP: %v", family, err)}
					}
					return nil
				}
			} else {
				s, err := listenHostPortUDP("udp"+network, ip)
				if err != nil {
					return []report.Finding{report.Failf("%s UDP host listener: %v", family, err)}
				}
				port, stop = s.port(), s.start()
				conn, err := net.ListenUDP("udp"+otherNetwork, &net.UDPAddr{IP: otherIP, Port: s.conn.LocalAddr().(*net.UDPAddr).Port})
				if err != nil {
					stop()
					return []report.Finding{report.Failf("%s opposite-family UDP listener: %v", family, err)}
				}
				other := &hostPortUDPService{conn: conn}
				if _, err := rand.Read(other.key[:]); err != nil {
					_ = conn.Close()
					stop()
					return []report.Finding{report.Failf("UDP decoy key: %v", err)}
				}
				if family == "dual" {
					other.key = s.key
				}
				stopFirst, stopOther := stop, other.start()
				stop = func() { stopFirst(); stopOther() }
				probe = func(id string) []report.Finding {
					out, failure := execOutput(ctx, e, id, "kit-tck-host-port-udp", "18432", "single")
					if failure != nil {
						return []report.Finding{*failure}
					}
					if err := hostPortUDPVerify(out, "single", s); err != nil && (family != "dual" || hostPortUDPVerify(out, "single", other) != nil) {
						return []report.Finding{report.Failf("%s UDP: %v", family, err)}
					}
					return nil
				}
			}
			id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-family"}, map[string]string{"host_port": port, "family": family, "transport": transport})
			if err != nil {
				stop()
				return []report.Finding{report.Failf("%s %s create: %v", family, transport, err)}
			}
			findings := probe(id)
			if len(findings) == 0 {
				selection, err := e.Adapter.Selection(ctx, id)
				if err != nil {
					findings = []report.Finding{report.Failf("selection: %v", err)}
				} else if len(selection.Surface.HostPorts) != 1 || selection.Surface.HostPorts[0] != port+"->18432/"+transport+"/"+family {
					findings = []report.Finding{report.Failf("host family missing from permission surface: %+v", selection.Surface)}
				}
			}
			remove()
			stop()
			if len(findings) > 0 {
				return findings
			}
		}
	}
	for _, family := range []string{"ipv6", "dual"} {
		for _, kits := range [][]string{{fixtureWorkload, "host-port", "host-port-family"}, {fixtureWorkload, "host-port-family", "host-port"}} {
			_, remove, err := e.sandbox(ctx, kits, map[string]string{"host_port": "18431", "container_port": "18432", "family": family, "transport": "tcp"})
			remove()
			var refused *adapter.RefusedError
			if !errors.As(err, &refused) {
				return []report.Finding{report.Failf("different host families at one sandbox endpoint must refuse: %v", err)}
			}
		}
	}
	return nil
}

func hostPortPublishedConflict(ctx context.Context, e *Env) []report.Finding {
	for _, kits := range [][]string{{fixtureWorkload, "host-port", "port"}, {fixtureWorkload, "port", "host-port"}} {
		_, remove, err := e.sandbox(ctx, kits, map[string]string{"host_port": "18431", "container_port": "8080"})
		remove()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("published port and host relay collision must refuse: %v", err)}
		}
	}
	return nil
}

func init() {
	checks = append(checks,
		check{requirement: "host-port@1/family-honored", capability: capHostPort, run: hostPortFamily},
		check{requirement: "host-port@1/published-port-conflict", capability: capHostPort, needs: []string{capPort}, run: hostPortPublishedConflict},
	)
}
