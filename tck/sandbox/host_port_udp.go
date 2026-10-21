package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// The response transformation is private to the host service, so a local
// echo server or canned successful exec cannot stand in for forwarding.
type hostPortUDPService struct {
	conn      *net.UDPConn
	key       [32]byte
	requests  atomic.Uint64
	mu        sync.Mutex
	datagrams [][]byte
}

func newHostPortUDPService() (*hostPortUDPService, error) {
	return listenHostPortUDP("udp4", net.IPv4(127, 0, 0, 1))
}

func listenHostPortUDP(network string, ip net.IP) (*hostPortUDPService, error) {
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
	if err != nil {
		return nil, err
	}
	s := &hostPortUDPService{conn: conn}
	if _, err := rand.Read(s.key[:]); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

func (s *hostPortUDPService) port() string {
	return strconv.Itoa(s.conn.LocalAddr().(*net.UDPAddr).Port)
}
func (s *hostPortUDPService) response(data []byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ s.key[i%len(s.key)]
	}
	return out
}
func (s *hostPortUDPService) start() func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 65535)
		for {
			n, peer, err := s.conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			s.mu.Lock()
			s.datagrams = append(s.datagrams, bytes.Clone(buffer[:n]))
			s.mu.Unlock()
			s.requests.Add(1)
			if _, err := s.conn.WriteToUDP(s.response(buffer[:n]), peer); err != nil {
				return
			}
		}
	}()
	return func() { _ = s.conn.Close(); <-done }
}

func hostPortUDPVerify(out, mode string, s *hostPortUDPService) error {
	var clients [][]struct{ Sent, Received []byte }
	if err := json.Unmarshal([]byte(out), &clients); err != nil {
		return fmt.Errorf("decode UDP observations: %w", err)
	}
	count := 1
	if mode == "clients" {
		count = 4
	}
	if len(clients) != count {
		return fmt.Errorf("got %d UDP clients, want %d", len(clients), count)
	}
	sizes := []int{31, 0, 257, 1024, 17}
	if mode == "single" {
		sizes = []int{32}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]bool, len(s.datagrams))
	for i, records := range clients {
		if len(records) != len(sizes) {
			return fmt.Errorf("client %d returned %d datagrams, want %d", i, len(records), len(sizes))
		}
		for j, record := range records {
			arrived := false
			for k, datagram := range s.datagrams {
				if !matched[k] && bytes.Equal(datagram, record.Sent) {
					matched[k], arrived = true, true
					break
				}
			}
			if !arrived {
				return fmt.Errorf("client %d datagram %d: request bytes not observed at host", i, j)
			}
			if len(record.Sent) != sizes[j] || !bytes.Equal(record.Received, s.response(record.Sent)) {
				return fmt.Errorf("client %d datagram %d: host response contents or boundary changed", i, j)
			}
		}
	}
	return nil
}

func hostPortUDPProbe(ctx context.Context, e *Env, id, port, mode string, s *hostPortUDPService) []report.Finding {
	out, failure := execOutput(ctx, e, id, "kit-tck-host-port-udp", port, mode)
	if failure != nil {
		return []report.Finding{*failure}
	}
	if err := hostPortUDPVerify(out, mode, s); err != nil {
		return []report.Finding{report.Failf("%v", err)}
	}
	return nil
}

func hostPortUDPForward(mode string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		s, err := newHostPortUDPService()
		if err != nil {
			return []report.Finding{report.Failf("host UDP listener: %v", err)}
		}
		defer s.start()()
		id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-udp"}, map[string]string{"host_port": s.port(), "container_port": "18432"})
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer remove()
		return hostPortUDPProbe(ctx, e, id, "18432", mode, s)
	}
}

func hostPortUDPComposition(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortUDPService()
	if err != nil {
		return []report.Finding{report.Failf("host UDP listener: %v", err)}
	}
	defer s.start()()
	other, err := newHostPortUDPService()
	if err != nil {
		return []report.Finding{report.Failf("second UDP listener: %v", err)}
	}
	defer other.start()()
	tcp, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("host TCP listener: %v", err)}
	}
	defer tcp.start()()
	// UDP defaults and an explicit equivalent mapping deduplicate, while
	// aliases remain live and TCP can occupy the same numeric guest port.
	args := map[string]string{"host_port": s.port(), "container_port": s.port(), "default_host_port": s.port(), "other_host_port": other.port(), "tcp_host_port": tcp.port()}
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-udp", "host-port-udp-default", "host-port-udp-alias"}, args)
	if err != nil {
		return []report.Finding{report.Failf("compose UDP mappings and TCP: %v", err)}
	}
	defer remove()
	for _, probe := range []struct {
		port    string
		service *hostPortUDPService
	}{{s.port(), s}, {"18433", s}, {"18434", other}} {
		if findings := hostPortUDPProbe(ctx, e, id, probe.port, "single", probe.service); len(findings) > 0 {
			return findings
		}
	}
	if findings := hostPortProbe(ctx, e, id, s.port(), tcp.token); len(findings) > 0 {
		return findings
	}
	for _, kits := range [][]string{{fixtureWorkload, "host-port-udp", "host-port-udp-default"}, {fixtureWorkload, "host-port-udp-default", "host-port-udp"}} {
		_, cleanup, err := e.sandbox(ctx, kits, map[string]string{"host_port": "18432", "container_port": "18431", "default_host_port": "18431"})
		cleanup()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("conflicting UDP destinations must refuse: %v", err)}
		}
	}
	return nil
}

func hostPortUDPHooks(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortUDPService()
	if err != nil {
		return []report.Finding{report.Failf("host UDP listener: %v", err)}
	}
	defer s.start()()
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-udp-hooks"}, map[string]string{"host_port": s.port(), "container_port": "18432"})
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	for _, phase := range []string{"install", "startup"} {
		out, failure := execOutput(ctx, e, id, "cat", "/var/tmp/kit-tck-host-port-udp-"+phase)
		if failure != nil {
			return []report.Finding{*failure}
		}
		if err := hostPortUDPVerify(out, "single", s); err != nil {
			return []report.Finding{report.Failf("%s hook: %v", phase, err)}
		}
	}
	return hostPortUDPProbe(ctx, e, id, "18432", "single", s)
}

func hostPortUDPNoGrant(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortUDPService()
	if err != nil {
		return []report.Finding{report.Failf("host UDP listener: %v", err)}
	}
	defer s.start()()
	_, remove, err := e.sandboxWith(ctx, []string{fixtureWorkload, "host-port-udp-default"}, adapter.CreateOptions{Args: map[string]string{"default_host_port": s.port()}, RejectCapabilities: []string{capHostPort}})
	remove()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("rejected required UDP mapping must refuse: %v", err)}
	}
	for _, active := range []bool{false, true} {
		kits := []string{fixtureWorkload}
		var args map[string]string
		var mapped *hostPortUDPService
		if active {
			mapped, err = newHostPortUDPService()
			if err != nil {
				return []report.Finding{report.Failf("active host UDP listener: %v", err)}
			}
			defer mapped.start()()
			kits = append(kits, "host-port-udp-default")
			args = map[string]string{"default_host_port": mapped.port()}
		}
		id, cleanup, err := e.sandbox(ctx, kits, args)
		if err != nil {
			return []report.Finding{report.Failf("control create: %v", err)}
		}
		defer cleanup()
		if active {
			if findings := hostPortUDPProbe(ctx, e, id, mapped.port(), "single", mapped); len(findings) > 0 {
				return findings
			}
		}
		res, err := e.Adapter.Exec(ctx, id, "kit-tck-host-port-udp", s.port(), "single")
		if err != nil {
			return []report.Finding{report.Failf("ungranted UDP probe: %v", err)}
		}
		if res.ExitCode != 0 && res.ExitCode != 7 {
			return []report.Finding{report.Failf("ungranted UDP observation failed: exit %d: %s", res.ExitCode, res.Stderr)}
		}
		if res.ExitCode == 0 && hostPortUDPVerify(res.Stdout, "single", s) == nil {
			return []report.Finding{report.Failf("host UDP service reachable without a grant (active mapping=%t)", active)}
		}
		if findings := hostPortNoRequests(ctx, &s.requests, "UDP"); len(findings) > 0 {
			return findings
		}
	}
	return nil
}

func hostPortUDPOnDemand(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortUDPService()
	if err != nil {
		return []report.Finding{report.Failf("reserve UDP port: %v", err)}
	}
	port := s.port()
	if err := s.conn.Close(); err != nil {
		return []report.Finding{report.Failf("close UDP reservation: %v", err)}
	}
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-udp"}, map[string]string{"host_port": port, "container_port": "18432"})
	if err != nil {
		return []report.Finding{report.Failf("admit absent UDP service: %v", err)}
	}
	defer remove()
	res, err := e.Adapter.Exec(ctx, id, "kit-tck-host-port-udp", "18432", "single")
	if err != nil || res.ExitCode != 7 {
		return []report.Finding{report.Failf("absent UDP service must leave datagram unanswered: %+v, %v", res, err)}
	}
	address, err := net.ResolveUDPAddr("udp4", "127.0.0.1:"+port)
	if err != nil {
		return []report.Finding{report.Failf("late UDP address: %v", err)}
	}
	s.conn, err = net.ListenUDP("udp4", address)
	if err != nil {
		return []report.Finding{report.Failf("start late UDP service: %v", err)}
	}
	defer s.start()()
	return hostPortUDPProbe(ctx, e, id, "18432", "single", s)
}

func hostPortUDPSurface(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortUDPService()
	if err != nil {
		return []report.Finding{report.Failf("host UDP listener: %v", err)}
	}
	defer s.start()()
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-udp"}, map[string]string{"host_port": s.port(), "container_port": "18432"})
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("selection: %v", err)}
	}
	surface := state.Surface
	if len(surface.HostPorts) != 1 || surface.HostPorts[0] != s.port()+"->18432/udp/ipv4" || len(surface.Ports) != 0 || len(surface.Services) != 0 || len(surface.NetworkRuntimeAllow) != 0 || len(surface.NetworkInstallAllow) != 0 {
		return []report.Finding{report.Failf("UDP grant not separately surfaced: %+v", surface)}
	}
	return nil
}

var hostPortUDPChecks = []check{
	{requirement: "host-port@1/udp-datagrams", capability: capHostPort, run: hostPortUDPForward("datagrams")},
	{requirement: "host-port@1/udp-client-isolation", capability: capHostPort, run: hostPortUDPForward("clients")},
	{requirement: "host-port@1/udp-composition", capability: capHostPort, run: hostPortUDPComposition},
	{requirement: "host-port@1/udp-before-hooks", capability: capHostPort, needs: []string{capLifecycle}, run: hostPortUDPHooks},
	{requirement: "host-port@1/udp-no-extra-access", capability: capHostPort, run: hostPortUDPNoGrant},
	{requirement: "host-port@1/udp-on-demand", capability: capHostPort, run: hostPortUDPOnDemand},
	{requirement: "host-port@1/udp-permission-surface", capability: capHostPort, run: hostPortUDPSurface},
}

func init() { checks = append(checks, hostPortUDPChecks...) }
