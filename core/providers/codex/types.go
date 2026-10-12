package codex

// Wire endpoints and client-identity defaults.
//
// The identity values mirror the Codex TUI build captured during reverse
// engineering (see debug/codex/README.md): inference and OAuth refresh both
// present as codex-tui/0.156.1 with originator codex-tui. The three move
// together — bump them as one unit. Revoke keeps the codex_cli_rs originator
// (that is what the captured revoke call sent).
const (
	// DefaultOAuthClientID is the public Codex CLI OAuth client. It is not a
	// secret (it ships in the CLI and relies on PKCE, RFC 8252); operators may
	// still override it per key via codex_key_config.client_id.
	DefaultOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	DefaultTokenURL   = "https://auth.openai.com/oauth/token"
	DefaultRevokeURL  = "https://auth.openai.com/oauth/revoke"
	DefaultBaseURL    = "https://chatgpt.com/backend-api/codex"
	DefaultAccountURL = "https://chatgpt.com/backend-api/wham/accounts/check"

	DefaultUserAgent  = "codex-tui/0.156.1 (Fedora 44.0.0; x86_64) Konsole/260801 (codex-tui; 0.156.1)"
	DefaultOriginator = "codex-tui"
	DefaultVersion    = "0.156.1"

	// DefaultClientVersion is the client_version query value for /models.
	DefaultClientVersion = "0.156.1"

	revokeOriginator = "codex_cli_rs"
)

// CodexTokenResponse is the wire shape of the OAuth token endpoint on both
// the authorization_code exchange and the refresh_token grant.
type CodexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// CodexAccount is one entry of the wham/accounts/check response.
type CodexAccount struct {
	ID       string `json:"id"`
	PlanType string `json:"plan_type"`
}

// CodexAccountsCheckResponse is the wire shape of
// GET /wham/accounts/check: every account on the credential plus the default
// to serve and the strict plan of the resolved account.
type CodexAccountsCheckResponse struct {
	Accounts         []CodexAccount `json:"accounts"`
	DefaultAccountID string         `json:"default_account_id"`
	AccountOrdering  []string       `json:"account_ordering"`
}

// CodexModel is one entry of the /models catalog. Only the fields Bifrost
// needs are decoded: the identity (slug), the gating fields, and the model
// metadata surfaced in ListModels.
type CodexModel struct {
	Slug             string   `json:"slug"`
	DisplayName      string   `json:"display_name"`
	Description      string   `json:"description"`
	AvailableInPlans []string `json:"available_in_plans"`
	ContextWindow    int      `json:"context_window"`
	MaxContextWindow int      `json:"max_context_window"`
}

// CodexModelsResponse is the wire shape of GET /models.
type CodexModelsResponse struct {
	Models []CodexModel `json:"models"`
}
