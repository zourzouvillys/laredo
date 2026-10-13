package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zourzouvillys/laredo/service"
)

func allowAll(_ context.Context, _ service.AuthRequest, _ Claims) (service.AuthDecision, error) {
	return service.AuthDecision{}, nil
}

func TestNew_RejectsANonHTTPSIssuer(t *testing.T) {
	if _, err := New(Config{Issuer: "http://issuer.example", Authorize: allowAll}); err == nil {
		t.Error("an http:// issuer was accepted without AllowInsecureIssuer")
	}
	if _, err := New(Config{Issuer: "issuer.example", Authorize: allowAll}); err == nil {
		t.Error("a scheme-less issuer was accepted")
	}
	if _, err := New(Config{Issuer: "http://issuer.example", AllowInsecureIssuer: true, Authorize: allowAll}); err != nil {
		t.Errorf("AllowInsecureIssuer did not permit http: %v", err)
	}
}

func TestNew_RequiresAnAuthorizeCallback(t *testing.T) {
	// A verified token establishes who is calling, not what they may reach.
	// Defaulting that to "allow" is how a table becomes readable by every
	// workload holding a token for the issuer.
	if _, err := New(Config{Issuer: "https://issuer.example"}); err == nil {
		t.Error("an Authorizer was built with no Authorize callback")
	}
}

// TestDiscovery_RejectsAnOffOriginJWKSURI is the SSRF regression test. The
// jwks_uri arrives inside a document fetched over the network and is then
// fetched in turn, so without a constraint it points this process at any
// address reachable from it.
func TestDiscovery_RejectsAnOffOriginJWKSURI(t *testing.T) {
	var jwksHit bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jwksHit = true
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer elsewhere.Close()

	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": issuerURLOf(r),
			// Points at a completely different host.
			"jwks_uri": elsewhere.URL + "/keys",
		})
	}))
	defer issuer.Close()

	a, err := New(Config{
		Issuer:              issuer.URL,
		AllowInsecureIssuer: true,
		HTTPClient:          issuer.Client(),
		Authorize:           allowAll,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = a.refresh(context.Background())
	if err == nil {
		t.Fatal("an off-origin jwks_uri was accepted")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error %q does not explain the origin constraint", err)
	}
	if jwksHit {
		t.Error("the off-origin jwks_uri was actually fetched")
	}
}

// TestDiscovery_AcceptsAnOnOriginJWKSURI confirms the constraint is not simply
// rejecting everything.
func TestDiscovery_AcceptsAnOnOriginJWKSURI(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":   srv.URL,
				"jwks_uri": srv.URL + "/keys",
			})
		case r.URL.Path == "/keys":
			// One well-formed RSA key so the fetch has something to accept.
			_, _ = fmt.Fprint(w, `{"keys":[{"kty":"RSA","kid":"k1","use":"sig",`+
				`"n":"sXchDaQebHnPiGvyDOAT4saGEUetSyo9MKLOoWFsueri23bOdgWp4Dy1Wl`+
				`UzewbgBHod5pcM9H95GQRV3JDXboIRROSBigeC5yjU1hGzHHyXss8UDpre`+
				`cbAYxknTcQkhslANGRUZmdTOQ5qTRsLAt6BTYuyvVRdhS8exSZEy_c4gs_`+
				`7svlJJQ4H9_NxsiIoLwAEk7-Q3UXERGYw_75IDrGA84-lA_-Ct4eTlXHBI`+
				`Y2EaV7t7LjJaynVJCpkv4LKjTTAumiGUIuQhrNhZLuF_RJLqHpM2kgWFLU`+
				`7-VTdL1VbC2tejvcI2BlMkEpk1BzBZI0KQB0GaDWFLN-aEAw3vRw","e":"AQAB"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a, err := New(Config{
		Issuer:              srv.URL,
		AllowInsecureIssuer: true,
		HTTPClient:          srv.Client(),
		Authorize:           allowAll,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.refresh(context.Background()); err != nil {
		t.Fatalf("refresh with an on-origin jwks_uri failed: %v", err)
	}
	if _, err := a.keyFor(context.Background(), "k1"); err != nil {
		t.Errorf("key k1 not loaded: %v", err)
	}
}

func TestVerify_RejectsUnsupportedAlgorithms(t *testing.T) {
	a, err := New(Config{Issuer: "https://issuer.example", Authorize: allowAll})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// alg "none" with no signature, the classic forgery.
	tok := b64(`{"alg":"none","kid":"k1"}`) + "." + b64(`{"iss":"https://issuer.example"}`) + "."
	if _, err := a.verify(context.Background(), tok); err == nil {
		t.Error(`a token with alg "none" was accepted`)
	}
	// An HMAC algorithm, where the issuer's public key would double as the secret.
	tok = b64(`{"alg":"HS256","kid":"k1"}`) + "." + b64(`{"iss":"https://issuer.example"}`) + ".sig"
	if _, err := a.verify(context.Background(), tok); err == nil {
		t.Error("a token with an HMAC alg was accepted")
	}
}

func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func issuerURLOf(r *http.Request) string {
	return "http://" + r.Host
}
