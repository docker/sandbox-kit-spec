package sandbox

import (
	"context"
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
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	capDockerAuth = "com.docker.sandbox/docker-auth@1"
	authProbe     = "kit-tck-docker-auth"
	authRegistry  = "registry.kit-tck.test"
	authIssuer    = "auth.kit-tck.test"
	authCDN       = "cdn.kit-tck.test"
)

// authService is the suite-owned registry, token issuer and content host. Its
// independent request trace, not probe output text, is what judges credential
// confinement: canaries are exact field values compared by equality, so no
// encoding of probe output can hide a leak from the checks.
type authService struct {
	server, plain                        *httptest.Server
	mu                                   sync.Mutex
	requests                             []authRequest
	tokens                               map[string]time.Time
	issuerMode                           string
	username, password, secret           string
	realmState, signature, marker, extra string
}

type authRequest struct {
	Host, Method, Path, Authorization string
	Query, Form                       url.Values
	FormError                         bool
	TLS, Authorized                   bool
}

func newAuthService() (*authService, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(random)
	s := &authService{tokens: map[string]time.Time{}, username: "user-" + nonce, password: "pat-" + nonce,
		secret: "token-" + nonce, realmState: "realm-" + nonce, signature: "signed-" + nonce, marker: "content-" + nonce, extra: "extra-" + nonce}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Kit TCK synthetic registry"},
		DNSNames: []string{authRegistry, authIssuer, authCDN}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	s.server = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	s.server.StartTLS()
	s.plain = httptest.NewServer(http.HandlerFunc(s.serve))
	return s, nil
}

func (s *authService) challenge() string {
	return fmt.Sprintf(`Bearer realm="https://%s/token?state=%s",service="kit-tck-registry"`, authIssuer, s.realmState)
}

func (s *authService) serve(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	formErr := r.ParseForm()
	credential, bearer := bearerOf(r.Header.Get("Authorization"), "Bearer")
	authorized := bearer && s.validBearer(credential, time.Now())
	s.mu.Lock()
	s.requests = append(s.requests, authRequest{r.Host, r.Method, r.URL.Path, r.Header.Get("Authorization"), r.URL.Query(), r.PostForm, formErr != nil, r.TLS != nil, authorized})
	s.mu.Unlock()
	switch {
	case r.Host == authRegistry && (r.URL.Path == "/v2/" || r.URL.Path == "/v2/unknown" || r.URL.Path == "/v2/replay"):
		if authorized {
			_, _ = fmt.Fprint(w, s.marker)
			return
		}
		challenge := s.challenge()
		if r.URL.Path == "/v2/replay" {
			challenge = strings.Replace(challenge, "/token?", "/token/replay?", 1)
		}
		w.Header().Set("WWW-Authenticate", challenge)
		w.WriteHeader(http.StatusUnauthorized)
	case r.Host == authIssuer && (r.URL.Path == "/token" || r.URL.Path == "/token/replay"):
		user, password, ok := r.BasicAuth()
		if !ok || user != s.username || password != s.password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("service") != "kit-tck-registry" || r.URL.Query().Get("state") != s.realmState {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// The armed scenario and the token's registration share one critical
		// section, so concurrent exchanges cannot collide or swap lifetimes.
		s.mu.Lock()
		mode := s.issuerMode
		lifetime := 1800
		if mode == "short" {
			lifetime = 2
		}
		token := fmt.Sprintf("%s-%d", s.secret, len(s.tokens)+1)
		if mode != "error" {
			s.tokens[token] = time.Now().Add(time.Duration(lifetime) * time.Second)
		}
		s.mu.Unlock()
		if mode == "error" {
			// The canary is the entire header and body value, so a mediated
			// error response is judged by equality, never by scanning.
			w.Header().Set("X-Secret", s.secret)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, s.secret)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_in": lifetime, "extra": s.extra})
	case r.Host == authRegistry && r.URL.Path == "/v2/blob":
		if !authorized {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := "/layer"
		if r.URL.Query().Get("deny") == "true" {
			path = "/blocked"
		}
		w.Header().Set("Location", "https://"+authCDN+path+"?signature="+s.signature)
		w.WriteHeader(http.StatusTemporaryRedirect)
	case r.Host == authRegistry && r.URL.Path == "/v2/uploads/" && r.Method == http.MethodPost:
		if !authorized {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Location", "/v2/uploads/session?state="+s.signature)
		w.WriteHeader(http.StatusAccepted)
	case r.Host == authRegistry && r.URL.Path == "/v2/uploads/session":
		if !authorized || r.Method != http.MethodPut || r.URL.Query().Get("state") != s.signature {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, s.marker)
	case r.Host == authCDN && r.URL.Path == "/layer" && r.URL.Query().Get("signature") == s.signature:
		_, _ = fmt.Fprint(w, s.marker)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// bearerOf splits an Authorization value, folding the scheme name because HTTP
// authentication schemes are case-insensitive while credentials are not.
func bearerOf(authorization, scheme string) (string, bool) {
	name, credential, ok := strings.Cut(authorization, " ")
	return strings.TrimLeft(credential, " "), ok && strings.EqualFold(name, scheme)
}

func (s *authService) validBearer(credential string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.tokens[credential]
	return ok && at.Before(expiry)
}

// issued reports whether a value equals, in whole, a credential this service
// created; leak judgments compare entire values, never substrings.
func (s *authService) issued(value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tokens[value]
	return ok || value == s.secret || value == s.username || value == s.password || value == "other-"+s.username || value == "other-"+s.password
}

// leaked walks decoded JSON and reports any string value that is an issued
// credential, so a placeholder in one field cannot excuse a leak in another.
func (s *authService) leaked(value any) bool {
	switch value := value.(type) {
	case string:
		if s.issued(value) || s.protectedBasic(value) {
			return true
		}
		if credential, ok := bearerOf(value, "Bearer"); ok && s.issued(credential) {
			return true
		}
		var decoded any
		return json.Unmarshal([]byte(value), &decoded) == nil && s.leaked(decoded)
	case map[string]any:
		if auth, ok := value["auth"].(string); ok && s.protectedBasic("Basic "+auth) {
			return true
		}
		for _, child := range value {
			if s.leaked(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if s.leaked(child) {
				return true
			}
		}
	}
	return false
}

func (s *authService) snapshot() []authRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authRequest(nil), s.requests...)
}

func (s *authService) reset() { s.mu.Lock(); s.requests = nil; s.mu.Unlock() }

// The scenario stays active for retries; probes need no nonstandard parameters.
func (s *authService) armIssuer(mode string) { s.mu.Lock(); s.issuerMode = mode; s.mu.Unlock() }

// checkRequests applies the credential-confinement invariants to every
// observation: the real login appears only on the admitted HTTPS token
// exchange and issued tokens only on the HTTPS registry API, so neither can
// reach the CDN or any other destination.
func (s *authService) protectedBasic(authorization string) bool {
	credential, ok := bearerOf(authorization, "Basic")
	decoded, err := base64.StdEncoding.DecodeString(credential)
	if !ok || err != nil {
		return false
	}
	user, password, _ := strings.Cut(string(decoded), ":")
	return s.issued(user) || s.issued(password)
}

func (s *authService) protectedLogin(r authRequest) bool {
	if s.protectedBasic(r.Authorization) {
		return true
	}
	for _, field := range []string{"refresh_token", "password", "username", "client_secret"} {
		for _, value := range r.Form[field] {
			if s.issued(value) {
				return true
			}
		}
	}
	return false
}

func (s *authService) checkRequests() error {
	for _, r := range s.snapshot() {
		if r.FormError {
			return fmt.Errorf("upstream form evidence was invalid or exceeded its size limit")
		}
		if s.protectedLogin(r) {
			if !r.TLS || r.Host != authIssuer || (r.Path != "/token" && r.Path != "/token/replay") || r.Query.Get("service") != "kit-tck-registry" || r.Query.Get("state") != s.realmState {
				return fmt.Errorf("real login sent outside the admitted authentication exchange")
			}
		}
		if credential, ok := bearerOf(r.Authorization, "Bearer"); ok && strings.HasPrefix(credential, s.secret) {
			if !r.TLS || r.Host != authRegistry || !strings.HasPrefix(r.Path, "/v2/") {
				return fmt.Errorf("issued token sent outside the admitted registry API")
			}
		}
	}
	return nil
}

// usedCredentials reports whether any real credential reached the service,
// regardless of what status the runtime reported to the guest.
func (s *authService) usedCredentials(paths ...string) bool {
	for _, r := range s.snapshot() {
		if len(paths) > 0 && !slices.Contains(paths, r.Path) {
			continue
		}
		if s.protectedLogin(r) {
			return true
		}
		if credential, ok := bearerOf(r.Authorization, "Bearer"); ok && strings.HasPrefix(credential, s.secret) {
			return true
		}
	}
	return false
}

// config writes the suite-owned adapter input: synthetic bindings, logical
// routes for the three test hosts, and the suite CA.
func (s *authService) config(dir, mode string) (string, error) {
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.server.Certificate().Raw}), 0600); err != nil {
		return "", err
	}
	bindings := map[string]any{authCDN: map[string]string{"username": "other-" + s.username, "password": "other-" + s.password}}
	if mode == "available" {
		bindings[authRegistry] = map[string]string{"username": s.username, "password": s.password}
	}
	address := strings.TrimPrefix(s.server.URL, "https://")
	raw, err := json.Marshal(map[string]any{"version": 1, "bindings": bindings,
		"routes":     map[string]string{authRegistry: address, authIssuer: address, authCDN: address},
		"httpRoutes": map[string]string{authRegistry: strings.TrimPrefix(s.plain.URL, "http://"), authIssuer: strings.TrimPrefix(s.plain.URL, "http://")}, "trust": ca})
	if err != nil {
		return "", err
	}
	file := filepath.Join(dir, "binding-"+mode+".json")
	return file, os.WriteFile(file, raw, 0600)
}

// Probe observations: every HTTP response the probe judges carries a status,
// so a zero status means the request never completed.
type authResponse struct {
	Status  int
	Body    string
	Headers map[string][]string
}

func ok2xx(r authResponse) bool { return r.Status >= 200 && r.Status < 300 }

type authLogin struct {
	Other    []string
	Config   any
	Pair     []string
	Token    string
	Exchange struct {
		Headers map[string][]string
		Body    string
	}
	Registry authResponse
}

type authSession struct {
	ctx     context.Context
	env     *Env
	service *authService
	dir, id string
	cleanup func()
}

func (s *authSession) create(binding string, fixtures ...string) (string, func(), error) {
	file, err := s.service.config(s.dir, binding)
	if err != nil {
		return "", func() {}, err
	}
	return s.env.sandboxWith(s.ctx, append([]string{fixtureWorkload}, fixtures...), adapter.CreateOptions{DockerAuthConfig: file, Env: map[string]string{"KIT_TCK_AUTH_CHALLENGE": s.service.challenge()}})
}

func (s *authSession) observe(id, mode string, out any, args ...string) error {
	result, err := s.env.Adapter.Exec(s.ctx, id, append([]string{authProbe, mode}, args...)...)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s observation exited %d: %s", mode, result.ExitCode, result.Stderr)
	}
	if err := json.Unmarshal([]byte(result.Stdout), out); err != nil {
		return fmt.Errorf("%s observation: %w", mode, err)
	}
	return s.service.checkRequests()
}

func (s *authSession) login(id string) (authLogin, error) {
	var login authLogin
	if err := s.observe(id, "login", &login); err != nil {
		return login, err
	}
	if len(login.Other) != 0 {
		return login, fmt.Errorf("unrequested registry credentials are resolvable")
	}
	if login.Registry.Status != 200 || login.Registry.Body != s.service.marker {
		return login, fmt.Errorf("placeholder login did not reach authenticated content")
	}
	return login, nil
}

func authCheck(name string, run func(*authSession) error) check {
	c := check{requirement: "docker-auth@1/" + name, capability: capDockerAuth, needs: []string{capNetworkPolicyV2}}
	if name == "lifetime" {
		c.needs = append(c.needs, capLifecycle)
	}
	c.run = func(ctx context.Context, e *Env) []report.Finding {
		service, err := newAuthService()
		if err != nil {
			return []report.Finding{report.Failf("start registry: %v", err)}
		}
		defer service.server.Close()
		defer service.plain.Close()
		dir, err := os.MkdirTemp("", "kit-tck-registry-*")
		if err != nil {
			return []report.Finding{report.Failf("registry binding: %v", err)}
		}
		defer func() { _ = os.RemoveAll(dir) }()
		s := &authSession{ctx: ctx, env: e, service: service, dir: dir}
		fixtures := []string{"docker-auth", "docker-auth-network"}
		if name == "lifetime" {
			fixtures = append(fixtures, "docker-auth-hooks")
		}
		s.id, s.cleanup, err = s.create("available", fixtures...)
		if err != nil {
			return []report.Finding{report.Failf("create registry sandbox: %v", err)}
		}
		defer func() {
			if s.cleanup != nil {
				s.cleanup()
			}
		}()
		if err := run(s); err != nil {
			return []report.Finding{report.Failf("%v", err)}
		}
		return nil
	}
	return c
}

func authHostResolution(s *authSession) error {
	if _, err := s.login(s.id); err != nil {
		return err
	}
	if err := s.env.Adapter.Remove(s.ctx, s.id); err != nil {
		return err
	}
	s.cleanup = func() {}
	id, remove, err := s.create("missing", "docker-auth", "docker-auth-network")
	if err == nil {
		remove()
		return fmt.Errorf("unavailable required binding accepted: %s", id)
	}
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return fmt.Errorf("unavailable binding was not a capability refusal: %w", err)
	}
	s.service.reset()
	id, remove, err = s.create("missing", "docker-auth-optional", "docker-auth-network")
	if err != nil {
		return err
	}
	defer remove()
	var ambient struct {
		Resolved bool
		Request  authResponse
	}
	if err := s.observe(id, "ambient", &ambient); err != nil {
		return err
	}
	selection, err := s.env.Adapter.Selection(s.ctx, id)
	if err != nil {
		return err
	}
	skipped := 0
	for _, r := range selection.Selection.Skipped {
		if r.Source != nil && (r.Source.Kit == "docker-auth-optional" || r.Source.Kit == s.env.Fixtures("docker-auth-optional")) {
			skipped++
		}
	}
	if skipped != 1 || ambient.Resolved || ambient.Request.Body == s.service.marker || s.service.usedCredentials() {
		return fmt.Errorf("unavailable optional binding was not skipped without authentication")
	}
	return nil
}

// authRequestScope sends the placeholder login toward destinations outside the
// admitted exchange; checkRequests judges what actually reached the service.
func authRequestScope(s *authSession) error {
	if _, err := s.login(s.id); err != nil {
		return err
	}
	var statuses []int
	if err := s.observe(s.id, "scope", &statuses); err != nil {
		return err
	}
	if len(statuses) != 4 {
		return fmt.Errorf("expected 4 off-scope login observations, got %d", len(statuses))
	}
	for _, status := range statuses {
		if status >= 200 && status < 300 {
			return fmt.Errorf("off-scope login request succeeded")
		}
	}
	return nil
}

func authHostOnly(s *authSession) error {
	login, err := s.login(s.id)
	if err != nil {
		return err
	}
	if len(login.Pair) != 2 || login.Pair[0] == "" || login.Pair[1] == "" {
		return fmt.Errorf("usable placeholder login missing")
	}
	return s.service.confidential(login)
}

func (s *authService) confidential(login authLogin) error {
	if login.Token == "" || len(login.Pair) != 2 || len(login.Other) != 0 {
		return fmt.Errorf("incomplete login observation")
	}
	if login.Config == nil || s.leaked(login.Config) {
		return fmt.Errorf("missing or credential-bearing Docker config")
	}
	if !json.Valid([]byte(login.Exchange.Body)) {
		return fmt.Errorf("missing or invalid token exchange body")
	}
	for _, value := range append(login.Pair, login.Token, login.Exchange.Body) {
		if s.leaked(value) {
			return fmt.Errorf("client-visible login exposed a real credential")
		}
	}
	for _, values := range login.Exchange.Headers {
		for _, value := range values {
			if s.leaked(value) {
				return fmt.Errorf("token exchange header exposed a real credential")
			}
		}
	}
	return nil
}

// Negative exchanges get their own registry, issuer and sandbox so valid cached
// authorization from a positive control cannot mask or invalidate the scenario.
func (s *authSession) isolatedIssuer(mode string, run func(*authSession) error) error {
	service, err := newAuthService()
	if err != nil {
		return err
	}
	defer service.server.Close()
	defer service.plain.Close()
	service.armIssuer(mode)
	n := &authSession{ctx: s.ctx, env: s.env, service: service, dir: s.dir}
	n.id, n.cleanup, err = n.create("available", "docker-auth", "docker-auth-network")
	if err != nil {
		var refused *adapter.RefusedError
		if mode == "error" && errors.As(err, &refused) {
			return service.failedExchange()
		}
		return err
	}
	defer n.cleanup()
	return run(n)
}

func authTokenMediation(s *authSession) error {
	login, err := s.login(s.id)
	if err != nil {
		return err
	}
	if err := s.service.confidential(login); err != nil {
		return err
	}
	return s.isolatedIssuer("error", authIssuerFailure)
}

func authIssuerFailure(s *authSession) error {
	var failure struct {
		Status  int
		Headers map[string][]string
		Body    string
	}
	if err := s.observe(s.id, "error", &failure); err != nil {
		return err
	}
	if failure.Status >= 200 && failure.Status < 300 {
		return fmt.Errorf("failed token exchange was not observed as a failure")
	}
	if err := s.service.failedExchange(); err != nil {
		return err
	}
	if s.service.leaked(failure.Body) {
		return fmt.Errorf("issuer error body exposed the real token")
	}
	for _, values := range failure.Headers {
		for _, value := range values {
			if s.service.leaked(value) {
				return fmt.Errorf("issuer error header exposed the real token")
			}
		}
	}
	return nil
}

// Connection refusal and eager capability refusal are both fail-closed;
// the trace must establish that the intended issuer failure was exercised.
func (s *authService) failedExchange() error {
	if err := s.checkRequests(); err != nil {
		return err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(s.username + ":" + s.password))
	for _, r := range s.snapshot() {
		credential, ok := bearerOf(r.Authorization, "Basic")
		if ok && credential == basic && r.Host == authIssuer && r.Path == "/token" {
			return nil
		}
	}
	return fmt.Errorf("failing token exchange never reached the issuer")
}

func authBearerScope(s *authSession) error {
	// The off-API and cross-host requests carry a known placeholder; the
	// shared trace invariants judge where the issued token actually went.
	var observation struct{ Valid, NonAPI, Cross authResponse }
	if err := s.observe(s.id, "bearer", &observation); err != nil {
		return err
	}
	if observation.Valid.Status != 200 || observation.Valid.Body != s.service.marker {
		return fmt.Errorf("placeholder bearer substitution failed")
	}
	s.service.reset()
	var unknown authResponse
	if err := s.observe(s.id, "unknown", &unknown); err != nil {
		return err
	}
	if ok2xx(unknown) || s.service.usedCredentials("/v2/unknown") {
		return fmt.Errorf("unknown bearer placeholder authorized registry access")
	}
	return s.isolatedIssuer("short", authExpiredBearer)
}

func authExpiredBearer(s *authSession) error {
	var token string
	if err := s.observe(s.id, "short", &token); err != nil {
		return err
	}
	timer := time.NewTimer(2500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-timer.C:
	}
	s.service.reset()
	var expired authResponse
	if err := s.observe(s.id, "expired", &expired, token); err != nil {
		return err
	}
	tokens := 0
	fresh := false
	s.service.mu.Lock()
	tokens = len(s.service.tokens)
	s.service.mu.Unlock()
	for _, r := range s.service.snapshot() {
		fresh = fresh || r.Host == authRegistry && r.Path == "/v2/" && r.Authorized
	}
	if tokens == 0 {
		return fmt.Errorf("short-lived issuer authorization was never exercised")
	}
	if ok2xx(expired) && (!fresh || expired.Body != s.service.marker) {
		return fmt.Errorf("expired placeholder authorized without fresh issuer authorization")
	}
	return nil
}

// authBlob is one signed-download observation: either directly delivered
// content (host-side following) or an opaque client-visible redirect.
type authBlob struct {
	Status   int
	Body     string
	Location string
	Headers  map[string][]string
	Followed *authResponse
}

// delivered reports whether the observation yielded the expected content,
// through either conforming shape; exposed reports a signed value leak in
// the client-visible location's decoded query.
func (b authBlob) delivered(marker string) bool {
	return b.Status/100 == 2 && b.Body == marker || b.Followed != nil && b.Followed.Status/100 == 2 && b.Followed.Body == marker
}

func (s *authService) blobExposed(b authBlob) bool {
	values := []string{b.Location, b.Body}
	headers := []map[string][]string{b.Headers}
	if b.Followed != nil {
		values = append(values, b.Followed.Body)
		headers = append(headers, b.Followed.Headers)
	}
	for _, headers := range headers {
		for _, entries := range headers {
			values = append(values, entries...)
		}
	}
	for _, value := range values {
		if s.leaked(value) || authLocationExposed(value, s.signature, 0) {
			return true
		}
	}
	return false
}

func authLocationExposed(value, signature string, depth int) bool {
	if value == signature || depth > 32 {
		return true
	}
	location, err := url.Parse(value)
	if err != nil {
		return false
	}
	for _, values := range location.Query() {
		for _, child := range values {
			if authLocationExposed(child, signature, depth+1) {
				return true
			}
		}
	}
	return false
}

func authRedirectConfinement(s *authSession) error {
	if _, err := s.login(s.id); err != nil {
		return err
	}
	var observation struct{ Allowed, Denied, Upload authBlob }
	if err := s.observe(s.id, "redirect", &observation); err != nil {
		return err
	}
	for name, blob := range map[string]authBlob{"download": observation.Allowed, "denied": observation.Denied, "upload": observation.Upload} {
		if s.service.blobExposed(blob) {
			return fmt.Errorf("signed %s location exposed to the client", name)
		}
	}
	if !observation.Allowed.delivered(s.service.marker) {
		return fmt.Errorf("allowed signed download was not usable")
	}
	if !observation.Upload.delivered(s.service.marker) {
		return fmt.Errorf("signed upload sequence was not usable")
	}
	if observation.Denied.delivered(s.service.marker) {
		return fmt.Errorf("policy-denied signed download delivered content")
	}
	cdn, upload := false, false
	for _, r := range s.service.snapshot() {
		if r.Host == authCDN && r.Path == "/blocked" && r.Method == http.MethodGet {
			return fmt.Errorf("resolved redirect bypassed network policy")
		}
		cdn = cdn || r.Host == authCDN && r.Query.Get("signature") == s.service.signature
		upload = upload || r.Host == authRegistry && r.Path == "/v2/uploads/session" && r.Query.Get("state") == s.service.signature
	}
	if !cdn || !upload {
		return fmt.Errorf("signed download or upload never reached its destination")
	}
	return nil
}

func authLifetime(s *authSession) error {
	var hooks map[string]authLogin
	if err := s.observe(s.id, "hooks", &hooks); err != nil {
		return err
	}
	for _, phase := range []string{"install", "startup"} {
		if err := s.service.confidential(hooks[phase]); err != nil {
			return fmt.Errorf("%s hook: %w", phase, err)
		}
		if hooks[phase].Registry.Status != 200 || hooks[phase].Registry.Body != s.service.marker {
			return fmt.Errorf("authentication unavailable during %s hook", phase)
		}
	}
	saved, err := s.login(s.id)
	if err != nil {
		return err
	}
	if err := s.env.Adapter.Remove(s.ctx, s.id); err != nil {
		return err
	}
	s.cleanup = func() {}
	if s.id, s.cleanup, err = s.create("available", "docker-auth", "docker-auth-network"); err != nil {
		return err
	}
	var endpoint string
	if err := s.observe(s.id, "discover", &endpoint); err != nil {
		return err
	}
	s.service.reset()
	var replay struct{ Bearer, Login authResponse }
	if err := s.observe(s.id, "replay", &replay, saved.Token, saved.Pair[0], saved.Pair[1], endpoint); err != nil {
		return err
	}
	if ok2xx(replay.Bearer) || ok2xx(replay.Login) {
		return fmt.Errorf("ended run's placeholder accepted in a new run")
	}
	if s.service.usedCredentials("/v2/replay", "/token/replay") {
		return fmt.Errorf("ended run's placeholder still reached upstream as real credentials")
	}
	return nil
}

type authAmbient struct {
	Resolved, Socket, Engine, Daemon, Endpoint bool
	Request                                    authResponse
}

func authNoExtraAccess(s *authSession) error {
	var ambient authAmbient
	if err := s.observe(s.id, "ambient", &ambient); err != nil {
		return err
	}
	if err := s.env.Adapter.Remove(s.ctx, s.id); err != nil {
		return err
	}
	s.cleanup = func() {}
	s.service.reset()
	id, remove, err := s.create("available", "docker-auth")
	if err != nil {
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return err
		}
	} else {
		var authenticated any
		observeErr := s.observe(id, "no-egress", &authenticated)
		remove()
		if observeErr != nil {
			return observeErr
		}
	}
	if len(s.service.snapshot()) != 0 {
		return fmt.Errorf("authentication granted egress without a network policy")
	}
	id, remove, err = s.create("available", "docker-auth-absent")
	if err != nil {
		return err
	}
	var baseline authAmbient
	observeErr := s.observe(id, "ambient", &baseline)
	remove()
	if observeErr != nil {
		return observeErr
	}
	if ambient.Socket && !baseline.Socket || ambient.Engine && !baseline.Engine || ambient.Daemon && !baseline.Daemon || ambient.Endpoint && !baseline.Endpoint {
		return fmt.Errorf("authentication supplied a Docker socket or engine")
	}
	if baseline.Resolved || baseline.Request.Body == s.service.marker || s.service.usedCredentials() {
		return fmt.Errorf("authentication supplied without a grant")
	}
	return nil
}

func init() {
	checks = append(checks,
		authCheck("host-resolution", authHostResolution),
		authCheck("request-scope", authRequestScope),
		authCheck("host-only", authHostOnly),
		authCheck("token-mediation", authTokenMediation),
		authCheck("bearer-scope", authBearerScope),
		authCheck("redirect-confinement", authRedirectConfinement),
		authCheck("lifetime", authLifetime),
		authCheck("no-extra-access", authNoExtraAccess),
	)
}
