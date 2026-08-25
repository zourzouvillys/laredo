// Package oidc provides an Authorizer that verifies bearer tokens against an
// OIDC issuer's published keys.
//
// It is the batteries-included option: point it at an issuer, name the
// audience, and every request must carry a valid token. Deployments whose
// issuer is not OIDC — one using an X.509 chain, say — implement
// service.Authorizer directly instead; this package is a convenience, not the
// only way in.
package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo/service"
)

// Config configures the authorizer.
type Config struct {
	// Issuer is the OIDC issuer URL. Its discovery document is fetched from
	// <Issuer>/.well-known/openid-configuration and its keys from the jwks_uri
	// named there.
	Issuer string

	// Audience, when set, must appear in the token's aud claim.
	Audience string

	// HTTPClient fetches discovery and keys. Defaults to http.DefaultClient.
	HTTPClient *http.Client

	// RefreshInterval bounds how long a fetched key set is reused.
	// Defaults to 15 minutes.
	RefreshInterval time.Duration

	// Leeway allows for clock skew when checking exp and nbf.
	// Defaults to 60 seconds.
	Leeway time.Duration

	// Authorize decides what a verified caller may do. It receives the
	// request and the token's claims.
	//
	// It is required. A verified token says who is calling, not what they may
	// reach — and defaulting that to "anything" is how a table ends up
	// readable by every workload that happens to hold a token for this issuer.
	Authorize func(ctx context.Context, req service.AuthRequest, claims Claims) (service.AuthDecision, error)
}

// Claims is the decoded token payload. Registered claims are lifted out; the
// rest stay in Raw.
type Claims struct {
	Subject  string
	Issuer   string
	Audience []string
	Expiry   time.Time
	IssuedAt time.Time
	Scope    string
	Raw      map[string]any
}

// HasScope reports whether the space-delimited scope claim contains s.
func (c Claims) HasScope(s string) bool {
	for _, have := range strings.Fields(c.Scope) {
		if have == s {
			return true
		}
	}
	return false
}

// Authorizer verifies bearer tokens and delegates the decision.
type Authorizer struct {
	cfg Config

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	jwksURI   string
}

// New builds an Authorizer. Discovery happens lazily on the first request, so
// construction does not depend on the issuer being reachable yet.
func New(cfg Config) (*Authorizer, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("oidc: Issuer is required")
	}
	if cfg.Authorize == nil {
		return nil, errors.New("oidc: Authorize is required — a verified token establishes identity, not permission")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 15 * time.Minute
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = 60 * time.Second
	}
	return &Authorizer{cfg: cfg, keys: map[string]*rsa.PublicKey{}}, nil
}

// Authorize implements service.Authorizer.
func (a *Authorizer) Authorize(ctx context.Context, req service.AuthRequest) (service.AuthDecision, error) {
	// The second call for a Sync stream carries no headers: the stream was
	// already authenticated when it opened, and this call exists to decide
	// scope now that the table is known. Re-verifying is neither possible nor
	// wanted, so delegate with the claims already established.
	if req.Header == nil {
		if claims, ok := claimsFromContext(ctx); ok {
			return a.cfg.Authorize(ctx, req, claims)
		}
		return service.AuthDecision{}, connect.NewError(connect.CodeUnauthenticated,
			errors.New("no verified claims on this stream"))
	}

	raw, err := bearerToken(req.Header)
	if err != nil {
		return service.AuthDecision{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	claims, err := a.verify(ctx, raw)
	if err != nil {
		return service.AuthDecision{}, connect.NewError(connect.CodeUnauthenticated, err)
	}
	return a.cfg.Authorize(withClaims(ctx, claims), req, claims)
}

type claimsKey struct{}

func withClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

func claimsFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}

func bearerToken(h http.Header) (string, error) {
	v := h.Get("Authorization")
	if v == "" {
		return "", errors.New("missing Authorization header")
	}
	const prefix = "Bearer "
	if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return "", errors.New("authorization header is not a bearer token")
	}
	return v[len(prefix):], nil
}

// verify checks the token's signature, issuer, audience and validity window.
func (a *Authorizer) verify(ctx context.Context, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("malformed token")
	}

	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return Claims{}, fmt.Errorf("decode header: %w", err)
	}
	// Only RSA-SHA256 is accepted. Notably "none" is not, and neither is an
	// HMAC algorithm — an issuer's public key doubling as an HMAC secret is
	// the classic algorithm-confusion forgery.
	if hdr.Alg != "RS256" {
		return Claims{}, fmt.Errorf("unsupported alg %q", hdr.Alg)
	}

	key, err := a.keyFor(ctx, hdr.Kid)
	if err != nil {
		return Claims{}, err
	}
	if err := verifyRS256(parts[0]+"."+parts[1], parts[2], key); err != nil {
		return Claims{}, err
	}

	var body map[string]any
	if err := decodeSegment(parts[1], &body); err != nil {
		return Claims{}, fmt.Errorf("decode claims: %w", err)
	}
	claims := claimsFrom(body)

	if claims.Issuer != a.cfg.Issuer {
		return Claims{}, fmt.Errorf("issuer %q does not match %q", claims.Issuer, a.cfg.Issuer)
	}
	now := time.Now()
	if !claims.Expiry.IsZero() && now.After(claims.Expiry.Add(a.cfg.Leeway)) {
		return Claims{}, errors.New("token expired")
	}
	if a.cfg.Audience != "" && !contains(claims.Audience, a.cfg.Audience) {
		return Claims{}, fmt.Errorf("audience %v does not include %q", claims.Audience, a.cfg.Audience)
	}
	return claims, nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func claimsFrom(body map[string]any) Claims {
	c := Claims{Raw: body}
	if v, ok := body["sub"].(string); ok {
		c.Subject = v
	}
	if v, ok := body["iss"].(string); ok {
		c.Issuer = v
	}
	if v, ok := body["scope"].(string); ok {
		c.Scope = v
	}
	switch aud := body["aud"].(type) {
	case string:
		c.Audience = []string{aud}
	case []any:
		for _, x := range aud {
			if s, ok := x.(string); ok {
				c.Audience = append(c.Audience, s)
			}
		}
	}
	if v, ok := body["exp"].(float64); ok {
		c.Expiry = time.Unix(int64(v), 0)
	}
	if v, ok := body["iat"].(float64); ok {
		c.IssuedAt = time.Unix(int64(v), 0)
	}
	return c
}

func decodeSegment(seg string, into any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// keyFor returns the signing key for kid, refreshing the key set if the kid is
// unknown or the cache has aged out.
func (a *Authorizer) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	a.mu.RLock()
	key, ok := a.keys[kid]
	fresh := time.Since(a.fetchedAt) < a.cfg.RefreshInterval
	a.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}

	if err := a.refresh(ctx); err != nil {
		// An unknown kid with a stale cache and a failed refresh is fatal, but
		// a known kid still verifies — a brief inability to reach the issuer
		// should not reject every request.
		if ok {
			return key, nil
		}
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if key, ok := a.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("no key for kid %q", kid)
}

func (a *Authorizer) refresh(ctx context.Context) error {
	a.mu.RLock()
	uri := a.jwksURI
	a.mu.RUnlock()

	if uri == "" {
		discovered, err := a.discover(ctx)
		if err != nil {
			return err
		}
		uri = discovered
	}

	keys, err := a.fetchKeys(ctx, uri)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.keys = keys
	a.jwksURI = uri
	a.fetchedAt = time.Now()
	a.mu.Unlock()
	return nil
}

func (a *Authorizer) discover(ctx context.Context) (string, error) {
	url := strings.TrimSuffix(a.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := a.getJSON(ctx, url, &doc); err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	if doc.Issuer != a.cfg.Issuer {
		return "", fmt.Errorf("discovery document issuer %q does not match %q", doc.Issuer, a.cfg.Issuer)
	}
	if doc.JWKSURI == "" {
		return "", errors.New("discovery document has no jwks_uri")
	}
	return doc.JWKSURI, nil
}

func (a *Authorizer) fetchKeys(ctx context.Context, uri string) (map[string]*rsa.PublicKey, error) {
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := a.getJSON(ctx, uri, &jwks); err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}

	out := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
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
		out[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nb),
			E: exponent(eb),
		}
	}
	if len(out) == 0 {
		return nil, errors.New("jwks contained no usable RSA signing keys")
	}
	return out, nil
}

// exponent decodes a JWK's base64url exponent. RSA public exponents are small
// (65537 in practice), so anything that does not fit in an int is a malformed
// key rather than a large one.
func exponent(b []byte) int {
	if len(b) == 0 || len(b) > 8 {
		return 0
	}
	var padded [8]byte
	copy(padded[8-len(b):], b)
	v := binary.BigEndian.Uint64(padded[:])
	if v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

func (a *Authorizer) getJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	// Bound the response: an issuer is trusted to be honest, not to be small.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

var _ service.Authorizer = (*Authorizer)(nil)
