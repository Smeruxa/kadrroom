package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	testOperator = "operator@example.test"
	testPublic   = "https://kadrroom.example"
	testPassword = "a very unique password"
	newPassword  = "new unique test password"
)

var testMailTokenKey = [32]byte{0x01, 0x02, 0x03}

func TestPasswordAndToken(t *testing.T) {
	if validPassword("short") || validPassword(strings.Repeat("я", 129)) || !validPassword(strings.Repeat("я", 15)) {
		t.Fatal("password length must count characters")
	}
	if acceptableNewPassword(strings.Repeat("я", 15)) || acceptableNewPassword("PasswordPassword1") || !acceptableNewPassword(testPassword) {
		t.Fatal("new password screening must reject repeated and common values")
	}
	first, err := hashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !verifyPassword(first, testPassword) || verifyPassword(second, "other wrong password") || verifyPassword("invalid", testPassword) {
		t.Fatal("unexpected password verification or salt reuse")
	}
	token, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if !validToken(token) || validToken(token+"=") || validToken("invalid") || bytes.Equal(tokenHash(token), []byte(token)) {
		t.Fatal("unexpected token format or persistence value")
	}
}

func TestInputsAndMailEncoding(t *testing.T) {
	address, valid := normalizeEmail("  Owner+demo@Example.Test ")
	if !valid || address != "owner+demo@example.test" {
		t.Fatal("email normalization failed")
	}
	for _, value := range []string{"name <demo@example.test>", "owner", "owner@localhost", "demo@example.test\r\nBcc: other@example.test"} {
		if _, valid := normalizeEmail(value); valid {
			t.Errorf("accepted an invalid address")
		}
	}
	if _, valid := normalizeName("name\nnext"); valid {
		t.Fatal("accepted a control character in a display name")
	}
	publicURL, err := url.Parse(testPublic)
	if err != nil {
		t.Fatal(err)
	}
	subject, body := challengeMessage(publicURL, purposeReset, "testing-token")
	encoded := mailMessage("sender@example.test", testOperator, subject, body)
	message, err := mail.ReadMessage(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	text, err := io.ReadAll(quotedprintable.NewReader(message.Body))
	if err != nil {
		t.Fatal(err)
	}
	if message.Header.Get("To") != testOperator || !strings.Contains(string(text), testPublic+"/reset?token=testing-token") {
		t.Fatal("mail message lost recipient or challenge link")
	}
}

func TestOriginAndSessionWithoutCredentials(t *testing.T) {
	const adminOrigin = "https://admin.example.test"
	handler := &identityHandler{publicOrigin: testPublic, adminOrigin: adminOrigin, secureCookies: true}
	routes := handler.routes()
	for _, test := range []struct {
		name   string
		method string
		path   string
		origin string
		status int
		code   string
	}{
		{name: "origin", method: http.MethodPost, path: "/v1/auth/login", origin: "https://other.example", status: 403, code: "invalid_origin"},
		{name: "admin origin", method: http.MethodPost, path: "/v1/auth/logout", origin: adminOrigin, status: 204},
		{name: "untrusted admin lookalike", method: http.MethodPost, path: "/v1/auth/logout", origin: adminOrigin + ".other.example", status: 403, code: "invalid_origin"},
		{name: "session", method: http.MethodGet, path: "/v1/auth/session", status: 401, code: "unauthenticated"},
		{name: "method", method: http.MethodGet, path: "/v1/auth/login", status: 405, code: "method_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, nil)
			req.Header.Set("Origin", test.origin)
			res := httptest.NewRecorder()
			routes.ServeHTTP(res, req)
			expectStatus(t, res, test.status, test.code)
		})
	}
	logout := makeRequest(routes, http.MethodPost, "/v1/auth/logout", "", nil)
	expectStatus(t, logout, 204, "")
	deleted := logout.Result().Cookies()[0]
	if deleted.Name != sessionCookie || !deleted.Secure || !deleted.HttpOnly || deleted.Path != "/" || deleted.MaxAge != -1 {
		t.Fatal("logout cookie attributes differ from login cookie policy")
	}
}

func TestClientAddressAcceptsOnlyAuthenticatedSingleHop(t *testing.T) {
	const token = "test-proxy-key-at-least-32-bytes-long"
	handler := &identityHandler{proxyToken: token}
	for _, test := range []struct {
		name      string
		tokens    []string
		addresses []string
		want      string
	}{
		{name: "direct", want: "192.0.2.1"},
		{name: "forged", tokens: []string{"forged"}, addresses: []string{"203.0.113.34"}, want: "192.0.2.1"},
		{name: "missing token", addresses: []string{"203.0.113.34"}, want: "192.0.2.1"},
		{name: "authenticated", tokens: []string{token}, addresses: []string{"203.0.113.34"}, want: "203.0.113.34"},
		{name: "canonical address", tokens: []string{token}, addresses: []string{"::ffff:203.0.113.34"}, want: "203.0.113.34"},
		{name: "IPv6", tokens: []string{token}, addresses: []string{"2001:db8::34"}, want: "2001:db8::34"},
		{name: "client list", tokens: []string{token}, addresses: []string{"203.0.113.34, 192.0.2.37"}, want: "192.0.2.1"},
		{name: "duplicate address", tokens: []string{token}, addresses: []string{"203.0.113.34", "192.0.2.37"}, want: "192.0.2.1"},
		{name: "duplicate token", tokens: []string{token, token}, addresses: []string{"203.0.113.34"}, want: "192.0.2.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
			request.Header.Set("X-Forwarded-For", "198.51.100.1")
			for _, value := range test.tokens {
				request.Header.Add(authProxyTokenHeader, value)
			}
			for _, value := range test.addresses {
				request.Header.Add(authClientIPHeader, value)
			}
			if got := handler.clientAddress(request); got != test.want {
				t.Fatalf("client address = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProxyPeerLimitSeparatesClientsWithPostgres(t *testing.T) {
	s := testStore(t)
	h := &identityHandler{store: s, proxyToken: "test-proxy-key-at-least-32-bytes-long"}
	for _, address := range []string{"192.0.2.1", "203.0.113.20"} {
		if _, err := s.pool.Exec(t.Context(), `
			INSERT INTO identity_rate_limits (key, hits, expires_at)
			VALUES ($1, 150, now()+interval '1 minute')
		`, rateKey("auth-peer", address)); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name    string
		address string
		token   string
		allowed bool
	}{
		{name: "limited client", address: "203.0.113.20", token: h.proxyToken},
		{name: "second client behind same proxy", address: "203.0.113.21", token: h.proxyToken, allowed: true},
		{name: "forged fresh address stays in peer limit", address: "203.0.113.22", token: "wrong"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
			request.Header.Set(authProxyTokenHeader, test.token)
			request.Header.Set(authClientIPHeader, test.address)
			if allowed := h.allowPeer(response, request); allowed != test.allowed {
				t.Fatalf("peer allowed = %v, want %v", allowed, test.allowed)
			}
			if !test.allowed {
				expectStatus(t, response, 429, "rate_limited")
			}
		})
	}
}

func TestAccountLifecycleWithPostgres(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	publicURL, err := url.Parse(testPublic)
	if err != nil {
		t.Fatal(err)
	}
	h, err := newIdentityHandler(s, config{publicURL: publicURL, secureCookies: true, operatorEmail: testOperator})
	if err != nil {
		t.Fatal(err)
	}
	routes := h.routes()
	catcher := &captureMailer{}

	expectStatus(t, makeRequest(routes, "GET", "/healthz", "", nil), 200, "")
	expectStatus(t, makeRequest(routes, "GET", "/readyz", "", nil), 200, "")
	h.draining.Store(true)
	expectStatus(t, makeRequest(routes, "GET", "/readyz", "", nil), 503, "service_unavailable")
	h.draining.Store(false)
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/register", `{"email":"not-mail"}`, nil), 400, "invalid_email")
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/register", `{"email":"Operator@EXAMPLE.TEST"}`, nil), 202, "")

	job, err := s.nextMail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catcher.err = errors.New("SMTP result unavailable after accepting data")
	catcher.acceptBeforeError = true
	if err := processMail(ctx, s, catcher, publicURL, testMailTokenKey, job); !errors.Is(err, catcher.err) {
		t.Fatalf("failed SMTP attempt did not surface an error: %v", err)
	}
	if len(catcher.messages) != 1 {
		t.Fatal("fixture did not capture the already-delivered email")
	}
	firstMessage := catcher.messages[0]
	var firstExpiry time.Time
	if err := s.pool.QueryRow(ctx, "SELECT expires_at FROM identity_challenges").Scan(&firstExpiry); err != nil {
		t.Fatal(err)
	}
	if token, err := s.mailChallenge(ctx, [32]byte{0x99}, purposeVerify, testOperator); err == nil || token != "" {
		t.Fatal("a different key replaced the outstanding challenge")
	}
	if _, err := s.nextMail(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("mail retried without waiting: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE identity_mail_requests SET next_at = now()"); err != nil {
		t.Fatal(err)
	}
	catcher.err = nil
	verifyToken := nextToken(t, s, catcher, publicURL, purposeVerify)
	if catcher.messages[1] != firstMessage {
		t.Fatal("retry invalidated the original email link")
	}
	var expiry time.Time
	var nonce, digest []byte
	if err := s.pool.QueryRow(ctx, "SELECT expires_at, token_nonce, token_hash FROM identity_challenges").
		Scan(&expiry, &nonce, &digest); err != nil {
		t.Fatal(err)
	}
	if !expiry.Equal(firstExpiry) || len(nonce) != tokenBytes ||
		!bytes.Equal(digest, tokenHash(verifyToken)) || bytes.Equal(nonce, []byte(verifyToken)) {
		t.Fatal("retry changed challenge expiry or did not store nonce and digest separately")
	}
	verifyBody := requestBody(t, tokenRequest{Token: verifyToken, Password: testPassword, DisplayName: "Ведущий"})
	results := make(chan int, 2)
	for range 2 {
		go func() {
			results <- makeRequest(routes, "POST", "/v1/auth/verify", verifyBody, nil).Code
		}()
	}
	first, second := <-results, <-results
	if (first != 204 || second != 400) && (first != 400 || second != 204) {
		t.Fatalf("concurrent confirmation returned %d and %d", first, second)
	}
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/verify", verifyBody, nil), 400, "invalid_token")
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/register", requestBody(t, emailRequest{Email: testOperator}), nil), 202, "")
	mailCount := len(catcher.messages)
	processNextMail(t, s, catcher, publicURL)
	if len(catcher.messages) != mailCount {
		t.Fatal("registration sent another activation to an existing account")
	}

	loginBody := requestBody(t, loginRequest{Email: testOperator, Password: testPassword})
	login := makeRequest(routes, "POST", "/v1/auth/login", loginBody, nil)
	expectStatus(t, login, 200, "")
	var profile account
	if err := json.Unmarshal(login.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.ID == "" || profile.Role != "operator" || profile.Email != testOperator || profile.DisplayName != "Ведущий" {
		t.Fatal("unexpected operator profile")
	}
	cookie := login.Result().Cookies()[0]
	if cookie.Name != sessionCookie || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unexpected account cookie")
	}
	expectStatus(t, makeRequest(routes, "GET", "/v1/auth/session", "", cookie), 200, "")
	rotated := makeRequest(routes, "POST", "/v1/auth/login", loginBody, cookie)
	expectStatus(t, rotated, 200, "")
	rotatedCookie := rotated.Result().Cookies()[0]
	expectStatus(t, makeRequest(routes, "GET", "/v1/auth/session", "", cookie), 401, "unauthenticated")
	expectStatus(t, makeRequest(routes, "GET", "/v1/auth/session", "", rotatedCookie), 200, "")

	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/password/reset-request", `{"email":"unknown@example.test"}`, nil), 202, "")
	processNextMail(t, s, catcher, publicURL)
	if len(catcher.messages) != mailCount {
		t.Fatal("password reset sent a secret for an unknown address")
	}
	operatorBody := requestBody(t, emailRequest{Email: testOperator})
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/password/reset-request", operatorBody, nil), 202, "")
	expired := nextToken(t, s, catcher, publicURL, purposeReset)
	if _, err := s.pool.Exec(ctx, "UPDATE identity_challenges SET expires_at = now() WHERE purpose = 'reset'"); err != nil {
		t.Fatal(err)
	}
	expiredBody := requestBody(t, tokenRequest{Token: expired, Password: newPassword})
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/password/reset", expiredBody, nil), 400, "invalid_token")
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/password/reset-request", operatorBody, nil), 202, "")
	resetToken := nextToken(t, s, catcher, publicURL, purposeReset)
	resetBody := requestBody(t, tokenRequest{Token: resetToken, Password: newPassword})
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/password/reset", resetBody, nil), 204, "")
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/password/reset", resetBody, nil), 400, "invalid_token")
	expectStatus(t, makeRequest(routes, "GET", "/v1/auth/session", "", rotatedCookie), 401, "unauthenticated")
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/login", loginBody, nil), 401, "invalid_credentials")

	loginReplacement := requestBody(t, loginRequest{Email: testOperator, Password: newPassword})
	replacement := makeRequest(routes, "POST", "/v1/auth/login", loginReplacement, nil)
	expectStatus(t, replacement, 200, "")
	replacementCookie := replacement.Result().Cookies()[0]
	expectStatus(t, makeRequest(routes, "POST", "/v1/auth/logout", "", replacementCookie), 204, "")
	expectStatus(t, makeRequest(routes, "GET", "/v1/auth/session", "", replacementCookie), 401, "unauthenticated")

	for range 3 {
		expectStatus(t, makeRequest(routes, "POST", "/v1/auth/register", `{"email":"limit@example.test"}`, nil), 202, "")
	}
	limited := makeRequest(routes, "POST", "/v1/auth/register", `{"email":"limit@example.test"}`, nil)
	expectStatus(t, limited, 429, "rate_limited")
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response has no retry hint")
	}
}

func TestExhaustedMailRequestCanBeQueuedAgainWithPostgres(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	email := "retry@example.test"
	if err := s.queueMail(ctx, purposeVerify, email); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE identity_mail_requests SET attempts=5, next_at=now()+interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	if err := s.queueMail(ctx, purposeVerify, email); err != nil {
		t.Fatal(err)
	}
	if _, err := s.nextMail(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("exhausted job ignored cooldown: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE identity_mail_requests SET next_at=now()"); err != nil {
		t.Fatal(err)
	}
	if err := s.queueMail(ctx, purposeVerify, email); err != nil {
		t.Fatal(err)
	}
	job, err := s.nextMail(ctx)
	if err != nil || job.email != email || job.attempts != 1 {
		t.Fatalf("expired exhausted job was not requeued: %v", err)
	}
}

func TestMailRetryKeepsPreMigrationChallengeValidWithPostgres(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	const email = "legacy@example.test"
	oldToken, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO identity_challenges (purpose, email, token_hash, expires_at)
		VALUES ('verify', $1, $2, now()+interval '1 hour')
	`, email, tokenHash(oldToken)); err != nil {
		t.Fatal(err)
	}
	next, err := s.mailChallenge(ctx, testMailTokenKey, purposeVerify, email)
	if err != nil || !validToken(next) || next == oldToken {
		t.Fatalf("active legacy challenge could not be supplemented: %v", err)
	}
	if matches, err := s.challengeExists(ctx, purposeVerify, oldToken); err != nil || !matches {
		t.Fatalf("pre-upgrade email was invalidated: %v", err)
	}
	if verified, err := s.verify(ctx, oldToken, "Old link", "unused hash fixture", ""); err != nil || !verified {
		t.Fatalf("pre-upgrade token could not be redeemed: %v", err)
	}
	if matches, err := s.challengeExists(ctx, purposeVerify, next); err != nil || matches {
		t.Fatalf("redeeming old link left new token active: %v", err)
	}
}

type captureMailer struct {
	messages          []string
	err               error
	acceptBeforeError bool
}

func (m *captureMailer) send(_ context.Context, _, _, body string) error {
	if m.err == nil || m.acceptBeforeError {
		m.messages = append(m.messages, body)
	}
	return m.err
}

func processNextMail(t *testing.T, s store, catcher *captureMailer, publicURL *url.URL) {
	t.Helper()
	job, err := s.nextMail(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := processMail(t.Context(), s, catcher, publicURL, testMailTokenKey, job); err != nil {
		t.Fatal(err)
	}
}

func nextToken(t *testing.T, s store, catcher *captureMailer, publicURL *url.URL, purpose string) string {
	t.Helper()
	before := len(catcher.messages)
	processNextMail(t, s, catcher, publicURL)
	if len(catcher.messages) != before+1 {
		t.Fatal("expected one challenge mail")
	}
	for _, line := range strings.Split(catcher.messages[before], "\r\n") {
		link, err := url.Parse(line)
		if err == nil && link.Host == publicURL.Host && link.Path == "/"+purpose {
			token := link.Query().Get("token")
			if validToken(token) {
				return token
			}
		}
	}
	t.Fatal("mail did not contain an expected challenge link")
	return ""
}

func testStore(t *testing.T) store {
	t.Helper()
	connection := os.Getenv("KADRROOM_IDENTITY_TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("KADRROOM_IDENTITY_TEST_DATABASE_URL is required for the Postgres integration test")
	}
	root, err := pgxpool.New(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	suffix, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "identity_test_" + strings.ToLower(suffix)
	identifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := root.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := root.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(connection)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = identifier + ", public"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := store{pool: pool}
	if err := s.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func requestBody(t *testing.T, data any) string {
	t.Helper()
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func makeRequest(routes http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	routes.ServeHTTP(res, req)
	return res
}

func expectStatus(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d: %s", response.Code, status, response.Body.String())
	}
	if code == "" {
		return
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != code {
		t.Fatalf("expected error %q, got %s", code, response.Body.String())
	}
}
