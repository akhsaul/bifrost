package handlers

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/antigravity"
	"github.com/valyala/fasthttp"
)

func TestGetAntigravityAuthURL_DefaultPKCE(t *testing.T) {
	h := &ProviderHandler{}
	ctx := &fasthttp.RequestCtx{}

	h.getAntigravityAuthURL(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", ctx.Response.StatusCode())
	}

	var resp map[string]string
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	authURL := resp["auth_url"]
	if authURL == "" {
		t.Fatal("expected non-empty auth_url")
	}

	codeVerifier := resp["code_verifier"]
	if codeVerifier == "" {
		t.Fatal("expected non-empty code_verifier for PKCE flow")
	}

	state := resp["state"]
	if state == "" {
		t.Fatal("expected non-empty state for PKCE flow")
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse auth_url: %v", err)
	}
	q := u.Query()
	if q.Get("redirect_uri") != antigravity.NewAntigravityRedirectURI {
		t.Errorf("expected redirect_uri=%s, got %s", antigravity.NewAntigravityRedirectURI, q.Get("redirect_uri"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("expected S256 code challenge method, got %s", q.Get("code_challenge_method"))
	}
	if !strings.Contains(q.Get("scope"), "https://www.googleapis.com/auth/aicode") {
		t.Errorf("expected aicode scope, got %s", q.Get("scope"))
	}
}

func TestGetAntigravityAuthURL_LegacyMode(t *testing.T) {
	h := &ProviderHandler{}
	ctx := &fasthttp.RequestCtx{}
	ctx.URI().SetQueryString("mode=legacy&state=custom-state")

	h.getAntigravityAuthURL(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", ctx.Response.StatusCode())
	}

	var resp map[string]string
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	authURL := resp["auth_url"]
	if authURL == "" {
		t.Fatal("expected non-empty auth_url")
	}

	if resp["code_verifier"] != "" {
		t.Errorf("legacy mode should not return code_verifier, got %s", resp["code_verifier"])
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse auth_url: %v", err)
	}
	q := u.Query()
	if q.Get("redirect_uri") != antigravity.DefaultAntigravityRedirectURI {
		t.Errorf("expected redirect_uri=%s, got %s", antigravity.DefaultAntigravityRedirectURI, q.Get("redirect_uri"))
	}
	if q.Get("code_challenge") != "" {
		t.Errorf("legacy mode should not contain code_challenge, got %s", q.Get("code_challenge"))
	}
	if q.Get("state") != "custom-state" {
		t.Errorf("expected state=custom-state, got %s", q.Get("state"))
	}
}
