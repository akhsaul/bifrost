package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
)

const (
	// DefaultCodexAuthorizeURL is the OpenAI OAuth authorize endpoint for the
	// Codex PKCE flow.
	DefaultCodexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	// DefaultCodexRedirectURI is the loopback callback the Codex CLI itself
	// registers (see debug/codex/README.md). The browser lands here after
	// approval and the operator pastes the redirect URL (or bare code) back
	// into Bifrost, so this works for remote Bifrosts too — nothing listens
	// on this port, the code is carried in the URL.
	DefaultCodexRedirectURI = "http://localhost:1455/auth/callback"
	// CodexOAuthScope is the scope requested on the authorize URL, mirrored
	// from the captured Codex CLI flow (see debug/codex/README.md). The
	// refresh grant intentionally omits scope (RFC 6749 §6): Auth0 treats a
	// refresh with scope as a re-scope, which can invalidate sibling refresh
	// token families on the same client_id.
	CodexOAuthScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"
)

// oauthTokenURL resolves the token endpoint: production by default, or
// <base>/oauth/token when the handler test hook provides a base.
func oauthTokenURL(oauthBase string) string {
	if base := strings.TrimSpace(oauthBase); base != "" {
		return strings.TrimRight(base, "/") + "/oauth/token"
	}
	return DefaultTokenURL
}

// oauthAccountsURL resolves the accounts/check endpoint: production by
// default, or <base>/wham/accounts/check under the handler test hook.
func oauthAccountsURL(oauthBase string) string {
	if base := strings.TrimSpace(oauthBase); base != "" {
		return strings.TrimRight(base, "/") + "/wham/accounts/check"
	}
	return DefaultAccountURL
}

var (
	pendingCodexVerifiersMu sync.RWMutex
	pendingCodexVerifiers   = make(map[string]pendingCodexVerifierEntry)
	latestCodexVerifier     string
	latestCodexVerifierAt   time.Time
)

type pendingCodexVerifierEntry struct {
	verifier  string
	createdAt time.Time
}

func generateCodexState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func generateCodexVerifier() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func codexCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// StorePendingCodexVerifier records a pending PKCE verifier by state and as
// the latest verifier.
func StorePendingCodexVerifier(state, verifier string) {
	if verifier == "" {
		return
	}
	pendingCodexVerifiersMu.Lock()
	defer pendingCodexVerifiersMu.Unlock()

	now := time.Now()
	for k, v := range pendingCodexVerifiers {
		if now.Sub(v.createdAt) > 30*time.Minute {
			delete(pendingCodexVerifiers, k)
		}
	}

	if state != "" {
		pendingCodexVerifiers[state] = pendingCodexVerifierEntry{
			verifier:  verifier,
			createdAt: now,
		}
	}
	latestCodexVerifier = verifier
	latestCodexVerifierAt = now
}

// GetPendingCodexVerifier retrieves and removes a stored PKCE verifier
// associated with state.
func GetPendingCodexVerifier(state string) string {
	pendingCodexVerifiersMu.Lock()
	defer pendingCodexVerifiersMu.Unlock()

	if state != "" {
		if entry, ok := pendingCodexVerifiers[state]; ok {
			if time.Since(entry.createdAt) <= 30*time.Minute {
				delete(pendingCodexVerifiers, state)
				return entry.verifier
			}
			delete(pendingCodexVerifiers, state)
		}
	}
	return ""
}

// GetLatestCodexVerifier returns the most recently generated PKCE verifier
// within 30 minutes.
func GetLatestCodexVerifier() string {
	pendingCodexVerifiersMu.RLock()
	defer pendingCodexVerifiersMu.RUnlock()

	if latestCodexVerifier != "" && time.Since(latestCodexVerifierAt) <= 30*time.Minute {
		return latestCodexVerifier
	}
	return ""
}

// loginClientOnce guards the shared login-flow client below.
var loginClientOnce sync.Once
var sharedLoginClient *fasthttp.Client

// LoginFlowClient returns the shared HTTP client for the Codex browser-login
// helpers (auth-code exchange + best-effort account discovery). Login is
// setup-time traffic against fixed hosts, so one pooled client serves every
// call instead of handshaking per attempt.
func LoginFlowClient() *fasthttp.Client {
	loginClientOnce.Do(func() {
		timeout := 30 * time.Second
		sharedLoginClient = &fasthttp.Client{
			ReadTimeout:         timeout,
			WriteTimeout:        timeout,
			MaxConnsPerHost:     20,
			MaxIdleConnDuration: 30 * time.Second,
			MaxConnWaitTimeout:  timeout,
			ConnPoolStrategy:    fasthttp.FIFO,
		}
	})
	return sharedLoginClient
}

// CodexExchangeResult is the durable outcome of the PKCE login: the refresh
// token the UI stores on the key, the discovered workspace, and the account
// email for the key name. The access token is deliberately NOT returned — it
// is minted server-side on demand and never persisted (see tables/key.go).
type CodexExchangeResult struct {
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
	Email        string `json:"email,omitempty"`
}

// BuildCodexAuthURLPKCE generates the OpenAI authorize URL for the Codex PKCE
// flow plus the verifier the exchange must present. Empty redirectURI, state
// or clientID fall back to the CLI defaults. The verifier is stored server-
// side keyed by state (and as latest) and also returned so the UI can send it
// back explicitly; the exchange resolves explicit > by-state > latest, mirroring
// antigravity.
//
// Extra params mirror the captured CLI flow (debug/codex/README.md) plus
// prompt=login: without it OpenAI silently reuses the browser session and a
// second account's flow invalidates the first account's refresh family.
func BuildCodexAuthURLPKCE(redirectURI, state, clientID string) (authURL, verifier, stateOut, clientOut string) {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = DefaultCodexRedirectURI
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = DefaultOAuthClientID
	}
	if strings.TrimSpace(state) == "" {
		state = generateCodexState()
	}

	verifier = generateCodexVerifier()
	challenge := codexCodeChallenge(verifier)
	StorePendingCodexVerifier(state, verifier)

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", CodexOAuthScope)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("originator", "codex_cli_rs")
	params.Set("prompt", "login")
	return DefaultCodexAuthorizeURL + "?" + params.Encode(), verifier, state, clientID
}

// ExchangeAuthCodePKCE exchanges a Codex PKCE authorization code for tokens.
// oauthBase is the handler test hook: "" means production (auth.openai.com +
// chatgpt.com), otherwise both the token and accounts/check calls go to the
// mock base. A nil client falls back to the shared login client.
//
// The exchange posts form-encoded (like the CLI capture); the refresh grant
// elsewhere posts JSON — the two grants genuinely differ on the wire.
//
// On success the refresh token (the only durable credential) is returned with
// the best-effort discovered workspace and account email. A missing refresh
// token is a hard failure: without it there is nothing durable to store.
func ExchangeAuthCodePKCE(ctx context.Context, client *fasthttp.Client, code, redirectURI, clientID, verifier, oauthBase string) (*CodexExchangeResult, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("codex: authorization code is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = DefaultCodexRedirectURI
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = DefaultOAuthClientID
	}
	verifier = strings.TrimSpace(verifier)
	if verifier == "" {
		verifier = GetLatestCodexVerifier()
	}
	if verifier == "" {
		return nil, fmt.Errorf("codex: PKCE code_verifier is required for the code exchange. Please re-authenticate to obtain a fresh authorization URL")
	}
	if client == nil {
		client = LoginFlowClient()
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("redirect_uri", strings.TrimSpace(redirectURI))
	form.Set("code_verifier", verifier)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(oauthTokenURL(oauthBase))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBodyString(form.Encode())

	if err := doExchangeRequest(ctx, client, req, resp); err != nil {
		return nil, fmt.Errorf("codex: could not reach OpenAI to exchange the authorization code: %w", err)
	}
	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("codex: token exchange failed (%d): %s", resp.StatusCode(), codexErrorDetail(resp.Body()))
	}

	var tok CodexTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &tok); err != nil {
		return nil, fmt.Errorf("codex: could not parse the token exchange response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("codex: token exchange returned no access_token")
	}
	if strings.TrimSpace(tok.RefreshToken) == "" {
		return nil, fmt.Errorf("codex: token exchange returned no refresh token (offline_access may not have been granted)")
	}

	email, claimAccount := parseCodexIDToken(tok.IDToken)

	// Best-effort workspace: accounts/check is authoritative; the id_token
	// claim is the fallback. Either may be empty — inference auto-discovers
	// anyway, this just makes the credential explicit at save time.
	accountID := ""
	if discovered, _ := discoverAccountWithBase(ctx, client, tok.AccessToken, oauthBase); discovered != "" {
		accountID = discovered
	} else if claimAccount != "" {
		accountID = claimAccount
	}

	return &CodexExchangeResult{
		RefreshToken: tok.RefreshToken,
		AccountID:    accountID,
		Email:        email,
	}, nil
}

// parseCodexIDToken extracts the account email and workspace from a Codex
// id_token without verifying its signature — OpenAI's token endpoint already
// validated it by issuing it. Returns empty strings when the token is absent
// or has an unexpected shape; callers treat that as "unknown", never as an
// error.
func parseCodexIDToken(idToken string) (email, accountID string) {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := sonic.Unmarshal(payload, &claims); err != nil {
		return "", ""
	}
	return strings.TrimSpace(claims.Email), strings.TrimSpace(claims.Auth.AccountID)
}
