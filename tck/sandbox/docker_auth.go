package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const authProbe = "kit-tck-docker-auth"

// The probe's HTTP observations are [status, headers, body]. Decode that wire
// format once so checks judge fields rather than serialized JSON fragments.
type authResponse struct {
	Status int
	Header map[string][]string
	Body   string
}

func (r *authResponse) UnmarshalJSON(raw []byte) error {
	var fields []json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields) != 3 {
		return fmt.Errorf("HTTP observation requires status, headers and body")
	}
	for i, out := range []any{&r.Status, &r.Header, &r.Body} {
		if err := json.Unmarshal(fields[i], out); err != nil {
			return err
		}
	}
	return nil
}

// Every request, including discovery and rejected exchanges, uses this same
// lossless record. Scenario results select behavior; responses preserve evidence.
type authExchange struct {
	URL, Method string
	Status      int
	Headers     map[string][]string
	Body        string
}

type authLogin struct{ Registry authResponse }
type authAmbient struct {
	Socket, Engine bool
	Authentication *authLogin
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
	return s.env.sandboxWith(s.ctx, append([]string{fixtureWorkload}, fixtures...), adapter.CreateOptions{DockerAuthConfig: file})
}
func (s *authSession) observe(id, mode string, out any, args ...string) error {
	result, err := s.env.Adapter.Exec(s.ctx, id, append([]string{authProbe, mode}, args...)...)
	if err != nil {
		return err
	}
	if s.service.containsSecret(result.Stdout) || s.service.containsSecret(result.Stderr) {
		return fmt.Errorf("%s observation exposed real credentials", mode)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s observation exited %d", mode, result.ExitCode)
	}
	var observation struct {
		Result    json.RawMessage
		Responses []authExchange
	}
	if err := json.Unmarshal([]byte(result.Stdout), &observation); err != nil || len(observation.Result) == 0 || string(observation.Result) == "null" || observation.Responses == nil {
		return fmt.Errorf("%s: invalid probe observation envelope", mode)
	}
	if err := s.service.checkRequests(); err != nil {
		return err
	}
	if err := requireObserved(observation.Result, reflect.TypeOf(out)); err != nil {
		return fmt.Errorf("%s observation: %w", mode, err)
	}
	if err := json.Unmarshal(observation.Result, out); err != nil {
		return fmt.Errorf("%s observation: %w", mode, err)
	}
	return nil
}

// Absent fields would decode as zero values and could satisfy negative
// assertions, so every field a check reads must have been observed.
func requireObserved(raw json.RawMessage, t reflect.Type) error {
	if t.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return nil
		}
		return requireObserved(raw, t.Elem())
	}
	var items []json.RawMessage
	switch {
	case t == reflect.TypeFor[authResponse]():
		return nil
	case t.Kind() == reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for i := range t.NumField() {
			field := t.Field(i)
			value, ok := foldedField(fields, field.Name)
			if !ok {
				return fmt.Errorf("missing %s", field.Name)
			}
			if err := requireObserved(value, field.Type); err != nil {
				return err
			}
		}
		return nil
	case t.Kind() == reflect.Slice:
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
	case t.Kind() == reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			items = append(items, value)
		}
	default:
		return nil
	}
	for _, item := range items {
		if err := requireObserved(item, t.Elem()); err != nil {
			return err
		}
	}
	return nil
}

// encoding/json matches field names case-insensitively; required checks must agree.
func foldedField(fields map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	for key, value := range fields {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

func authorized(status int) bool { return status >= 200 && status < 300 }

func (s *authSession) login() error {
	var login authLogin
	if err := s.observe(s.id, "login", &login); err != nil {
		return err
	}
	if login.Registry.Status != 200 || login.Registry.Body != s.service.marker {
		return fmt.Errorf("bound registry login did not reach authenticated content")
	}
	return nil
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
		defer service.untrusted.Close()
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
	if err := s.login(); err != nil {
		return err
	}
	id, remove, err := s.create("missing", "docker-auth")
	if err == nil {
		remove()
		return fmt.Errorf("unavailable registry binding accepted as required: %s", id)
	}
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return fmt.Errorf("unavailable binding was not a capability refusal: %w", err)
	}
	id, remove, err = s.create("missing", "docker-auth-optional", "docker-auth-network")
	if err != nil {
		return err
	}
	var ambient authAmbient
	observationErr := s.observe(id, "ambient", &ambient)
	selection, selectionErr := s.env.Adapter.Selection(s.ctx, id)
	remove()
	if err := errors.Join(observationErr, selectionErr); err != nil {
		return err
	}
	skipped := 0
	for _, r := range selection.Selection.Skipped {
		if r.Source != nil && (r.Source.Kit == "docker-auth-optional" || r.Source.Kit == s.env.Fixtures("docker-auth-optional")) {
			skipped++
		}
	}
	if skipped != 1 || ambient.Authentication != nil {
		return fmt.Errorf("unavailable optional binding was not skipped without authentication")
	}
	return nil
}

func authRequestScope(s *authSession) error {
	if err := s.login(); err != nil {
		return err
	}
	s.service.reset()
	var responses []authResponse
	if err := s.observe(s.id, "scope", &responses); err != nil {
		return err
	}
	// The issuer route presents a certificate outside the supplied trust, so a
	// runtime that skips verification delivers the real login there.
	id, remove, err := s.create("untrusted", "docker-auth", "docker-auth-network")
	if err != nil {
		return err
	}
	defer remove()
	var untrusted map[string]authResponse
	return s.observe(id, "mediation", &untrusted)
}

func authTokenMediation(s *authSession) error {
	var responses map[string]authResponse
	if err := s.observe(s.id, "mediation", &responses); err != nil {
		return err
	}
	if len(responses) != 4 || responses[""].Status != 200 {
		return fmt.Errorf("valid token exchange missing")
	}
	for _, name := range []string{"error", "redirect", "malformed"} {
		if _, ok := responses[name]; !ok {
			return fmt.Errorf("missing %s token response observation", name)
		}
	}
	return nil
}

func authBearerScope(s *authSession) error {
	var observation struct {
		Valid, Unknown authResponse
	}
	if err := s.observe(s.id, "bearer", &observation); err != nil {
		return err
	}
	if authorized(observation.Unknown.Status) {
		return fmt.Errorf("unknown bearer placeholder authorized registry access")
	}
	if observation.Valid.Body != s.service.marker {
		return fmt.Errorf("bearer substitution failed")
	}
	var saved struct{ Token string }
	if err := s.observe(s.id, "save-short", &saved); err != nil {
		return err
	}
	timer := time.NewTimer(2200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-timer.C:
	}
	s.service.reset()
	var response authResponse
	if err := s.observe(s.id, "saved", &response); err != nil {
		return err
	}
	return nil
}

func authRedirectConfinement(s *authSession) error {
	var redirects []struct {
		Body     string
		Followed *authResponse
	}
	if err := s.observe(s.id, "redirect", &redirects); err != nil {
		return err
	}
	if len(redirects) != 3 || (redirects[0].Body != s.service.marker && (redirects[0].Followed == nil || redirects[0].Followed.Body != s.service.marker)) {
		return fmt.Errorf("allowed redirect download failed")
	}
	var upload []struct {
		Status int
		Body   string
	}
	if err := s.observe(s.id, "upload", &upload); err != nil {
		return err
	}
	if len(upload) != 2 || upload[0].Status != 202 || upload[1].Status != 201 || upload[1].Body != s.service.marker {
		return fmt.Errorf("upload sequence did not complete")
	}
	cdn := false
	for _, r := range s.service.snapshot() {
		if r.Host == authCDN {
			cdn = true
		}
	}
	if !cdn {
		return fmt.Errorf("allowed redirect never reached CDN")
	}
	return nil
}

func authHostOnly(s *authSession) error {
	var config struct{ Pair []string }
	if err := s.observe(s.id, "config", &config); err != nil {
		return err
	}
	if len(config.Pair) != 2 || config.Pair[0] == "" || config.Pair[1] == "" {
		return fmt.Errorf("usable placeholder login missing")
	}
	return nil
}

func authLifetime(s *authSession) error {
	var hooks map[string]authLogin
	if err := s.observe(s.id, "hooks", &hooks); err != nil {
		return err
	}
	for _, phase := range []string{"install", "startup"} {
		if hooks[phase].Registry.Body != s.service.marker {
			return fmt.Errorf("authentication unavailable during %s", phase)
		}
	}
	var saved struct {
		Token string
		Pair  []string
	}
	if err := s.observe(s.id, "save", &saved); err != nil {
		return err
	}
	original, expiry := s.service.latestToken()
	if saved.Token == "" || original == "" || len(saved.Pair) != 2 {
		return fmt.Errorf("missing run-local placeholder or issuer token")
	}
	if err := s.env.Adapter.Remove(s.ctx, s.id); err != nil {
		return err
	}
	s.cleanup = func() {}
	var err error
	s.id, s.cleanup, err = s.create("available", "docker-auth", "docker-auth-network")
	if err != nil {
		return err
	}
	s.service.reset()
	var responses []authResponse
	if err := s.observe(s.id, "foreign", &responses, append([]string{saved.Token}, saved.Pair...)...); err != nil {
		return err
	}
	if !time.Now().Before(expiry) || !s.service.validBearer("Bearer "+original, time.Now()) {
		return fmt.Errorf("withdrawal probe outlived its control token")
	}
	if len(responses) != 2 {
		return fmt.Errorf("missing withdrawn credential observations")
	}
	if s.service.usedCredentials() {
		return fmt.Errorf("ended run's credential placeholder reached upstream as real credentials")
	}
	for _, response := range responses {
		if authorized(response.Status) {
			return fmt.Errorf("ended run's credential placeholder accepted in new run")
		}
	}
	return nil
}

func authNoExtraAccess(s *authSession) error {
	var ambient authAmbient
	if err := s.observe(s.id, "ambient", &ambient); err != nil {
		return err
	}
	for _, fixture := range []string{"docker-auth-absent", "docker-auth"} {
		s.service.reset()
		id, remove, err := s.create("available", fixture)
		if err != nil {
			return err
		}
		var baseline authAmbient
		err = s.observe(id, "ambient", &baseline)
		remove()
		if err != nil {
			return err
		}
		if fixture == "docker-auth" && len(s.service.snapshot()) != 0 {
			return fmt.Errorf("authentication granted egress without network policy")
		}
		if fixture == "docker-auth-absent" {
			if ambient.Socket && !baseline.Socket || ambient.Engine && !baseline.Engine {
				return fmt.Errorf("authentication supplied engine or Docker socket")
			}
			if baseline.Authentication != nil && baseline.Authentication.Registry.Body == s.service.marker {
				return fmt.Errorf("authentication supplied without grant")
			}
		}
	}
	return nil
}

func init() {
	checks = append(checks,
		authCheck("host-resolution", authHostResolution),
		authCheck("request-scope", authRequestScope),
		authCheck("token-mediation", authTokenMediation),
		authCheck("bearer-scope", authBearerScope),
		authCheck("redirect-confinement", authRedirectConfinement),
		authCheck("host-only", authHostOnly),
		authCheck("lifetime", authLifetime),
		authCheck("no-extra-access", authNoExtraAccess),
	)
}
