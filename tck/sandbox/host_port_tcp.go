package sandbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const hostPortTCPRequestSize = 4096

// The host records the request independently of the guest observation and
// authenticates its response with a key that never enters the sandbox.
type hostPortTCPService struct {
	listener net.Listener
	key      [32]byte
	mu       sync.Mutex
	requests [][]byte
}

func newHostPortTCPService() (*hostPortTCPService, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &hostPortTCPService{listener: listener}
	if _, err := rand.Read(s.key[:]); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return s, nil
}

func (s *hostPortTCPService) port() string {
	return strconv.Itoa(s.listener.Addr().(*net.TCPAddr).Port)
}

func (s *hostPortTCPService) response(data []byte) []byte {
	mac := hmac.New(sha256.New, s.key[:])
	_, _ = mac.Write(data)
	return append(mac.Sum(nil), data...)
}

func (s *hostPortTCPService) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return
	}
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return
	}
	if binary.BigEndian.Uint32(header[:]) != hostPortTCPRequestSize {
		return
	}
	data := make([]byte, hostPortTCPRequestSize)
	if _, err := io.ReadFull(conn, data); err != nil {
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, data)
	s.mu.Unlock()
	response := s.response(data)
	binary.BigEndian.PutUint32(header[:], uint32(len(response)))
	// Copy handles short writes without treating TCP reads as message boundaries.
	_, _ = io.Copy(conn, bytes.NewReader(append(header[:], response...)))
}

func (s *hostPortTCPService) start() func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				return
			}
			s.serve(conn)
		}
	}()
	return func() { _ = s.listener.Close(); <-done }
}

func hostPortTCPVerify(out string, s *hostPortTCPService) error {
	var record struct{ Sent, Received []byte }
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		return fmt.Errorf("decode TCP observation: %w", err)
	}
	if len(record.Sent) != hostPortTCPRequestSize {
		return fmt.Errorf("TCP request size %d; want %d", len(record.Sent), hostPortTCPRequestSize)
	}
	// A complete byte alphabet makes this observably non-HTTP and binary.
	for i := 0; i < 256; i++ {
		if record.Sent[i] != byte(i) {
			return fmt.Errorf("TCP binary request prefix changed")
		}
	}
	s.mu.Lock()
	arrived := false
	for _, request := range s.requests {
		if bytes.Equal(request, record.Sent) {
			arrived = true
			break
		}
	}
	s.mu.Unlock()
	if !arrived {
		return fmt.Errorf("reported TCP request did not arrive at the host service")
	}
	if !bytes.Equal(record.Received, s.response(record.Sent)) {
		return fmt.Errorf("TCP host response bytes changed")
	}
	return nil
}

func hostPortTCPBytes(ctx context.Context, e *Env) []report.Finding {
	s, err := newHostPortTCPService()
	if err != nil {
		return []report.Finding{report.Failf("host TCP listener: %v", err)}
	}
	defer s.start()()
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-port"}, map[string]string{"host_port": s.port(), "container_port": "18432"})
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	out, failure := execOutput(ctx, e, id, "kit-tck-host-port-tcp", "18432")
	if failure != nil {
		return []report.Finding{*failure}
	}
	if err := hostPortTCPVerify(out, s); err != nil {
		return []report.Finding{report.Failf("%v", err)}
	}
	return nil
}

var hostPortTCPChecks = []check{
	{requirement: "host-port@1/tcp-bytes", capability: capHostPort, run: hostPortTCPBytes},
}

func init() { checks = append(checks, hostPortTCPChecks...) }
