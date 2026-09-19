package auth

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Service wires the provider, the session store and the secrets together.
type Service struct {
	Provider      *Provider
	Sessions      *Store
	SessionSecret string
	DataKey       []byte // 32 bytes, AES-256
	Logger        *log.Logger

	limiter rateLimiter
}

func (s *Service) Enabled() bool {
	return s != nil && s.Provider != nil && s.Provider.cfg.Enabled() &&
		s.Sessions != nil && s.SessionSecret != "" && len(s.DataKey) == 32
}

func (s *Service) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

func (s *Service) secure() bool { return IsSecure(s.Provider.cfg.AppBaseURL) }

// back sends the browser to the login page with a short machine-readable
// notice. The reason is never spelled out to the visitor: "no such account"
// and "wrong password" must look identical or the page becomes an oracle for
// which email addresses exist.
func (s *Service) back(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/login?notice="+url.QueryEscape(notice), http.StatusFound)
}

// Routes registers everything under /auth plus the two pages.
func (s *Service) Routes(mux *http.ServeMux, pages func(name string) ([]byte, error)) {
	mux.HandleFunc("/auth/login", s.handleLogin)
	mux.HandleFunc("/auth/callback", s.handleCallback)
	mux.HandleFunc("/auth/logout", s.handleLogout)
	mux.HandleFunc("/login", pageHandler(pages, "login.html"))
	mux.HandleFunc("/signup", pageHandler(pages, "signup.html"))
	mux.HandleFunc("/api/me", s.handleMe)
}

func pageHandler(pages func(string) ([]byte, error), name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := pages(name)
		if err != nil {
			http.Error(w, "page not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	}
}

// ------------------------------------------------------------------ /auth/login

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled() {
		s.back(w, r, "auth-unavailable")
		return
	}
	if !s.limiter.allow("start:"+clientIP(r), 30, 10*time.Minute) {
		s.back(w, r, "auth-limit")
		return
	}
	q := r.URL.Query()
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	authURL, tx, err := s.Provider.StartAuthorization(
		ctx, SafeNext(q.Get("next")), TimezoneFrom(q.Get("timezone")),
		q.Get("screen_hint") == "signup",
	)
	if err != nil {
		s.logf("auth0 start failed: %v", err)
		s.back(w, r, "auth-unavailable")
		return
	}
	if err := s.saveTransaction(w, tx); err != nil {
		s.logf("auth0 transaction: %v", err)
		s.back(w, r, "auth-unavailable")
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Service) saveTransaction(w http.ResponseWriter, tx Transaction) error {
	raw, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	// Encrypted, not just signed: the PKCE verifier is a secret, and a signed
	// cookie is readable by anyone holding it.
	sealed, err := EncryptText(s.DataKey, string(raw), AAD("oidc", "tx", "browser"))
	if err != nil {
		return err
	}
	setCookie(w, TxCookieName(s.secure()), sealed, int(txLifetime/time.Second), s.secure())
	return nil
}

// takeTransaction reads and DELETES the cookie. Single use: a replayed
// callback must not be able to reuse the same state and nonce.
func (s *Service) takeTransaction(w http.ResponseWriter, r *http.Request) (Transaction, bool) {
	name := TxCookieName(s.secure())
	raw := readCookie(r, name)
	clearCookie(w, name, s.secure())
	if raw == "" {
		return Transaction{}, false
	}
	plain, err := DecryptText(s.DataKey, raw, AAD("oidc", "tx", "browser"))
	if err != nil {
		return Transaction{}, false
	}
	var tx Transaction
	if err := json.Unmarshal([]byte(plain), &tx); err != nil {
		return Transaction{}, false
	}
	if tx.Exp <= time.Now().UnixMilli() {
		return Transaction{}, false
	}
	return tx, true
}

// --------------------------------------------------------------- /auth/callback

var shortCode = regexp.MustCompile(`^[a-z_]{1,40}$`)

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled() {
		s.back(w, r, "auth-unavailable")
		return
	}
	if !s.limiter.allow("callback:"+clientIP(r), 30, 10*time.Minute) {
		s.back(w, r, "auth-limit")
		return
	}
	q := r.URL.Query()

	if e := q.Get("error"); e != "" {
		// Auth0 refused: cancelled, blocked, MFA failed. Log only the short
		// code — the description can contain whatever the IdP chose to put
		// there, and it ends up in our logs.
		if !shortCode.MatchString(e) {
			e = "error"
		}
		s.logf("auth0 denied: %s", e)
		s.back(w, r, "auth-denied")
		return
	}

	// Taken (and deleted) before any validation, so a failed attempt cannot be
	// retried against the same transaction.
	tx, ok := s.takeTransaction(w, r)
	if !ok {
		s.back(w, r, "auth-expired")
		return
	}
	if q.Get("state") == "" || q.Get("state") != tx.State {
		s.logf("auth0 callback: state mismatch")
		s.back(w, r, "auth-failed")
		return
	}
	code := q.Get("code")
	if code == "" {
		s.back(w, r, "auth-failed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id, err := s.Provider.CompleteAuthorization(ctx, code, tx)
	if err != nil {
		s.logf("auth0 callback validation failed: %v", err)
		s.back(w, r, "auth-failed")
		return
	}
	if id.Email == "" {
		s.back(w, r, "auth-failed")
		return
	}
	// An unverified email must never create or link an account: anyone can
	// claim an address they do not own, and linking is how they would take
	// over someone else's.
	if !id.EmailVerified {
		s.back(w, r, "auth-unverified")
		return
	}

	user, created, err := s.Sessions.ResolveAuth0User(id, tx.Timezone)
	if err != nil {
		s.logf("resolve user: %v", err)
		s.back(w, r, "auth-failed")
		return
	}
	token, err := s.Sessions.Create(user.ID, clientIP(r), r.UserAgent())
	if err != nil {
		s.logf("create session: %v", err)
		s.back(w, r, "auth-failed")
		return
	}
	setCookie(w, CookieName(s.secure()), SignToken(token, s.SessionSecret),
		int(s.Sessions.Absolute/time.Second), s.secure())

	s.logf("auth.login ok: user=%s created=%v mfa=%v", user.ID, created, id.MFA)
	http.Redirect(w, r, SafeNext(tx.Next), http.StatusFound)
}

// ----------------------------------------------------------------- /auth/logout

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	name := CookieName(s.secure())
	if token := VerifySignedToken(readCookie(r, name), s.SessionSecret); token != "" && s.Sessions != nil {
		s.Sessions.Destroy(token)
	}
	clearCookie(w, name, s.secure())
	if s.Enabled() {
		// End the Auth0 SSO session too, or the next person on this device is
		// signed straight back in without being asked.
		http.Redirect(w, r, s.Provider.LogoutURL(), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login?notice=signed-out", http.StatusFound)
}

// -------------------------------------------------------------------- session

// Current returns the signed-in user, sliding the idle timer.
func (s *Service) Current(w http.ResponseWriter, r *http.Request, touch bool) (User, bool) {
	if s == nil || s.Sessions == nil {
		return User{}, false
	}
	token := VerifySignedToken(readCookie(r, CookieName(s.secure())), s.SessionSecret)
	if token == "" {
		return User{}, false
	}
	return s.Sessions.Read(token, touch)
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	u, ok := s.Current(w, r, false)
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": false})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": true, "name": u.Name, "email": u.Email,
		"role": u.Role, "is_demo": u.IsDemo,
	})
}

// Require gates a handler behind a session, sending anonymous visitors to the
// login page with a next parameter so they land where they were going.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.Current(w, r, true); ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

// ------------------------------------------------------------------- helpers

// SafeNext refuses anything that is not a local path, so ?next= cannot be used
// to bounce someone to another site carrying the look of a trusted redirect.
func SafeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") ||
		strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/dashboard"
	}
	return next
}

var tzPattern = regexp.MustCompile(`^[A-Za-z]+(?:[/_+-][A-Za-z0-9_+-]+){0,3}$`)

func TimezoneFrom(tz string) string {
	if tz == "" || len(tz) > 64 || !tzPattern.MatchString(tz) {
		return "UTC"
	}
	return tz
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.SplitN(f, ",", 2)[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// rateLimiter is a fixed-window counter, enough to blunt credential stuffing
// against the callback without a dependency.
type rateLimiter struct {
	mu sync.Mutex
	at map[string]*window
}

type window struct {
	count int
	until time.Time
}

func (l *rateLimiter) allow(key string, limit int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at == nil {
		l.at = map[string]*window{}
	}
	now := time.Now()
	w, ok := l.at[key]
	if !ok || now.After(w.until) {
		l.at[key] = &window{count: 1, until: now.Add(per)}
		return true
	}
	w.count++
	return w.count <= limit
}
