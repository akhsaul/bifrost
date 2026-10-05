package zed

import (
	"github.com/bytedance/sonic"
)

// Zed API endpoints and identity defaults. The user-agent doubles as the
// protocol version: x-zed-version carries the same build string, and the two
// must move together when bumped. Observed from a live Zed 1.10.2 capture.
const (
	DefaultBaseURL = "https://cloud.zed.dev"

	DefaultUserAgent = "Zed/1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734 (linux; x86_64)"
	DefaultVersion   = "1.10.2+stable.322.adc60ccf12e199b8828bad3abb2591e147034734"

	// DefaultSystemID is the well-known Zed editor system id used when a key
	// leaves zed_key_config.system_id empty.
	DefaultSystemID = "3dfad06a-5c48-4c7b-a480-7ea86b311eb9"
)

// Zed completions-envelope provider discriminators. These are Zed's own
// vocabulary and intentionally differ from Bifrost's ModelProvider values:
// "open_ai" (underscore) vs "openai", "google" vs "gemini".
const (
	ZedInnerAnthropic = "anthropic"
	ZedInnerGoogle    = "google"
	ZedInnerOpenAI    = "open_ai"
)

// ZedCompletionsEnvelope is the wire shape of POST /completions: routing
// fields plus the native upstream request for the inner family.
type ZedCompletionsEnvelope struct {
	ThreadID        string      `json:"thread_id"`
	PromptID        string      `json:"prompt_id"`
	Provider        string      `json:"provider"`
	Model           string      `json:"model"`
	ProviderRequest interface{} `json:"provider_request"`
}

// ZedStreamLine is one NDJSON line of a /completions response. Exactly one
// of Event / Status is present: {"event": ...} carries an inner-family
// stream event, {"status":"stream_ended"} terminates the stream.
type ZedStreamLine struct {
	Event  *ZedStreamEvent `json:"event,omitempty"`
	Status *string         `json:"status,omitempty"`
}

// ZedStreamEvent is the opaque inner-family event payload. It is decoded
// into the concrete family type (Anthropic/Gemini/OpenAI) downstream.
//
// The full event object is preserved as Raw so family converters see the
// complete shape (message_start carries message+usage, deltas carry
// index+delta, etc.). Only Type is promoted for dispatch.
type ZedStreamEvent struct {
	Type string                 `json:"type"`
	Raw  map[string]interface{} `json:"-"`
}

// UnmarshalJSON preserves the full event object in Raw while promoting
// Type for dispatch. sonic funnels both encoding/json and its own decode
// through this method on the ZedStreamEvent pointer.
func (e *ZedStreamEvent) UnmarshalJSON(data []byte) error {
	type eventShadow struct {
		Type string `json:"type"`
	}
	var shadow eventShadow
	if err := sonic.Unmarshal(data, &shadow); err != nil {
		return err
	}
	var raw map[string]interface{}
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Type = shadow.Type
	e.Raw = raw
	return nil
}

// ZedStreamEnded is the terminal status value of a /completions stream.
const ZedStreamEnded = "stream_ended"

// ZedUsersMeResponse is the subset of GET /client/users/me Bifrost needs:
// the default billing organization plus the human-readable username for the
// OAuth key name. Observed shape (debug/zed-dev/resp-users-me.json):
// {"user":{"id":678672,"username":"akhsaul",...},"default_organization_id":"org_..."}.
type ZedUsersMeResponse struct {
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	DefaultOrganizationID string `json:"default_organization_id"`
}

// ZedLLMTokensRequest is the wire shape of POST /client/llm_tokens.
type ZedLLMTokensRequest struct {
	OrganizationID string `json:"organization_id"`
}

// ZedLLMTokensResponse is the wire shape of POST /client/llm_tokens.
type ZedLLMTokensResponse struct {
	Token string `json:"token"`
}

// ZedModelEntry is one entry of GET /models: the model id plus the inner
// family that serves it. Provider is Zed's vocabulary (anthropic | google |
// open_ai); DisplayName, ContextLength and capabilities map onto schemas.Model.
type ZedModelEntry struct {
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	MaxTokens     *int   `json:"max_token_count,omitempty"`
	MaxOutputToks *int   `json:"max_output_tokens,omitempty"`
	SupportsTools *bool  `json:"supports_tools,omitempty"`
}

// ZedModelsResponse is the wire shape of GET /models.
type ZedModelsResponse struct {
	Models []ZedModelEntry `json:"models"`
}
