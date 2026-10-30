package sandbox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const capHostPort = spec.CapabilityHostPort

// A fresh response binds the guest observation to this suite-owned host
// service, rather than an unrelated listener or a canned proxy response.
type hostPortService struct {
	listener net.Listener
	server   *http.Server
	token    string
	requests atomic.Uint64
}

func newHostPortService() (*hostPortService, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := rand.Text()
	service := &hostPortService{listener: listener, token: token}
	service.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		service.requests.Add(1)
		_, _ = fmt.Fprint(w, token)
	})}
	return service, nil
}

func (s *hostPortService) port() string { return strconv.Itoa(s.listener.Addr().(*net.TCPAddr).Port) }

func (s *hostPortService) start() func() {
	done := make(chan error, 1)
	go func() { done <- s.server.Serve(s.listener) }()
	return func() { _ = s.server.Close(); <-done }
}

func hostPortArgs(s *hostPortService) map[string]string {
	return map[string]string{"host_port": s.port(), "container_port": "18432"}
}

func hostPortProbe(ctx context.Context, e *Env, id, port, token string) []report.Finding {
	out, failure := execOutput(ctx, e, id, "curl", "--noproxy", "*", "--silent", "--show-error", "--fail", "--max-time", "5", "http://127.0.0.1:"+port+"/")
	if failure != nil {
		return []report.Finding{*failure}
	}
	if out != token {
		return []report.Finding{report.Failf("host service response %q; want %q", out, token)}
	}
	return nil
}

func hostPortForwarded(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("host listener: %v", err)}
	}
	defer s.start()()
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port"}, hostPortArgs(s))
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	return hostPortProbe(ctx, e, id, "18432", s.token)
}

func hostPortHooks(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("host listener: %v", err)}
	}
	defer s.start()()
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-hooks"}, hostPortArgs(s))
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	for _, phase := range []string{"install", "startup"} {
		out, failure := execOutput(ctx, e, id, "cat", "/var/tmp/kit-tck-host-port-"+phase)
		if failure != nil {
			return []report.Finding{*failure}
		}
		if out != s.token {
			return []report.Finding{report.Failf("%s hook did not reach host service: %q", phase, out)}
		}
	}
	return hostPortProbe(ctx, e, id, "18432", s.token)
}

func hostPortComposition(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("host listener: %v", err)}
	}
	defer s.start()()
	other, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("second host listener: %v", err)}
	}
	defer other.start()()
	args := hostPortArgs(s)
	args["container_port"] = s.port()
	args["default_host_port"] = s.port()
	args["other_host_port"] = other.port()
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-default", "host-port", "host-port-alias"}, args)
	if err != nil {
		return []report.Finding{report.Failf("compose duplicate/default and distinct mappings: %v", err)}
	}
	defer remove()
	for _, probe := range []struct{ port, token string }{{s.port(), s.token}, {"18433", s.token}, {"18434", other.token}} {
		if findings := hostPortProbe(ctx, e, id, probe.port, probe.token); len(findings) > 0 {
			return findings
		}
	}
	return nil
}

func hostPortConflict(ctx context.Context, e *Env) []report.Finding {
	for _, kits := range [][]string{{fixtureWorkload, "host-port", "host-port-default"}, {fixtureWorkload, "host-port-default", "host-port"}} {
		_, remove, err := e.sandbox(ctx, kits, map[string]string{"host_port": "18432", "container_port": "18431", "default_host_port": "18431"})
		remove()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("conflicting host destinations must refuse: %v", err)}
		}
	}
	return nil
}

func hostPortNoGrant(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("host listener: %v", err)}
	}
	defer s.start()()
	_, remove, err := e.sandboxWith(ctx, []string{fixtureWorkload, "host-port-default"}, adapter.CreateOptions{Args: map[string]string{"default_host_port": s.port()}, RejectCapabilities: []string{capHostPort}})
	remove()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("rejected required mapping must refuse: %v", err)}
	}
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload}, nil)
	if err != nil {
		return []report.Finding{report.Failf("ungranted control create: %v", err)}
	}
	defer remove()
	if findings := hostPortUngranted(ctx, e, id, s); len(findings) > 0 {
		return findings
	}

	other, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("second host listener: %v", err)}
	}
	defer other.start()()
	mapped, removeMapped, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port-default"}, map[string]string{"default_host_port": s.port()})
	if err != nil {
		return []report.Finding{report.Failf("active mapping create: %v", err)}
	}
	defer removeMapped()
	if findings := hostPortProbe(ctx, e, mapped, s.port(), s.token); len(findings) > 0 {
		return findings
	}
	return hostPortUngranted(ctx, e, mapped, other)
}

func hostPortUngranted(ctx context.Context, e *Env, id string, service *hostPortService) []report.Finding {
	res, err := e.Adapter.Exec(ctx, id, "curl", "--noproxy", "*", "--silent", "--max-time", "5", "http://127.0.0.1:"+service.port()+"/")
	if err != nil {
		return []report.Finding{report.Failf("ungranted probe: %v", err)}
	}
	if res.ExitCode == 0 && res.Stdout == service.token {
		return []report.Finding{report.Failf("host service reachable without a grant: %q", res.Stdout)}
	}
	return hostPortNoRequests(ctx, &service.requests, "TCP")
}

// A discarded reply cannot undo host-side effects. Leave a bounded window
// for an outbound request still in flight when the guest probe returns.
func hostPortNoRequests(ctx context.Context, requests *atomic.Uint64, transport string) []report.Finding {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if requests.Load() != 0 {
			return []report.Finding{report.Failf("host %s service received an ungranted request", transport)}
		}
		select {
		case <-ctx.Done():
			return []report.Finding{report.Failf("observe ungranted %s traffic: %v", transport, ctx.Err())}
		case <-timer.C:
			if requests.Load() != 0 {
				return []report.Finding{report.Failf("host %s service received an ungranted request", transport)}
			}
			return nil
		case <-ticker.C:
		}
	}
}

func hostPortOnDemand(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("reserve host port: %v", err)}
	}
	port := s.port()
	if err := s.listener.Close(); err != nil {
		return []report.Finding{report.Failf("close reservation: %v", err)}
	}
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port"}, hostPortArgs(s))
	if err != nil {
		return []report.Finding{report.Failf("admit absent service: %v", err)}
	}
	defer remove()
	res, err := e.Adapter.Exec(ctx, id, "curl", "--noproxy", "*", "--silent", "--max-time", "5", "http://127.0.0.1:18432/")
	if err != nil || res.ExitCode == 0 {
		return []report.Finding{report.Failf("absent service must fail connection: %+v, %v", res, err)}
	}
	s.listener, err = net.Listen("tcp4", "127.0.0.1:"+port)
	if err != nil {
		return []report.Finding{report.Failf("start late service: %v", err)}
	}
	defer s.start()()
	return hostPortProbe(ctx, e, id, "18432", s.token)
}

func hostPortSurface(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("host listener: %v", err)}
	}
	defer s.start()()
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port"}, hostPortArgs(s))
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("selection: %v", err)}
	}
	surface := state.Surface
	if len(surface.HostPorts) != 1 || surface.HostPorts[0] != s.port()+"->18432/tcp/ipv4" || len(surface.Ports) != 0 || len(surface.Services) != 0 || len(surface.NetworkRuntimeAllow) != 0 || len(surface.NetworkInstallAllow) != 0 {
		return []report.Finding{report.Failf("host-service grant not separately surfaced: %+v", surface)}
	}
	return nil
}

var hostPortChecks = []check{
	{requirement: "host-port@1/forwarded", capability: capHostPort, run: hostPortForwarded},
	{requirement: "host-port@1/before-hooks", capability: capHostPort, needs: []string{capLifecycle}, run: hostPortHooks},
	{requirement: "host-port@1/composition", capability: capHostPort, run: hostPortComposition},
	{requirement: "host-port@1/conflict", capability: capHostPort, run: hostPortConflict},
	{requirement: "host-port@1/no-extra-access", capability: capHostPort, run: hostPortNoGrant},
	{requirement: "host-port@1/on-demand", capability: capHostPort, run: hostPortOnDemand},
	{requirement: "host-port@1/permission-surface", capability: capHostPort, run: hostPortSurface},
}

func init() { checks = append(checks, hostPortChecks...) }
