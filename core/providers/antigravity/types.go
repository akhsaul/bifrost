// Package antigravity implements the Antigravity (Google Cloud Code / Gemini Code Assist) provider for Bifrost.
package antigravity

import (
	"github.com/maximhq/bifrost/core/providers/gemini"
)

// AntigravityCredentials represents the credentials extracted from an Antigravity key.
type AntigravityCredentials struct {
	ProjectID     string `json:"project_id,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	AccessToken   string `json:"access_token,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
	ClientSecret  string `json:"client_secret,omitempty"`
	ClientProfile string `json:"client_profile,omitempty"`
}

// AntigravityInnerRequest represents the inner request payload for generateContent and streamGenerateContent.
type AntigravityInnerRequest struct {
	SessionID         string                   `json:"sessionId,omitempty"`
	Contents          []gemini.Content         `json:"contents,omitempty"`
	SystemInstruction *gemini.Content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *gemini.GenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []gemini.SafetySetting   `json:"safetySettings,omitempty"`
	Tools             []gemini.Tool            `json:"tools,omitempty"`
	ToolConfig        *gemini.ToolConfig       `json:"toolConfig,omitempty"`
}

// AntigravityRequestEnvelope is the top-level envelope required by Google Cloud Code API endpoints.
type AntigravityRequestEnvelope struct {
	Project            string                   `json:"project"`
	Model              string                   `json:"model,omitempty"`
	UserAgent          string                   `json:"userAgent"` // "antigravity"
	RequestID          string                   `json:"requestId"`
	RequestType        string                   `json:"requestType"` // "agent" or "image_gen"
	Request            *AntigravityInnerRequest `json:"request"`
	EnabledCreditTypes []string                 `json:"enabledCreditTypes,omitempty"`
}

// AntigravityTokenResponse represents the response from the Google OAuth2 token refresh endpoint.
type AntigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

// AntigravityLoadCodeAssistResponse represents the response from /v1internal:loadCodeAssist.
type AntigravityLoadCodeAssistResponse struct {
	CloudAICompanionProject interface{} `json:"cloudaicompanionProject"`
}

// AntigravitySSEChunk represents an SSE data chunk returned from /v1internal:streamGenerateContent.
type AntigravitySSEChunk struct {
	Response      *gemini.GenerateContentResponse              `json:"response,omitempty"`
	Candidates    []*gemini.Candidate                          `json:"candidates,omitempty"`
	UsageMetadata *gemini.GenerateContentResponseUsageMetadata `json:"usageMetadata,omitempty"`
	Markdown      string                                       `json:"markdown,omitempty"`
	Error         *AntigravityErrorPayload                     `json:"error,omitempty"`
}

// AntigravityErrorPayload represents an error payload in Antigravity responses.
type AntigravityErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
