package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	testReadToken  = "read-token-0123456789"
	testWriteToken = "write-token-0123456789"
)

func authServer() *Server {
	return NewServer(&mockStore{}, Options{Auth: Auth{ReadToken: testReadToken, WriteToken: testWriteToken}})
}

func do(t *testing.T, h http.Handler, method, target, body string, setup func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if setup != nil {
		setup(req)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func withBearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// publicAPIRoutes is the allowlist of /api routes that work without a token,
// with the reason. A new route that is not here must require a token.
var publicAPIRoutes = map[string]string{
	"GET /api/session":    "dashboard asks whether to show the login form",
	"POST /api/session":   "the login itself; rate limited",
	"DELETE /api/session": "logout only clears a cookie",
}

// Walks every registered route: with tokens configured, each /api route must
// refuse a request without credentials unless it is on the allowlist.
func TestEveryAPIRouteRequiresAToken(t *testing.T) {
	s := authServer()
	r := s.router()
	walked := 0
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") {
			return nil
		}
		walked++
		key := method + " " + route
		rr := do(t, r, method, route, "{}", nil)
		if _, public := publicAPIRoutes[key]; public {
			if rr.Code == http.StatusUnauthorized {
				t.Errorf("%s is allowlisted as public but returned 401", key)
			}
			return nil
		}
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", key, rr.Code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A refactor that hides routes from the walk must fail, not pass on nothing.
	if walked < 8 {
		t.Fatalf("walked %d /api routes, expected at least 8", walked)
	}
}

func TestTokensGrantOnlyTheirSide(t *testing.T) {
	h := authServer().SetupRoutes()
	logBody := `{"level":"INFO","type":"API","message":"m"}`

	tests := []struct {
		name         string
		method, path string
		body         string
		token        string
		want         int
	}{
		{"read token reads", "GET", "/api/logs", "", testReadToken, http.StatusOK},
		{"read token reads facets", "GET", "/api/facets", "", testReadToken, http.StatusOK},
		{"write token cannot read", "GET", "/api/logs", "", testWriteToken, http.StatusUnauthorized},
		{"write token writes", "POST", "/api/logs", logBody, testWriteToken, http.StatusOK},
		{"write token writes batches", "POST", "/api/logs/batch", "[" + logBody + "]", testWriteToken, http.StatusOK},
		{"read token cannot write", "POST", "/api/logs", logBody, testReadToken, http.StatusUnauthorized},
		{"wrong token", "GET", "/api/logs", "", "nope", http.StatusUnauthorized},
		{"token prefix is not enough", "GET", "/api/logs", "", testReadToken[:10], http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := do(t, h, tc.method, tc.path, tc.body, withBearer(tc.token))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tc.want, rr.Body.String())
			}
			if tc.want == http.StatusUnauthorized && rr.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 without WWW-Authenticate")
			}
		})
	}
}

func TestNoTokensMeansOpen(t *testing.T) {
	h := newTestServer(&mockStore{}).SetupRoutes()
	if rr := do(t, h, "GET", "/api/logs", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("GET /api/logs = %d", rr.Code)
	}
	if rr := do(t, h, "POST", "/api/logs", `{"level":"INFO","type":"API","message":"m"}`, nil); rr.Code != http.StatusOK {
		t.Fatalf("POST /api/logs = %d", rr.Code)
	}
	rr := do(t, h, "GET", "/api/session", "", nil)
	if body := strings.TrimSpace(rr.Body.String()); body != `{"auth":false,"authenticated":true,"canWrite":true}` {
		t.Fatalf("session = %s", body)
	}
	if rr := do(t, h, "POST", "/api/session", `{"token":"x"}`, nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("login with auth off = %d, want 400", rr.Code)
	}
}

func TestDashboardSession(t *testing.T) {
	s := authServer()
	h := s.SetupRoutes()

	rr := do(t, h, "GET", "/api/session", "", nil)
	if body := strings.TrimSpace(rr.Body.String()); body != `{"auth":true,"authenticated":false,"canWrite":false}` {
		t.Fatalf("session before login = %s", body)
	}

	if rr := do(t, h, "POST", "/api/session", `{"token":"wrong-token-0123456789"}`, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d", rr.Code)
	}
	if rr := do(t, h, "POST", "/api/session", `{"token":"`+testWriteToken+`"}`, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("login with the write token = %d, want 401", rr.Code)
	}
	if rr := do(t, h, "POST", "/api/session", `{}`, nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("login without token = %d, want 400", rr.Code)
	}

	rr = do(t, h, "POST", "/api/session", `{"token":"`+testReadToken+`"}`, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("login = %d", rr.Code)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies", len(cookies))
	}
	c := cookies[0]
	if c.Name != sessionCookie || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Fatalf("cookie = %+v", c)
	}
	if strings.Contains(c.Value, testReadToken) {
		t.Fatal("the cookie must not carry the token itself")
	}

	withCookie := func(r *http.Request) { r.AddCookie(c) }
	if rr := do(t, h, "GET", "/api/logs", "", withCookie); rr.Code != http.StatusOK {
		t.Fatalf("read with cookie = %d", rr.Code)
	}
	if rr := do(t, h, "POST", "/api/logs", `{"level":"INFO","type":"API","message":"m"}`, withCookie); rr.Code != http.StatusUnauthorized {
		t.Fatalf("write with the session cookie = %d, want 401", rr.Code)
	}
	rr = do(t, h, "GET", "/api/session", "", withCookie)
	if body := strings.TrimSpace(rr.Body.String()); body != `{"auth":true,"authenticated":true,"canWrite":false}` {
		t.Fatalf("session after login = %s", body)
	}

	// Rotating the read token ends existing sessions.
	rotated := NewServer(&mockStore{}, Options{Auth: Auth{ReadToken: "another-read-token-0123", WriteToken: testWriteToken}})
	if rr := do(t, rotated.SetupRoutes(), "GET", "/api/logs", "", withCookie); rr.Code != http.StatusUnauthorized {
		t.Fatalf("old cookie after token rotation = %d, want 401", rr.Code)
	}

	rr = do(t, h, "DELETE", "/api/session", "", withCookie)
	if rr.Code != http.StatusNoContent || len(rr.Result().Cookies()) != 1 || rr.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout = %d, cookies %+v", rr.Code, rr.Result().Cookies())
	}
}

func TestSecureCookieFlag(t *testing.T) {
	s := NewServer(&mockStore{}, Options{Auth: Auth{ReadToken: testReadToken, SecureCookies: true}})
	rr := do(t, s.SetupRoutes(), "POST", "/api/session", `{"token":"`+testReadToken+`"}`, nil)
	if c := rr.Result().Cookies(); len(c) != 1 || !c[0].Secure {
		t.Fatalf("cookie = %+v, want Secure", c)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := authServer().SetupRoutes()
	fromIP := func(ip string) func(*http.Request) {
		return func(r *http.Request) { r.RemoteAddr = ip + ":1234" }
	}
	for i := 0; i < loginAttempts; i++ {
		if rr := do(t, h, "POST", "/api/session", `{"token":"guess-0123456789abc"}`, fromIP("10.0.0.1")); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, rr.Code)
		}
	}
	// Even the right token is refused once the limit is hit.
	rr := do(t, h, "POST", "/api/session", `{"token":"`+testReadToken+`"}`, fromIP("10.0.0.1"))
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit = %d", rr.Code)
	}
	// X-Forwarded-For does not get around it.
	rr = do(t, h, "POST", "/api/session", `{"token":"`+testReadToken+`"}`, func(r *http.Request) {
		r.RemoteAddr = "10.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
	})
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For = %d, want 429", rr.Code)
	}
	// Another address is not affected.
	if rr := do(t, h, "POST", "/api/session", `{"token":"`+testReadToken+`"}`, fromIP("10.0.0.2")); rr.Code != http.StatusNoContent {
		t.Fatalf("other IP = %d", rr.Code)
	}
}

func TestLoginLimiterWindowAndPrune(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < loginAttempts; i++ {
		if !l.allow("a") {
			t.Fatalf("attempt %d refused", i)
		}
	}
	if l.allow("a") {
		t.Fatal("attempt over the limit allowed")
	}
	now = now.Add(loginWindow)
	if !l.allow("a") {
		t.Fatal("attempt after the window refused")
	}

	// Filling the map triggers a prune of addresses with no recent attempts.
	now = now.Add(2 * loginWindow)
	for i := 0; i < loginLimiterMaxIPs; i++ {
		l.attempts[string(rune(i))+"x"] = []time.Time{now.Add(-2 * loginWindow)}
	}
	l.allow("b")
	if len(l.attempts) > 2 {
		t.Fatalf("limiter kept %d stale addresses after prune", len(l.attempts))
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[2001:db8::1]:443"
	if got := clientIP(r); got != "2001:db8::1" {
		t.Fatalf("clientIP = %q", got)
	}
	r.RemoteAddr = "no-port"
	if got := clientIP(r); got != "no-port" {
		t.Fatalf("clientIP = %q", got)
	}
}

func TestStreamRequiresReadAccess(t *testing.T) {
	s := authServer()
	ts, _ := startServer(t, s, nil)
	resp, err := http.Get(ts.URL + "/api/logs/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stream without token = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/logs/stream", nil)
	req.Header.Set("Authorization", "Bearer "+testReadToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream with read token = %d", resp.StatusCode)
	}
}
