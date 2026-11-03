package sandbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	capDockerAuth     = "com.docker.sandbox/docker-auth@1"
	authRegistry      = "registry.kit-tck.test"
	authOtherRegistry = "other-registry.kit-tck.test"
	authIssuer        = "auth.kit-tck.test"
	authCDN           = "cdn.kit-tck.test"
	authDenied        = "denied.kit-tck.test"
)

type authRequest struct {
	Host, Method, Path, Authorization, Query, Scheme string
	At                                               time.Time
}
type authService struct {
	server                                                 *httptest.Server
	plain                                                  *httptest.Server
	untrusted                                              *httptest.Server
	mu                                                     sync.Mutex
	requests                                               []authRequest
	tokens                                                 map[string]time.Time
	username, password, bearer, refresh, signature, marker string
}

func newAuthService() (*authService, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(random)
	s := &authService{tokens: map[string]time.Time{}, username: "user-" + nonce, password: "pat-" + nonce, bearer: "bearer-" + nonce, refresh: "refresh-" + nonce, signature: "signature-" + nonce, marker: "content-" + nonce}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Kit TCK synthetic registry"}, DNSNames: []string{authRegistry, authOtherRegistry, authIssuer, authCDN, authDenied}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	s.server = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	s.server.StartTLS()
	s.plain = httptest.NewServer(http.HandlerFunc(s.serve))
	// Its certificate is outside the supplied trust anchor. Recording requests
	// under a distinct host makes credentials accepted there a scope violation.
	s.untrusted = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "untrusted." + r.Host
		s.serve(w, r)
	}))
	s.untrusted.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.untrusted.StartTLS()
	return s, nil
}

func authCredential(authorization, scheme string) (string, bool) {
	name, credential, ok := strings.Cut(authorization, " ")
	return strings.TrimLeft(credential, " "), ok && strings.EqualFold(name, scheme)
}

func (s *authService) validBearer(authorization string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, bearer := authCredential(authorization, "Bearer")
	expires, ok := s.tokens[credential]
	return bearer && ok && at.Before(expires)
}

func (s *authService) latestToken() (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest string
	var expiry time.Time
	for token, expires := range s.tokens {
		if expires.After(expiry) {
			latest, expiry = token, expires
		}
	}
	return latest, expiry
}

func (s *authService) snapshot() []authRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authRequest(nil), s.requests...)
}
func (s *authService) reset() { s.mu.Lock(); s.requests = nil; s.mu.Unlock() }
func (s *authService) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, authRequest{r.Host, r.Method, r.URL.Path, r.Header.Get("Authorization"), r.URL.RawQuery, func() string {
		if r.TLS == nil {
			return "http"
		}
		return "https"
	}(), time.Now()})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Host == authRegistry && r.URL.Path == "/v2/":
		if s.validBearer(r.Header.Get("Authorization"), time.Now()) {
			_, _ = fmt.Fprint(w, s.marker)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+authIssuer+`/token",service="kit-tck-registry"`)
		w.WriteHeader(401)
	case r.Host == authIssuer && r.URL.Path == "/token":
		user, password, ok := r.BasicAuth()
		if !ok || user != s.username || password != s.password {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("service") != "kit-tck-registry" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Query().Get("case") {
		case "error":
			w.Header().Add("X-Secret", s.bearer)
			w.Header().Add("X-Secret", "redacted")
			w.WriteHeader(400)
			_, _ = fmt.Fprint(w, s.bearer)
		case "redirect":
			w.Header().Set("Location", "https://"+authDenied+"/leak?token="+s.bearer)
			w.WriteHeader(302)
		case "malformed":
			_, _ = fmt.Fprintf(w, `{"token":%q,"token":"ambiguous"}`, s.bearer)
		default:
			w.Header().Add("X-Secret", s.refresh)
			w.Header().Add("X-Secret", "redacted")
			s.mu.Lock()
			token := fmt.Sprintf("%s-%d", s.bearer, len(s.tokens)+1)
			lifetime := int64(1800)
			if r.URL.Query().Get("case") == "short" {
				lifetime = 2
			}
			s.tokens[token] = time.Now().Add(time.Duration(lifetime) * time.Second)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"token": token, "access_token": token, "refresh_token": s.refresh, "expires_in": lifetime, "extra": s.password})
		}
	case r.Host == authRegistry && r.URL.Path == "/v2/blob":
		if !s.validBearer(r.Header.Get("Authorization"), time.Now()) {
			w.WriteHeader(401)
			return
		}
		host := authCDN
		if r.URL.Query().Get("deny") == "true" {
			host = authDenied
		}
		path := "/layer"
		if r.URL.Query().Get("path-deny") == "true" {
			path = "/blocked"
		}
		w.Header().Set("Location", "https://"+host+path+"?signature="+s.signature)
		w.WriteHeader(307)
	case r.Host == authRegistry && r.URL.Path == "/v2/uploads/" && r.Method == http.MethodPost:
		if !s.validBearer(r.Header.Get("Authorization"), time.Now()) {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Location", "/v2/uploads/session?state="+s.signature)
		w.WriteHeader(http.StatusAccepted)
	case r.Host == authRegistry && r.URL.Path == "/v2/uploads/session":
		if !s.validBearer(r.Header.Get("Authorization"), time.Now()) || r.URL.Query().Get("state") != s.signature {
			w.WriteHeader(401)
			return
		}
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, s.marker)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case r.Host == authCDN && r.URL.Path == "/layer" && r.URL.Query().Get("signature") == s.signature:
		_, _ = fmt.Fprint(w, s.marker)
	default:
		_, _ = fmt.Fprint(w, "unrelated")
	}
}

// This file is adapter input, not a Docker config or a guest fixture. Adapters
// translate bindings and route names through their normal runtime facilities.
func (s *authService) config(dir, mode string) (string, error) {
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.server.Certificate().Raw}), 0600); err != nil {
		return "", err
	}
	bindings := map[string]any{authOtherRegistry: map[string]string{"username": "other-" + s.username, "password": "other-" + s.password}}
	if mode != "missing" {
		bindings[authRegistry] = map[string]string{"username": s.username, "password": s.password}
	}
	routes := map[string]string{}
	for _, host := range []string{authRegistry, authOtherRegistry, authIssuer, authCDN, authDenied} {
		routes[host] = strings.TrimPrefix(s.server.URL, "https://")
	}
	if mode == "untrusted" {
		routes[authIssuer] = strings.TrimPrefix(s.untrusted.URL, "https://")
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "bindings": bindings, "routes": routes, "httpRoutes": map[string]string{authRegistry: strings.TrimPrefix(s.plain.URL, "http://"), authIssuer: strings.TrimPrefix(s.plain.URL, "http://")}, "trust": ca})
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, "binding-"+mode+".json")
	return file, os.WriteFile(file, raw, 0600)
}

// Apply the same security invariants to every upstream observation rather than
// relying on individual scenarios to remember each credential and policy bound.
func (s *authService) checkRequests() error {
	basic := base64.StdEncoding.EncodeToString([]byte(s.username + ":" + s.password))
	for _, r := range s.snapshot() {
		if r.Host == authDenied || r.Host == authCDN && r.Path == "/blocked" || r.Host == authRegistry && r.Method == "DELETE" || r.Host == authIssuer && r.Method == "POST" {
			return fmt.Errorf("policy-denied request reached upstream")
		}
		query, err := url.ParseQuery(r.Query)
		if err != nil {
			return err
		}
		if credential, ok := authCredential(r.Authorization, "Basic"); ok && credential == basic && (r.Scheme != "https" || r.Host != authIssuer || r.Path != "/token" || query.Get("service") != "kit-tck-registry") {
			return fmt.Errorf("login sent outside admitted authentication exchange")
		}
		if credential, ok := authCredential(r.Authorization, "Bearer"); ok && strings.HasPrefix(credential, s.bearer) && (r.Scheme != "https" || r.Host != authRegistry || !strings.HasPrefix(r.Path, "/v2/") || !s.validBearer(r.Authorization, r.At)) {
			return fmt.Errorf("bearer sent outside valid registry authorization")
		}
		if r.Host == authCDN && r.Authorization != "" {
			return fmt.Errorf("registry authorization forwarded to CDN")
		}
	}
	return nil
}

func (s *authService) containsSecret(text string) bool {
	return s.secretJSON(text, 0)
}

// Real credentials reaching any upstream show that a placeholder was honored,
// independent of the status a runtime chose to report to the guest.
func (s *authService) usedCredentials() bool {
	basic := base64.StdEncoding.EncodeToString([]byte(s.username + ":" + s.password))
	for _, r := range s.snapshot() {
		if credential, ok := authCredential(r.Authorization, "Basic"); ok && credential == basic {
			return true
		}
		if credential, ok := authCredential(r.Authorization, "Bearer"); ok && strings.HasPrefix(credential, s.bearer) {
			return true
		}
	}
	return false
}

var percentEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

// Observation strings can themselves contain JSON bodies. Decode both layers
// so JSON escaping cannot hide credentials that clients can recover.
func (s *authService) secretJSON(text string, depth int) bool {
	if depth > 64 || s.secretText(text) {
		return true
	}
	// Clients and the fixture decode URL escapes. Decode only well-formed
	// escapes so one stray percent sign cannot hide the rest of the evidence.
	if decoded := percentEscape.ReplaceAllStringFunc(text, func(escape string) string {
		value, _ := hex.DecodeString(escape[1:])
		return string(value)
	}); decoded != text && s.secretJSON(decoded, depth+1) {
		return true
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		// Token traversal retains duplicate fields and evidence preceding a
		// malformed suffix; map decoding would silently discard both.
		if value, ok := token.(string); ok && s.secretJSON(value, depth+1) {
			return true
		}
	}
}

func (s *authService) secretText(text string) bool {
	for _, secret := range []string{s.username, s.password, s.bearer, s.refresh, s.signature, base64.StdEncoding.EncodeToString([]byte(s.username + ":" + s.password)), base64.StdEncoding.EncodeToString([]byte("other-" + s.username + ":other-" + s.password))} {
		if strings.Contains(text, secret) {
			return true
		}
	}
	return false
}
