package auth

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Cookie names. The __Host- prefix pins a cookie to exactly this origin —
// Secure, Path=/, no Domain — so a subdomain cannot set or read it. It only
// works over https, hence the split.
const (
	sessionCookieDev  = "pp_session"
	sessionCookieProd = "__Host-pp_session"
	txCookieDev       = "pp_oidc"
	txCookieProd      = "__Host-pp_oidc"
)

// Timeouts match the web app: HIPAA automatic logoff after inactivity, plus an
// absolute cap so a session cannot live forever by being touched.
const (
	DefaultIdle     = 15 * time.Minute
	DefaultAbsolute = 12 * time.Hour
	txLifetime      = 10 * time.Minute
)

// User is who the session belongs to.
type User struct {
	ID     string
	Email  string
	Name   string
	Role   string
	IsDemo bool
}

// Store persists users and sessions. SQLite here, Postgres in the web app;
// the schema and semantics are the same.
type Store struct {
	DB       *sql.DB
	Idle     time.Duration
	Absolute time.Duration
}

func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{DB: db, Idle: DefaultIdle, Absolute: DefaultAbsolute}
	return s, s.migrate()
}

func (s *Store) migrate() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id           TEXT PRIMARY KEY,
  auth0_sub    TEXT UNIQUE,
  email        TEXT UNIQUE NOT NULL,
  name         TEXT NOT NULL,
  role         TEXT NOT NULL DEFAULT 'patient',
  is_demo      INTEGER NOT NULL DEFAULT 0,
  timezone     TEXT NOT NULL DEFAULT 'UTC',
  created_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip           TEXT,
  user_agent   TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_hash ON sessions(token_hash);`)
	return err
}

// ResolveAuth0User finds or creates the account behind an Auth0 identity.
//
// Matching falls back to email so a person who signed up with a password and
// later uses Google lands on the same account rather than a duplicate. That is
// only safe because the caller has already refused unverified emails.
func (s *Store) ResolveAuth0User(id Identity, timezone string) (User, bool, error) {
	var u User
	var isDemo int
	err := s.DB.QueryRow(
		`SELECT id, email, name, role, is_demo FROM users WHERE auth0_sub = ?`, id.Sub,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &isDemo)
	if err == nil {
		u.IsDemo = isDemo == 1
		return u, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, false, err
	}

	// Same person, different connection: link rather than duplicate.
	err = s.DB.QueryRow(
		`SELECT id, email, name, role, is_demo FROM users WHERE email = ?`, id.Email,
	).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &isDemo)
	if err == nil {
		if _, err := s.DB.Exec(`UPDATE users SET auth0_sub = ? WHERE id = ?`, id.Sub, u.ID); err != nil {
			return User{}, false, err
		}
		u.IsDemo = isDemo == 1
		return u, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, false, err
	}

	uid, err := RandomToken(12)
	if err != nil {
		return User{}, false, err
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := s.DB.Exec(
		`INSERT INTO users (id, auth0_sub, email, name, role, is_demo, timezone, created_at)
		 VALUES (?,?,?,?,'patient',0,?,?)`,
		uid, id.Sub, id.Email, id.Name, timezone, time.Now().Unix(),
	); err != nil {
		return User{}, false, err
	}
	return User{ID: uid, Email: id.Email, Name: id.Name, Role: "patient"}, true, nil
}

// Create issues a session and returns the opaque token for the browser.
func (s *Store) Create(userID, ip, ua string) (string, error) {
	token, err := RandomToken(32)
	if err != nil {
		return "", err
	}
	now := time.Now()
	// Sweep expired rows on the way past; no scheduler needed.
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now.Unix())
	_, err = s.DB.Exec(
		`INSERT INTO sessions (user_id, token_hash, expires_at, last_seen_at, ip, user_agent)
		 VALUES (?,?,?,?,?,?)`,
		userID, HashToken(token), now.Add(s.Absolute).Unix(), now.Unix(), ip, ua,
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// Read resolves a token to its user.
//
// touch slides the idle timer, and background streams pass false: a dashboard
// left open on a nightstand must still log itself out.
func (s *Store) Read(token string, touch bool) (User, bool) {
	if token == "" {
		return User{}, false
	}
	now := time.Now().Unix()
	// Round UP to whole seconds. Truncating means any idle window under one
	// second becomes zero, and "last_seen_at > now - 0" is false for a session
	// created this instant — every session invalid the moment it is made.
	idle := int64((s.Idle + time.Second - 1) / time.Second)
	if s.Idle <= 0 {
		idle = int64(DefaultIdle / time.Second)
	}

	var u User
	var isDemo int
	var sid int64
	err := s.DB.QueryRow(
		`SELECT s.id, u.id, u.email, u.name, u.role, u.is_demo
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = ? AND s.expires_at > ? AND s.last_seen_at > ?`,
		HashToken(token), now, now-idle,
	).Scan(&sid, &u.ID, &u.Email, &u.Name, &u.Role, &isDemo)
	if err != nil {
		return User{}, false
	}
	u.IsDemo = isDemo == 1
	if touch {
		_, _ = s.DB.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now, sid)
	}
	return u, true
}

// Destroy removes one session.
func (s *Store) Destroy(token string) {
	if token != "" {
		_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	}
}

// ------------------------------------------------------------------ cookies

// CookieName picks the __Host- variant when the origin is https, because that
// prefix is rejected by browsers over plain http.
func CookieName(secure bool) string {
	if secure {
		return sessionCookieProd
	}
	return sessionCookieDev
}

func TxCookieName(secure bool) string {
	if secure {
		return txCookieProd
	}
	return txCookieDev
}

// IsSecure reports whether the app's own base URL is https.
func IsSecure(appBaseURL string) bool { return strings.HasPrefix(appBaseURL, "https://") }

func setCookie(w http.ResponseWriter, name, value string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		// Lax, not Strict: the Auth0 callback is a top-level GET from another
		// origin, and Strict would drop the cookie exactly then.
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func readCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
