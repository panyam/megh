package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/panyam/oneauth/utils"
)

const aud = "/projects/123/apps/meghplane"

// iapFixture stands in for IAP: a JWKS endpoint serving one ES256 key, and a
// signer for assertions made with it. This pins oneauth's validator to the
// claims IAP actually sends, so a change there that breaks IAP fails here.
type iapFixture struct {
	key *ecdsa.PrivateKey
	srv *httptest.Server
}

func newIAPFixture(t *testing.T) *iapFixture {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set := utils.JWKSet{Keys: []utils.JWK{utils.ECDSAPublicKeyToJWK("k1", "ES256", &k.PublicKey)}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(srv.Close)
	return &iapFixture{key: k, srv: srv}
}

func (f *iapFixture) token(t *testing.T, edit func(jwt.MapClaims)) string {
	t.Helper()
	c := jwt.MapClaims{
		"iss":   IAPIssuer,
		"aud":   aud,
		"sub":   "accounts.google.com:1234",
		"email": "Me@Example.com",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
	}
	if edit != nil {
		edit(c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *iapFixture) authorize(t *testing.T) func(*http.Request) (string, error) {
	t.Helper()
	a, err := NewIAP(IAPConfig{Audience: aud, AllowedEmails: []string{"me@example.com"}, KeysURL: f.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func withAssertion(tok string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if tok != "" {
		r.Header.Set(IAPHeader, tok)
	}
	return r
}

func TestIAPAdmitsAValidAssertionFromAnAllowedEmail(t *testing.T) {
	f := newIAPFixture(t)
	email, err := f.authorize(t)(withAssertion(f.token(t, nil)))
	if err != nil || email != "Me@Example.com" {
		t.Errorf("email=%q err=%v", email, err)
	}
}

func TestIAPRefuses(t *testing.T) {
	f := newIAPFixture(t)
	authorize := f.authorize(t)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	forged := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": IAPIssuer, "aud": aud, "sub": "x", "email": "me@example.com",
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	forged.Header["kid"] = "k1"
	forgedTok, _ := forged.SignedString(other)
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": IAPIssuer, "aud": aud, "sub": "x", "email": "me@example.com",
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	hs.Header["kid"] = "k1"
	hsTok, _ := hs.SignedString([]byte("guess"))

	for name, tok := range map[string]string{
		"no header":          "",
		"other app":          f.token(t, func(c jwt.MapClaims) { c["aud"] = "/projects/9/apps/other" }),
		"not IAP":            f.token(t, func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }),
		"expired":            f.token(t, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }),
		"email not allowed":  f.token(t, func(c jwt.MapClaims) { c["email"] = "someone@else.com" }),
		"no email":           f.token(t, func(c jwt.MapClaims) { delete(c, "email") }),
		"signed by another":  forgedTok,
		"HS256 with IAP kid": hsTok,
		"garbage":            "not.a.jwt",
	} {
		if email, err := authorize(withAssertion(tok)); err == nil {
			t.Errorf("%s: admitted as %q", name, email)
		}
	}
}

func TestNewIAPRefusesToStartWithoutAnAllowlistAudienceOrKeys(t *testing.T) {
	f := newIAPFixture(t)
	if _, err := NewIAP(IAPConfig{Audience: aud, KeysURL: f.srv.URL}); err == nil || !strings.Contains(err.Error(), "allowed_emails") {
		t.Errorf("empty allowlist: %v", err)
	}
	if _, err := NewIAP(IAPConfig{AllowedEmails: []string{"me@example.com"}, KeysURL: f.srv.URL}); err == nil {
		t.Error("no audience must refuse")
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if _, err := NewIAP(IAPConfig{Audience: aud, AllowedEmails: []string{"me@example.com"}, KeysURL: dead.URL}); err == nil {
		t.Error("unreachable keys must refuse to start")
	}
}
