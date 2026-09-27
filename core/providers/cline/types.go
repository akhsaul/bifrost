package cline

import (
	"encoding/json"
)

// Cline API endpoints and identity defaults. The version strings move together
// with the User-Agent: Cline validates that a caller looks like a supported
// client, so bump them as one unit, mirroring the real extension release.
const (
	DefaultBaseURL = "https://api.cline.bot/api"

	DefaultUserAgent   = "Cline/4.1.21 ai-sdk/openai-compatible/3.0.37 ai-sdk/provider-utils/5.0.30 runtime/node.js/v24.18.1"
	DefaultHTTPReferer = "https://cline.bot"
	DefaultClientType  = "VSCode Extension"
	DefaultClientVers  = "4.1.21"
	DefaultCoreVers    = "0.0.86"
	DefaultIsMultiroot = "false"
	DefaultPlatform    = "Visual Studio Code"
	DefaultPlatVers    = "1.134.0"
	DefaultTitle       = "Cline"

	// DefaultWorkOSClientID is the public Cline VSCode client on WorkOS. It is
	// not a secret (it ships in the extension); operators may still override it
	// per key via cline_key_config.client_id.
	DefaultWorkOSClientID = "client_01K3A541FN8TA3EPPHTD2325AR"

	// OAuthTokenPrefix marks an OAuth access token in the Authorization header.
	// Cline requires "Bearer workos:<access_token>"; without the prefix the
	// gateway cannot route the credential and rejects the call.
	OAuthTokenPrefix = "workos:"
)

// ClineRecommendedModel is one entry of the recommended-models response.
type ClineRecommendedModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// ClineRecommendedModelsResponse is the wire shape of
// GET /v1/ai/cline/recommended-models.
type ClineRecommendedModelsResponse struct {
	Recommended []ClineRecommendedModel `json:"recommended"`
	Free        []ClineRecommendedModel `json:"free"`
	ClinePass   []ClineRecommendedModel `json:"clinePass"`
	ClineCloud  []ClineRecommendedModel `json:"clineCloud"`
}

// All returns every recommended entry across all buckets.
func (r *ClineRecommendedModelsResponse) All() []ClineRecommendedModel {
	if r == nil {
		return nil
	}
	out := make([]ClineRecommendedModel, 0, len(r.Recommended)+len(r.Free)+len(r.ClinePass)+len(r.ClineCloud))
	out = append(out, r.Recommended...)
	out = append(out, r.Free...)
	out = append(out, r.ClinePass...)
	out = append(out, r.ClineCloud...)
	return out
}

// ClineChatEnvelope is the non-streaming chat completion envelope:
// {"data": {<openai chat completion>}, "success": true}.
type ClineChatEnvelope struct {
	Data    json.RawMessage `json:"data"`
	Success bool            `json:"success"`
}

// ClineRefreshRequest is the wire shape of POST /v1/auth/refresh.
type ClineRefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
	GrantType    string `json:"grantType"`
}

// ClineRefreshData is the inner data of a successful refresh response.
type ClineRefreshData struct {
	AccessToken  string          `json:"accessToken"`
	TokenType    string          `json:"tokenType"`
	ExpiresAt    string          `json:"expiresAt"`
	RefreshToken string          `json:"refreshToken"`
	UserInfo     json.RawMessage `json:"userInfo,omitempty"`
}

// ClineRefreshResponse is the wire shape of a successful refresh response:
// {"data": {...}, "success": true}.
type ClineRefreshResponse struct {
	Data    ClineRefreshData `json:"data"`
	Success bool             `json:"success"`
}
