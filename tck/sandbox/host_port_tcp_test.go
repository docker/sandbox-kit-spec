package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHostPortTCPChecks(t *testing.T) {
	rep := runAgainstFake(t, "", hostPortTCPChecks...)
	require.False(t, rep.Failed(), "%s", rep)
}

func TestHostPortTCPGuestProbe(t *testing.T) {
	s, err := newHostPortTCPService()
	require.NoError(t, err)
	defer s.start()()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "python3", filepath.Join(FixtureDir, "workload", "kit-tck-host-port-tcp.py"), s.port()).CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.NoError(t, hostPortTCPVerify(string(out), s))
	var record struct{ Sent, Received []byte }
	require.NoError(t, json.Unmarshal(out, &record))
	record.Received[0] ^= 1
	altered, err := json.Marshal(record)
	require.NoError(t, err)
	require.ErrorContains(t, hostPortTCPVerify(string(altered), s), "response bytes changed")
	record.Sent[len(record.Sent)-1] ^= 1
	altered, err = json.Marshal(record)
	require.NoError(t, err)
	require.ErrorContains(t, hostPortTCPVerify(string(altered), s), "did not arrive")
}

func TestHostPortTCPVerificationRequiresHostObservation(t *testing.T) {
	s, err := newHostPortTCPService()
	require.NoError(t, err)
	defer func() { require.NoError(t, s.listener.Close()) }()
	data := make([]byte, hostPortTCPRequestSize)
	for i := 0; i < 256; i++ {
		data[i] = byte(i)
	}
	out, err := json.Marshal(struct{ Sent, Received []byte }{data, s.response(data)})
	require.NoError(t, err)
	require.ErrorContains(t, hostPortTCPVerify(string(out), s), "did not arrive")
	require.Error(t, hostPortTCPVerify(`{}`, s))
}

func TestHostPortTCPServiceHandlesFragmentedFrames(t *testing.T) {
	s, err := newHostPortTCPService()
	require.NoError(t, err)
	defer s.start()()
	conn, err := net.DialTimeout("tcp4", "127.0.0.1:"+s.port(), time.Second)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	data := bytes.Repeat([]byte{0, 255, 10, 128}, hostPortTCPRequestSize/4)
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	frame := append(header[:], data...)
	for len(frame) > 0 {
		n := min(7, len(frame))
		_, err := io.Copy(conn, bytes.NewReader(frame[:n]))
		require.NoError(t, err)
		frame = frame[n:]
	}
	_, err = io.ReadFull(conn, header[:])
	require.NoError(t, err)
	require.Equal(t, uint32(len(data)+32), binary.BigEndian.Uint32(header[:]))
	reply := make([]byte, len(data)+32)
	_, err = io.ReadFull(conn, reply)
	require.NoError(t, err)
	require.Equal(t, s.response(data), reply)
}

func TestHostPortTCPMutations(t *testing.T) {
	for _, mode := range []string{"host-port-tcp-no-forward", "host-port-tcp-corrupts-request", "host-port-tcp-corrupts-reply"} {
		t.Run(mode, func(t *testing.T) {
			rep := runAgainstFake(t, mode, hostPortTCPChecks...)
			require.True(t, rep.Failed(), "%s", rep)
		})
	}
}
