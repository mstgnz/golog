package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie  = "golog_session"
	sessionMaxAge  = 7 * 24 * time.Hour
	sessionContext = "golog-session-v1" // HMAC domain separator for the cookie value

	loginAttempts = 5 // per IP (IPv6: per /64) per loginWindow
	loginWindow   = time.Minute
	// loginLimiterMaxIPs caps how many addresses the limiter tracks. When it
	// is full of recent ones, new addresses are refused until entries expire.
	loginLimiterMaxIPs = 10000
)

// Auth holds the API tokens. An empty token leaves that side open.
type Auth struct {
	WriteToken string // POST /api/logs, /api/logs/batch
	ReadToken  string // every read endpoint and the dashboard session
	// SecureCookies sets Secure on the session cookie even without TLS on
	// this connection (TLS ending at a proxy).
	SecureCookies bool
}

// tokenEqual compares in constant time, length included: comparing SHA-256
// digests keeps a length mismatch from returning early.
func tokenEqual(got, want string) bool {
	a := sha256.Sum256([]byte(got))
	b := sha256.Sum256([]byte(want))
	return hmac.Equal(a[:], b[:])
}

func bearerToken(r *http.Request) string {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(tok)
	}
	return ""
}

// sessionValue is derived from the read token, so rotating the token ends
// every dashboard session without server-side state.
func (s *Server) sessionValue() string {
	mac := hmac.New(sha256.New, []byte(s.opts.Auth.ReadToken))
	mac.Write([]byte(sessionContext))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) canRead(r *http.Request) bool {
	if s.opts.Auth.ReadToken == "" {
		return true
	}
	if tok := bearerToken(r); tok != "" && tokenEqual(tok, s.opts.Auth.ReadToken) {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && tokenEqual(c.Value, s.sessionValue()) {
		return true
	}
	return false
}

// canWrite never accepts the session cookie: the write token stays with
// applications and never reaches a browser.
func (s *Server) canWrite(r *http.Request) bool {
	if s.opts.Auth.WriteToken == "" {
		return true
	}
	tok := bearerToken(r)
	return tok != "" && tokenEqual(tok, s.opts.Auth.WriteToken)
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="golog"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

func (s *Server) requireRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.canRead(r) {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.canWrite(r) {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type sessionInfo struct {
	Auth          bool `json:"auth"`          // a read token is configured
	Authenticated bool `json:"authenticated"` // this request may read
	CanWrite      bool `json:"canWrite"`      // writes need no token (dashboard shows Add Log)
}

// GetSessionHandler tells the dashboard whether to show the login form and
// the Add Log button.
func (s *Server) GetSessionHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, sessionInfo{
		Auth:          s.opts.Auth.ReadToken != "",
		Authenticated: s.canRead(r),
		CanWrite:      s.opts.Auth.WriteToken == "",
	})
}

// LoginHandler exchanges the read token for an HttpOnly session cookie.
// Body: {"token": "..."}.
func (s *Server) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if s.opts.Auth.ReadToken == "" {
		http.Error(w, "authentication is not enabled", http.StatusBadRequest)
		return
	}
	if !s.logins.allow(limiterKey(clientIP(r))) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	if !tokenEqual(body.Token, s.opts.Auth.ReadToken) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessionValue(),
		Path:     "/",
		MaxAge:   int(sessionMaxAge / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil || s.opts.Auth.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// LogoutHandler clears the session cookie.
func (s *Server) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil || s.opts.Auth.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// clientIP uses the TCP peer address only. X-Forwarded-For is ignored on
// purpose: any client can set it, which would void the rate limit.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiterKey groups IPv6 addresses by /64: one subscriber usually holds a
// whole /64, so per-address counting would give them billions of budgets.
func limiterKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// loginLimiter allows `limit` attempts per IP per window (sliding).
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	limit    int
	window   time.Duration
	now      func() time.Time
	// nextPrune: before it a full map refuses newcomers without scanning.
	// It is when the oldest tracked attempt expires, but at least a tenth of
	// the window after the last scan, so a spray costs ~10 scans per window.
	nextPrune time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string][]time.Time{}, limit: loginAttempts, window: loginWindow, now: time.Now}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if _, known := l.attempts[ip]; !known && len(l.attempts) >= loginLimiterMaxIPs {
		if now.Before(l.nextPrune) {
			return false
		}
		l.pruneLocked(now)
		if len(l.attempts) >= loginLimiterMaxIPs {
			// Refusing a newcomer keeps memory bounded; under an address spray
			// sign-in pauses for new addresses, Bearer tokens are unaffected.
			return false
		}
	}
	recent := l.recentLocked(ip, now)
	if len(recent) >= l.limit {
		l.attempts[ip] = recent
		return false
	}
	l.attempts[ip] = append(recent, now)
	return true
}

func (l *loginLimiter) recentLocked(ip string, now time.Time) []time.Time {
	kept := l.attempts[ip][:0]
	for _, t := range l.attempts[ip] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	return kept
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	var oldest time.Time
	for ip := range l.attempts {
		recent := l.recentLocked(ip, now)
		if len(recent) == 0 {
			delete(l.attempts, ip)
			continue
		}
		l.attempts[ip] = recent
		if oldest.IsZero() || recent[0].Before(oldest) { // recent is in time order
			oldest = recent[0]
		}
	}
	l.nextPrune = time.Time{}
	if !oldest.IsZero() {
		l.nextPrune = oldest.Add(l.window)
		if floor := now.Add(l.window / 10); l.nextPrune.Before(floor) {
			l.nextPrune = floor
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are gone; all we can do is log.
		log.Printf("golog: encode response: %v", err)
	}
}
