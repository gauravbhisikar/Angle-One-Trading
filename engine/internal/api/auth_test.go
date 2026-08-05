package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newAuthTestServer() *Server {
	return &Server{
		Config: Config{
			LoginUsername: "testuser",
			LoginPassword: "testpass",
			LoginKey:      "testkey",
		},
		lastStatus: map[string]string{},
		sessions:   map[string]time.Time{},
	}
}

func passthrough() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:54321": true,
		"[::1]:54321":     true,
		"203.0.113.5:80":  false,
		"10.0.0.5:9080":   false,
	}
	for addr, want := range cases {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestAuthMiddlewareLoopbackAlwaysPasses guards the exact property agent/
// contextbuilder's ENGINE_URL=http://localhost:9080 calls depend on: even
// with a login gate fully configured, a loopback caller with no session
// cookie at all must still reach the real handler untouched.
func TestAuthMiddlewareLoopbackAlwaysPasses(t *testing.T) {
	s := newAuthTestServer()
	req := httptest.NewRequest(http.MethodGet, "/strategies", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	rec := httptest.NewRecorder()

	s.authMiddleware(passthrough()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("loopback request was blocked: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// TestAuthMiddlewareBlocksUnconfiguredSessionFromPublicIP is the other
// half: a non-loopback caller with no cookie must NOT reach the real
// handler — this is the entire point of the gate.
func TestAuthMiddlewareBlocksPublicIPWithoutSession(t *testing.T) {
	s := newAuthTestServer()

	getReq := httptest.NewRequest(http.MethodGet, "/", nil)
	getReq.RemoteAddr = "203.0.113.5:12345"
	getRec := httptest.NewRecorder()
	s.authMiddleware(passthrough()).ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || !bytes.Contains(getRec.Body.Bytes(), []byte("Sign in")) {
		t.Fatalf("expected the login page for an unauthenticated GET, got status=%d", getRec.Code)
	}

	// A GET without a session always gets the login page regardless of
	// path — in the real browser flow, dashboard.html's own JS can't even
	// be running yet to issue a fetch unless GET / already returned it,
	// which only happens once a session is valid. The 401 JSON path is for
	// non-GET calls (mutating actions), which is what actually matters if
	// a session goes stale mid-use.
	postReq := httptest.NewRequest(http.MethodPost, "/strategies/x/run", nil)
	postReq.RemoteAddr = "203.0.113.5:12345"
	postRec := httptest.NewRecorder()
	s.authMiddleware(passthrough()).ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an unauthenticated non-GET call, got %d: %s", postRec.Code, postRec.Body.String())
	}
}

// TestAuthMiddlewareUnconfiguredIsNoop confirms the gate disables itself
// entirely (today's open behavior) when username/password/key aren't all
// set — a fresh local clone with no .env must not get locked out.
func TestAuthMiddlewareUnconfiguredIsNoop(t *testing.T) {
	s := &Server{sessions: map[string]time.Time{}}
	req := httptest.NewRequest(http.MethodGet, "/strategies", nil)
	req.RemoteAddr = "203.0.113.5:12345"
	rec := httptest.NewRecorder()

	s.authMiddleware(passthrough()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("expected passthrough with no login configured, got status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// TestLoginFlowEndToEnd: wrong credentials rejected, right credentials
// issue a session cookie that then lets the same (still non-loopback)
// caller through on a subsequent request.
func TestLoginFlowEndToEnd(t *testing.T) {
	s := newAuthTestServer()
	handler := s.authMiddleware(passthrough())

	badBody, _ := json.Marshal(loginRequest{Username: "testuser", Password: "wrong", Key: "testkey"})
	badReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(badBody))
	badReq.RemoteAddr = "203.0.113.5:12345"
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong credentials should be rejected, got %d", badRec.Code)
	}

	goodBody, _ := json.Marshal(loginRequest{Username: "testuser", Password: "testpass", Key: "testkey"})
	goodReq := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(goodBody))
	goodReq.RemoteAddr = "203.0.113.5:12345"
	goodRec := httptest.NewRecorder()
	handler.ServeHTTP(goodRec, goodReq)
	if goodRec.Code != http.StatusOK {
		t.Fatalf("correct credentials should succeed, got %d: %s", goodRec.Code, goodRec.Body.String())
	}
	cookies := goodRec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("expected a %s cookie, got %v", sessionCookieName, cookies)
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/strategies", nil)
	apiReq.RemoteAddr = "203.0.113.5:12345"
	apiReq.AddCookie(cookies[0])
	apiRec := httptest.NewRecorder()
	handler.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusOK || apiRec.Body.String() != "ok" {
		t.Fatalf("valid session should reach the real handler, got status=%d body=%q", apiRec.Code, apiRec.Body.String())
	}
}
