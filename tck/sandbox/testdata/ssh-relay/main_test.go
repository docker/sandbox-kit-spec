package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestLoginKeyAlgorithms(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaPublic, err := ssh.NewPublicKey(&rsaKey.PublicKey)
	require.NoError(t, err)
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	edSigner, err := ssh.NewSignerFromKey(edKey)
	require.NoError(t, err)
	// Signing requires an explicit principal. Match the login below so the
	// certificate fixture represents the user whose algorithm is checked.
	cert := &ssh.Certificate{Key: rsaPublic, CertType: ssh.UserCert, ValidPrincipals: []string{"git"}}
	require.NoError(t, cert.SignCert(rand.Reader, edSigner))

	for _, tc := range []struct {
		key     ssh.PublicKey
		allowed []string
		denied  []string
	}{
		{rsaPublic, []string{ssh.KeyAlgoRSA, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512}, []string{ssh.KeyAlgoED25519, ssh.CertAlgoRSASHA256v01}},
		{cert, []string{ssh.CertAlgoRSAv01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSASHA512v01}, []string{ssh.KeyAlgoRSA, ssh.KeyAlgoRSASHA256}},
		{edSigner.PublicKey(), []string{ssh.KeyAlgoED25519}, []string{ssh.KeyAlgoRSA, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512}},
	} {
		for _, method := range []string{"publickey", "publickey-hostbound-v00@openssh.com"} {
			for _, algorithm := range append(append([]string{}, tc.allowed...), tc.denied...) {
				t.Run(tc.key.Type()+"/"+method+"/"+algorithm, func(t *testing.T) {
					data := ssh.Marshal(struct {
						SessionID []byte
						Message   byte
						User      string
						Service   string
						Method    string
						Signed    bool
						Algorithm string
						Key       []byte
					}{[]byte("session"), msgUserauthRequest, "git", "ssh-connection", method, true, algorithm, tc.key.Marshal()})
					if method != "publickey" {
						data = append(data, ssh.Marshal(struct{ HostKey []byte }{edSigner.PublicKey().Marshal()})...)
					}
					_, ok := parseLogin(data, tc.key.Marshal(), false)
					require.Equal(t, slices.Contains(tc.allowed, algorithm), ok)
					_, ok = parseLogin(data, []byte("another key"), false)
					require.False(t, ok, "algorithm compatibility must not bypass the outer key match")
				})
			}
		}
	}
}
