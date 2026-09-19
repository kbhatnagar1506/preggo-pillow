package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config is everything the OIDC flow needs.
type Config struct {
	Domain       string // e.g. dev-xxxx.us.auth0.com
	ClientID     string
	ClientSecret string
	AppBaseURL   string // public origin, e.g. http://localhost:8080
	HTTP         *http.Client
}

func (c Config) Enabled() bool {
	return c.Domain != "" && c.ClientID != "" && c.ClientSecret != "" && c.AppBaseURL != ""
}

var loopback = regexp.MustCompile(`^(localhost|127\.0\.0\.1)(:\d+)?$`)

// issuerBase allows plain http only for loopback, where there is nothing in
// between to intercept it. Everything else must be https.
func (c Config) issuerBase() string {
	scheme := "https"
	if loopback.MatchString(c.Domain) {
		scheme = "http"
	}
	return scheme + "://" + c.Domain
}

// RedirectURI is what Auth0 must have registered. A mismatch here is the most
// common setup failure and Auth0 rejects it before the user sees anything.
func (c Config) RedirectURI() string { return strings.TrimRight(c.AppBaseURL, "/") + "/auth/callback" }

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 12 * time.Second}
}

// ---------------------------------------------------------------- discovery

type discovery struct {
	Issuer        string `json:"issuer"`
	AuthzEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
}

type Provider struct {
	cfg  Config
	mu   sync.Mutex
	doc  *discovery
	keys map[string]*rsa.PublicKey
	when time.Time
}

func NewProvider(cfg Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Config() Config { return p.cfg }

// discover fetches and caches the OpenID configuration. Failures are not
// cached, so a provider that was briefly unreachable recovers on its own.
func (p *Provider) discover(ctx context.Context) (*discovery, error) {
	p.mu.Lock()
	if p.doc != nil {
		d := p.doc
		p.mu.Unlock()
		return d, nil
	}
	p.mu.Unlock()

	u := p.cfg.issuerBase() + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.cfg.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc discovery: %s", resp.Status)
	}
	var d discovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if d.AuthzEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return nil, errors.New("oidc discovery: incomplete document")
	}
	p.mu.Lock()
	p.doc = &d
	p.mu.Unlock()
	return &d, nil
}

type jwksDoc struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

// jwks fetches the signing keys, re-fetching when a key id is unknown: Auth0
// rotates keys, and a cached set that never refreshes fails every login from
// the moment it rotates.
func (p *Provider) jwks(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	if k, ok := p.keys[kid]; ok && time.Since(p.when) < time.Hour {
		p.mu.Unlock()
		return k, nil
	}
	p.mu.Unlock()

	d, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.JWKSURI, nil)
	resp, err := p.cfg.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	defer resp.Body.Close()
	var doc jwksDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
	}
	p.mu.Lock()
	p.keys, p.when = keys, time.Now()
	p.mu.Unlock()

	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("jwks: no key %q", kid)
}

// ------------------------------------------------------------- authorization

// Transaction is the short-lived state carried between /auth/login and
// /auth/callback. It lives only in an encrypted, single-use cookie.
type Transaction struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Timezone string `json:"timezone"`
	Exp      int64  `json:"exp"` // unix millis
}

// StartAuthorization builds the Auth0 URL and the transaction to remember.
//
// PKCE (S256), state and nonce are all present because each defends a
// different attack: PKCE stops a stolen code being redeemed, state stops CSRF
// on the callback, and nonce stops an ID token from one session being replayed
// into another.
func (p *Provider) StartAuthorization(ctx context.Context, next, timezone string, signup bool) (string, Transaction, error) {
	d, err := p.discover(ctx)
	if err != nil {
		return "", Transaction{}, err
	}
	verifier, err := RandomToken(32)
	if err != nil {
		return "", Transaction{}, err
	}
	state, err := RandomToken(16)
	if err != nil {
		return "", Transaction{}, err
	}
	nonce, err := RandomToken(16)
	if err != nil {
		return "", Transaction{}, err
	}
	sum := sha256.Sum256([]byte(verifier))

	q := url.Values{}
	q.Set("client_id", p.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", p.cfg.RedirectURI())
	q.Set("scope", "openid profile email")
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("nonce", nonce)
	if signup {
		q.Set("screen_hint", "signup")
	}

	tx := Transaction{
		State: state, Nonce: nonce, Verifier: verifier,
		Next: next, Timezone: timezone,
		Exp: time.Now().Add(10 * time.Minute).UnixMilli(),
	}
	return d.AuthzEndpoint + "?" + q.Encode(), tx, nil
}

// Identity is what the ID token proves.
type Identity struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
	MFA           bool
}

type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// CompleteAuthorization exchanges the code and validates the ID token fully:
// signature, issuer, audience, expiry and nonce. Any mismatch is an error —
// skipping one of these is how OIDC implementations get broken into.
func (p *Provider) CompleteAuthorization(ctx context.Context, code string, tx Transaction) (Identity, error) {
	d, err := p.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", p.cfg.ClientID)
	form.Set("client_secret", p.cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", p.cfg.RedirectURI())
	form.Set("code_verifier", tx.Verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.cfg.client().Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return Identity{}, fmt.Errorf("token exchange: %w", err)
	}
	if tr.Error != "" {
		return Identity{}, fmt.Errorf("token exchange: %s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.IDToken == "" {
		return Identity{}, errors.New("token exchange: no id_token")
	}
	return p.verifyIDToken(ctx, tr.IDToken, tx.Nonce)
}

type idClaims struct {
	Iss           string   `json:"iss"`
	Sub           string   `json:"sub"`
	Aud           any      `json:"aud"`
	Exp           int64    `json:"exp"`
	Iat           int64    `json:"iat"`
	Nonce         string   `json:"nonce"`
	Email         string   `json:"email"`
	EmailVerified bool     `json:"email_verified"`
	Name          string   `json:"name"`
	Nickname      string   `json:"nickname"`
	AMR           []string `json:"amr"`
}

func (p *Provider) verifyIDToken(ctx context.Context, token, nonce string) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, errors.New("id token is not a JWS")
	}
	headRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Identity{}, err
	}
	var head struct{ Alg, Kid string }
	if err := json.Unmarshal(headRaw, &head); err != nil {
		return Identity{}, err
	}
	// "none" and HMAC algorithms must be refused outright: accepting alg from
	// the token lets an attacker choose how their own forgery is checked.
	if head.Alg != "RS256" {
		return Identity{}, fmt.Errorf("id token alg %q is not RS256", head.Alg)
	}
	key, err := p.jwks(ctx, head.Kid)
	if err != nil {
		return Identity{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, err
	}
	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, signed[:], sig); err != nil {
		return Identity{}, errors.New("id token signature is invalid")
	}

	bodyRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, err
	}
	var c idClaims
	if err := json.Unmarshal(bodyRaw, &c); err != nil {
		return Identity{}, err
	}
	if c.Sub == "" {
		return Identity{}, errors.New("id token has no subject")
	}
	if want := p.cfg.issuerBase() + "/"; c.Iss != want && c.Iss != strings.TrimRight(want, "/") {
		return Identity{}, fmt.Errorf("id token issuer %q is not %q", c.Iss, want)
	}
	if !audienceContains(c.Aud, p.cfg.ClientID) {
		return Identity{}, errors.New("id token audience is not this client")
	}
	now := time.Now().Unix()
	if c.Exp != 0 && now > c.Exp+60 { // small leeway for clock skew
		return Identity{}, errors.New("id token has expired")
	}
	if nonce != "" && c.Nonce != nonce {
		return Identity{}, errors.New("id token nonce does not match this login")
	}

	email := strings.ToLower(strings.TrimSpace(c.Email))
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = strings.TrimSpace(c.Nickname)
	}
	if name == "" && email != "" {
		name = strings.SplitN(email, "@", 2)[0]
	}
	if name == "" {
		name = "Member"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	mfa := false
	for _, a := range c.AMR {
		if a == "mfa" {
			mfa = true
		}
	}
	return Identity{Sub: c.Sub, Email: email, EmailVerified: c.EmailVerified, Name: name, MFA: mfa}, nil
}

func audienceContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// LogoutURL ends the Auth0 single-sign-on session too, so the next person on a
// shared device is asked to sign in rather than silently resuming.
func (p *Provider) LogoutURL() string {
	u, _ := url.Parse(p.cfg.issuerBase() + "/v2/logout")
	q := u.Query()
	q.Set("client_id", p.cfg.ClientID)
	q.Set("returnTo", strings.TrimRight(p.cfg.AppBaseURL, "/")+"/login")
	u.RawQuery = q.Encode()
	return u.String()
}

var _ = rand.Reader
