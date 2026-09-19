// Package auth is Auth0 OpenID Connect with PKCE, followed by the app's own
// server-side session.
//
// Auth0 only proves who the person is. It deliberately does not become the
// session: the app issues its own, so it can enforce idle logout, keep an
// audit trail, and revoke access without waiting for a token to expire.
//
// The cookie formats, names, timeouts and validation rules here match the
// PregoPillow web app exactly, so a browser can move between the two without
// noticing.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// b64 is base64url without padding, which is what Node's "base64url" produces.
var b64 = base64.RawURLEncoding

// AAD binds a ciphertext to where it is used, so a value lifted out of one
// place cannot be replayed into another.
func AAD(table, column, userID string) string {
	return fmt.Sprintf("%s.%s:%s", table, column, userID)
}

// EncryptText produces "v1.<iv>.<tag>.<ciphertext>", all base64url.
func EncryptText(key []byte, plain, context string) (string, error) {
	if len(key) != 32 {
		return "", errors.New("data key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, []byte(plain), []byte(context))
	// Go appends the tag; Node keeps it separate. Split it back out so the
	// wire format is identical to the web app's.
	ct, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	return strings.Join([]string{"v1", b64.EncodeToString(iv), b64.EncodeToString(tag), b64.EncodeToString(ct)}, "."), nil
}

// DecryptText reverses EncryptText and fails on any tampering.
func DecryptText(key []byte, payload, context string) (string, error) {
	parts := strings.Split(payload, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", errors.New("unsupported ciphertext format")
	}
	iv, err := b64.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	tag, err := b64.DecodeString(parts[2])
	if err != nil {
		return "", err
	}
	ct, err := b64.DecodeString(parts[3])
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	out, err := gcm.Open(nil, iv, append(ct, tag...), []byte(context))
	if err != nil {
		return "", errors.New("ciphertext failed authentication")
	}
	return string(out), nil
}

// SignToken returns "<token>.<hmac>". The signature lets a request be rejected
// as forged without touching the database.
func SignToken(token, secret string) string {
	return token + "." + mac(token, secret)
}

// VerifySignedToken returns the raw token, or "" if the signature is wrong.
func VerifySignedToken(value, secret string) string {
	if value == "" || secret == "" {
		return ""
	}
	dot := strings.LastIndex(value, ".")
	if dot <= 0 {
		return ""
	}
	token, given := value[:dot], value[dot+1:]
	expected := mac(token, secret)
	// Constant time: a timing side channel here leaks the signature byte by
	// byte, which is enough to forge one.
	if len(given) != len(expected) || subtle.ConstantTimeCompare([]byte(given), []byte(expected)) != 1 {
		return ""
	}
	return token
}

func mac(token, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(token))
	return b64.EncodeToString(h.Sum(nil))
}

// RandomToken is the opaque session token handed to the browser.
func RandomToken(n int) (string, error) {
	if n <= 0 {
		n = 32
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b64.EncodeToString(buf), nil
}

// HashToken is what the database stores. The browser's copy is never written
// down, so a leaked database cannot be used to impersonate anyone.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return b64.EncodeToString(sum[:])
}
