package auth

import (
	"crypto/rand"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// ------------------------------------------------------------------ crypto

func TestEncryptRoundTrip(t *testing.T) {
	k := key(t)
	ct, err := EncryptText(k, "hello", AAD("oidc", "tx", "browser"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ct, "v1.") || len(strings.Split(ct, ".")) != 4 {
		t.Errorf("wire format is not v1.<iv>.<tag>.<ct>: %s", ct)
	}
	back, err := DecryptText(k, ct, AAD("oidc", "tx", "browser"))
	if err != nil || back != "hello" {
		t.Fatalf("round trip failed: %q %v", back, err)
	}
}

// AAD binds a ciphertext to where it is used. Without it, a value lifted from
// one context could be replayed into another.
func TestDecryptRejectsWrongContext(t *testing.T) {
	k := key(t)
	ct, _ := EncryptText(k, "hello", AAD("oidc", "tx", "browser"))
	if _, err := DecryptText(k, ct, AAD("oidc", "tx", "someone-else")); err == nil {
		t.Error("a different AAD must not decrypt")
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	k := key(t)
	ct, _ := EncryptText(k, "hello", "ctx")
	parts := strings.Split(ct, ".")
	parts[3] = b64.EncodeToString([]byte("tampered-ciphertext"))
	if _, err := DecryptText(k, strings.Join(parts, "."), "ctx"); err == nil {
		t.Error("tampered ciphertext must fail authentication")
	}
	if _, err := DecryptText(key(t), ct, "ctx"); err == nil {
		t.Error("a different key must not decrypt")
	}
}

func TestSignedTokenRoundTrip(t *testing.T) {
	signed := SignToken("abc123", "a-very-long-session-secret-value")
	if got := VerifySignedToken(signed, "a-very-long-session-secret-value"); got != "abc123" {
		t.Errorf("got %q", got)
	}
}

// The signature is what lets a forged cookie be rejected without a database
// call, so every way of faking one must fail.
func TestSignedTokenRejectsForgery(t *testing.T) {
	secret := "a-very-long-session-secret-value"
	signed := SignToken("abc123", secret)
	cases := map[string]string{
		"wrong secret":  VerifySignedToken(signed, "another-secret-entirely-here!!"),
		"no signature":  VerifySignedToken("abc123", secret),
		"empty":         VerifySignedToken("", secret),
		"bad mac":       VerifySignedToken("abc123.bogus", secret),
		"swapped token": VerifySignedToken("abc124."+strings.SplitN(signed, ".", 2)[1], secret),
		"leading dot":   VerifySignedToken(".mac", secret),
		"no secret":     VerifySignedToken(signed, ""),
	}
	for name, got := range cases {
		if got != "" {
			t.Errorf("%s was accepted, returning %q", name, got)
		}
	}
}

func TestHashTokenIsNotTheToken(t *testing.T) {
	tok, _ := RandomToken(32)
	if HashToken(tok) == tok {
		t.Fatal("the stored hash equals the token; a leaked database would be a leaked credential")
	}
	if HashToken(tok) != HashToken(tok) {
		t.Error("hash is not stable")
	}
}

// ------------------------------------------------------------------ sessions

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/a.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionLifecycle(t *testing.T) {
	s := newStore(t)
	u, created, err := s.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@example.com", Name: "A"}, "Europe/London")
	if err != nil || !created {
		t.Fatalf("first login should create: %v created=%v", err, created)
	}
	tok, err := s.Create(u.ID, "1.2.3.4", "test")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Read(tok, true)
	if !ok || got.ID != u.ID || got.Email != "a@example.com" {
		t.Fatalf("read back %+v ok=%v", got, ok)
	}
	s.Destroy(tok)
	if _, ok := s.Read(tok, true); ok {
		t.Error("destroyed session still resolves")
	}
}

// The same person arriving through a second Auth0 connection must land on the
// same account, not a duplicate with none of their history.
func TestSecondConnectionLinksByEmail(t *testing.T) {
	s := newStore(t)
	first, _, _ := s.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@example.com", Name: "A"}, "UTC")
	second, created, err := s.ResolveAuth0User(Identity{Sub: "google-oauth2|9", Email: "a@example.com", Name: "A"}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("should have linked to the existing account, not created a second")
	}
	if second.ID != first.ID {
		t.Errorf("linked to %s, want %s", second.ID, first.ID)
	}
}

// HIPAA automatic logoff: a dashboard left open on a nightstand must expire.
func TestSessionExpiresWhenIdle(t *testing.T) {
	s := newStore(t)
	s.Idle = time.Second
	u, _, _ := s.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@b.c", Name: "A"}, "UTC")
	tok, _ := s.Create(u.ID, "", "")
	if _, ok := s.Read(tok, false); !ok {
		t.Fatal("should be valid immediately")
	}
	time.Sleep(2500 * time.Millisecond) // last_seen_at has second resolution
	if _, ok := s.Read(tok, true); ok {
		t.Error("an idle session must expire")
	}
}

// touch=false exists so background streams do not keep a session alive.
func TestReadWithoutTouchDoesNotSlideTheTimer(t *testing.T) {
	s := newStore(t)
	u, _, _ := s.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@b.c", Name: "A"}, "UTC")
	tok, _ := s.Create(u.ID, "", "")
	var before, after int64
	_ = s.DB.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash=?`, HashToken(tok)).Scan(&before)
	time.Sleep(1100 * time.Millisecond)
	s.Read(tok, false)
	_ = s.DB.QueryRow(`SELECT last_seen_at FROM sessions WHERE token_hash=?`, HashToken(tok)).Scan(&after)
	if after != before {
		t.Errorf("last_seen_at moved from %d to %d without touch", before, after)
	}
}

// ------------------------------------------------------------------ handlers

func svc(t *testing.T) *Service {
	t.Helper()
	return &Service{
		Provider: NewProvider(Config{
			Domain: "example.auth0.com", ClientID: "cid", ClientSecret: "sec",
			AppBaseURL: "http://localhost:8080",
		}),
		Sessions:      newStore(t),
		SessionSecret: "a-very-long-session-secret-value",
		DataKey:       key(t),
	}
}

func TestRequireRedirectsAnonymousWithNext(t *testing.T) {
	s := svc(t)
	rr := httptest.NewRecorder()
	s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("anonymous request reached the protected handler")
	})).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard?x=1", nil))

	if rr.Code != http.StatusFound {
		t.Fatalf("code = %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?next=") || !strings.Contains(loc, "dashboard") {
		t.Errorf("redirect = %q; the visitor should land back where they were going", loc)
	}
}

func TestRequireAllowsAValidSession(t *testing.T) {
	s := svc(t)
	u, _, _ := s.Sessions.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@b.c", Name: "A"}, "UTC")
	tok, _ := s.Sessions.Create(u.ID, "", "")

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: CookieName(false), Value: SignToken(tok, s.SessionSecret)})
	rr := httptest.NewRecorder()
	reached := false
	s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })).ServeHTTP(rr, req)
	if !reached {
		t.Errorf("a valid session was rejected: %d %s", rr.Code, rr.Header().Get("Location"))
	}
}

// A cookie whose signature does not verify must never reach the database.
func TestForgedCookieIsRejected(t *testing.T) {
	s := svc(t)
	u, _, _ := s.Sessions.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@b.c", Name: "A"}, "UTC")
	tok, _ := s.Sessions.Create(u.ID, "", "")

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: CookieName(false), Value: tok + ".forged"})
	rr := httptest.NewRecorder()
	s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a forged cookie reached the protected handler")
	})).ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Errorf("code = %d", rr.Code)
	}
}

// The transaction cookie is single use: a replayed callback must not be able
// to reuse the same state, nonce and PKCE verifier.
func TestTransactionCookieIsSingleUse(t *testing.T) {
	s := svc(t)
	tx := Transaction{State: "st", Nonce: "no", Verifier: "vf", Next: "/dashboard",
		Exp: time.Now().Add(time.Minute).UnixMilli()}

	w1 := httptest.NewRecorder()
	if err := s.saveTransaction(w1, tx); err != nil {
		t.Fatal(err)
	}
	cookie := w1.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("tx cookie must be HttpOnly and SameSite=Lax, got %+v", cookie)
	}
	if strings.Contains(cookie.Value, "vf") {
		t.Error("the PKCE verifier is readable in the cookie; it must be encrypted")
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	req.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	got, ok := s.takeTransaction(w2, req)
	if !ok || got.State != "st" || got.Verifier != "vf" {
		t.Fatalf("take failed: %+v ok=%v", got, ok)
	}
	// It must have been cleared on the way out.
	cleared := w2.Result().Cookies()[0]
	if cleared.MaxAge >= 0 {
		t.Errorf("tx cookie was not deleted: MaxAge=%d", cleared.MaxAge)
	}
}

func TestExpiredTransactionIsRefused(t *testing.T) {
	s := svc(t)
	tx := Transaction{State: "st", Exp: time.Now().Add(-time.Second).UnixMilli()}
	w := httptest.NewRecorder()
	_ = s.saveTransaction(w, tx)
	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	req.AddCookie(w.Result().Cookies()[0])
	if _, ok := s.takeTransaction(httptest.NewRecorder(), req); ok {
		t.Error("an expired transaction was accepted")
	}
}

// A state mismatch is CSRF on the callback and must be refused.
func TestCallbackRefusesStateMismatch(t *testing.T) {
	s := svc(t)
	tx := Transaction{State: "expected", Nonce: "n", Verifier: "v",
		Exp: time.Now().Add(time.Minute).UnixMilli()}
	w := httptest.NewRecorder()
	_ = s.saveTransaction(w, tx)

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=attacker", nil)
	req.AddCookie(w.Result().Cookies()[0])
	rr := httptest.NewRecorder()
	s.handleCallback(rr, req)
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "auth-failed") {
		t.Errorf("state mismatch should fail, got %q", loc)
	}
}

func TestCallbackWithNoTransactionIsExpired(t *testing.T) {
	s := svc(t)
	rr := httptest.NewRecorder()
	s.handleCallback(rr, httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=x", nil))
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "auth-expired") {
		t.Errorf("got %q", loc)
	}
}

// ?next= must not become an open redirect: a link that looks like ours but
// lands on someone else's site is a phishing primitive.
func TestSafeNextRefusesOffsiteTargets(t *testing.T) {
	for _, bad := range []string{
		"https://evil.example.com", "//evil.example.com", "/\\evil.example.com",
		"javascript:alert(1)", "", "http://evil",
	} {
		if got := SafeNext(bad); got != "/dashboard" {
			t.Errorf("SafeNext(%q) = %q, want /dashboard", bad, got)
		}
	}
	if got := SafeNext("/history?page=2"); got != "/history?page=2" {
		t.Errorf("a local path should survive, got %q", got)
	}
}

func TestTimezoneFromRejectsJunk(t *testing.T) {
	if got := TimezoneFrom("Europe/London"); got != "Europe/London" {
		t.Errorf("got %q", got)
	}
	for _, bad := range []string{"", "'; DROP TABLE users;--", strings.Repeat("a", 80), "../../etc"} {
		if got := TimezoneFrom(bad); got != "UTC" {
			t.Errorf("TimezoneFrom(%q) = %q, want UTC", bad, got)
		}
	}
}

// The __Host- prefix only works over https; sending it over http means the
// browser silently drops the cookie and nobody can ever sign in.
func TestHostPrefixOnlyWhenSecure(t *testing.T) {
	if CookieName(true) != "__Host-pp_session" || TxCookieName(true) != "__Host-pp_oidc" {
		t.Error("https should use the __Host- prefixed names")
	}
	if CookieName(false) != "pp_session" || TxCookieName(false) != "pp_oidc" {
		t.Error("http must not use __Host-, the browser would drop it")
	}
	if !IsSecure("https://x.example") || IsSecure("http://localhost:8080") {
		t.Error("IsSecure is wrong")
	}
}

func TestRedirectURIIsDerivedFromAppBaseURL(t *testing.T) {
	c := Config{AppBaseURL: "http://localhost:8080/"}
	if got := c.RedirectURI(); got != "http://localhost:8080/auth/callback" {
		t.Errorf("got %q; a trailing slash must not double up", got)
	}
}

func TestDisabledWithoutFullConfig(t *testing.T) {
	full := svc(t)
	if !full.Enabled() {
		t.Fatal("a fully configured service should be enabled")
	}
	short := svc(t)
	short.DataKey = []byte("too-short")
	if short.Enabled() {
		t.Error("a bad data key must disable auth rather than half-work")
	}
	noSecret := svc(t)
	noSecret.SessionSecret = ""
	if noSecret.Enabled() {
		t.Error("no session secret must disable auth")
	}
}

func TestRateLimiter(t *testing.T) {
	var l rateLimiter
	for i := 0; i < 3; i++ {
		if !l.allow("k", 3, time.Minute) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if l.allow("k", 3, time.Minute) {
		t.Error("the fourth request should be refused")
	}
	if !l.allow("other", 3, time.Minute) {
		t.Error("a different key has its own window")
	}
}

// A sub-second idle window must not round down to zero, which would make every
// session invalid the instant it was created.
func TestSubSecondIdleDoesNotLockEveryoneOut(t *testing.T) {
	s := newStore(t)
	s.Idle = 50 * time.Millisecond
	u, _, _ := s.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@b.c", Name: "A"}, "UTC")
	tok, _ := s.Create(u.ID, "", "")
	if _, ok := s.Read(tok, false); !ok {
		t.Error("a freshly created session was rejected; idle rounded down to zero")
	}
}

func TestZeroIdleFallsBackToTheDefault(t *testing.T) {
	s := newStore(t)
	s.Idle = 0
	u, _, _ := s.ResolveAuth0User(Identity{Sub: "auth0|1", Email: "a@b.c", Name: "A"}, "UTC")
	tok, _ := s.Create(u.ID, "", "")
	if _, ok := s.Read(tok, false); !ok {
		t.Error("an unset idle window should fall back to the default, not lock everyone out")
	}
}
