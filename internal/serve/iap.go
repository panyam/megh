package serve

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/panyam/oneauth/apiauth"
	"github.com/panyam/oneauth/keys"
)

// Identity-Aware Proxy constants, from Google's "Securing your app with
// signed headers" guide.
const (
	IAPHeader  = "X-Goog-Iap-Jwt-Assertion"
	IAPIssuer  = "https://cloud.google.com/iap"
	IAPKeysURL = "https://www.gstatic.com/iap/verify/public_key-jwk"
)

// IAPConfig configures NewIAP.
type IAPConfig struct {
	// Audience is "/projects/<project number>/apps/<project id>" for App
	// Engine. A token minted for any other app is refused.
	Audience string
	// AllowedEmails are compared case-insensitively against the token's
	// email claim. Required: an empty list is a configuration error rather
	// than "allow everyone IAP admits".
	AllowedEmails []string
	// KeysURL and HTTPClient default to IAP's public JWKS and
	// http.DefaultClient; tests point them at a local server.
	KeysURL    string
	HTTPClient *http.Client
}

// NewIAP returns an Authorize func that admits a request only when it carries
// a valid IAP assertion for cfg.Audience whose email is allowed. It fetches
// IAP's signing keys before returning and fails if it cannot, so a deployed
// app never starts unable to check. Keys are refreshed hourly and on an
// unknown key id; signature, algorithm pinning, expiry, issuer and audience
// are checked by oneauth's validator.
//
// This is defence in depth behind IAP itself. The server holds no
// credentials, so getting past IAP would yield an empty page, but a request
// that did not come through IAP is refused here regardless.
func NewIAP(cfg IAPConfig) (func(*http.Request) (string, error), error) {
	if cfg.Audience == "" {
		return nil, errors.New("iap: audience is required")
	}
	allowed := map[string]bool{}
	for _, e := range cfg.AllowedEmails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			allowed[e] = true
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("iap: serve.allowed_emails is empty; refusing to start")
	}
	url := cfg.KeysURL
	if url == "" {
		url = IAPKeysURL
	}
	var opts []keys.JWKSOption
	if cfg.HTTPClient != nil {
		opts = append(opts, keys.WithHTTPClient(cfg.HTTPClient))
	}
	ks := keys.NewJWKSKeyStore(url, opts...)
	if err := ks.Start(); err != nil {
		return nil, fmt.Errorf("iap: %w", err)
	}
	v := apiauth.NewJWTValidator(apiauth.JWTValidatorConfig{KeyLookup: ks, Issuer: IAPIssuer, Audience: cfg.Audience})

	return func(r *http.Request) (string, error) {
		tok := r.Header.Get(IAPHeader)
		if tok == "" {
			return "", errors.New("no IAP assertion header")
		}
		resp, err := v.ValidateToken(r.Context(), &apiauth.ValidateTokenRequest{Token: tok})
		if err != nil {
			return "", err
		}
		email, _ := resp.Info.CustomClaims["email"].(string)
		if !allowed[strings.ToLower(email)] {
			return "", fmt.Errorf("%q is not in serve.allowed_emails", email)
		}
		return email, nil
	}, nil
}
