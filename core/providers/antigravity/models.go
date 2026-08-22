package antigravity

import (
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// AntigravityPublicModels contains the standard models available through Antigravity / Google Cloud Code.
var AntigravityPublicModels = []schemas.Model{
	{ID: "gemini-3.7-flash-high", Name: schemas.Ptr("Gemini 3.7 Flash (High)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.7-flash-medium", Name: schemas.Ptr("Gemini 3.7 Flash (Medium)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.7-flash-low", Name: schemas.Ptr("Gemini 3.7 Flash (Low)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.6-flash-high", Name: schemas.Ptr("Gemini 3.6 Flash (High)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.6-flash-medium", Name: schemas.Ptr("Gemini 3.6 Flash (Medium)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.6-flash-low", Name: schemas.Ptr("Gemini 3.6 Flash (Low)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.5-flash-high", Name: schemas.Ptr("Gemini 3.5 Flash (High)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.5-flash-low", Name: schemas.Ptr("Gemini 3.5 Flash (Medium)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.5-flash-extra-low", Name: schemas.Ptr("Gemini 3.5 Flash (Low)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-pro-agent", Name: schemas.Ptr("Gemini 3.1 Pro (High)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65535)},
	{ID: "gemini-3.1-pro-low", Name: schemas.Ptr("Gemini 3.1 Pro (Low)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65535)},
	{ID: "gemini-3-flash-agent", Name: schemas.Ptr("Gemini 3.5 Flash (High)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gemini-3.1-flash-lite", Name: schemas.Ptr("Gemini 3.1 Flash Lite"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65535)},
	{ID: "gemini-2.5-flash-thinking", Name: schemas.Ptr("Gemini 2.5 Flash Thinking"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65535)},
	{ID: "gemini-2.5-flash", Name: schemas.Ptr("Gemini 2.5 Flash"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65535)},
	{ID: "gemini-2.5-flash-lite", Name: schemas.Ptr("Gemini 2.5 Flash Lite"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65535)},
	{ID: "claude-opus-4-6-thinking", Name: schemas.Ptr("Claude Opus 4.6 (Thinking)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "claude-sonnet-4-6", Name: schemas.Ptr("Claude Sonnet 4.6 (Thinking)"), ContextLength: schemas.Ptr(1048576), MaxOutputTokens: schemas.Ptr(65536)},
	{ID: "gpt-oss-120b-medium", Name: schemas.Ptr("GPT-OSS 120B (Medium)"), ContextLength: schemas.Ptr(131072), MaxOutputTokens: schemas.Ptr(32768)},
}

// AntigravityModelAliases maps legacy or convenient model names to upstream model IDs.
var AntigravityModelAliases = map[string]string{
	"gemini-3.7-flash-high":             "gemini-3.7-flash-tiered(high)",
	"gemini-3.7-flash-medium":           "gemini-3.7-flash-tiered(medium)",
	"gemini-3.7-flash-low":              "gemini-3.7-flash-tiered(low)",
	"gemini-3.6-flash-high":             "gemini-3.6-flash-tiered(high)",
	"gemini-3.6-flash-medium":           "gemini-3.6-flash-tiered(medium)",
	"gemini-3.6-flash-low":              "gemini-3.6-flash-tiered(low)",
	"gemini-3-flash-agent":              "gemini-3.5-flash-high",
	"gemini-pro-agent":                  "gemini-3.1-pro-high",
	"gemini-claude-sonnet-4-5":          "claude-sonnet-4-6",
	"gemini-claude-sonnet-4-5-thinking": "claude-sonnet-4-6",
	"gemini-claude-opus-4-5-thinking":   "claude-opus-4-6-thinking",
	"gemini-3-pro-image-preview":        "gemini-3-pro-image",
}

// ResolveModel resolves model aliases and strips any routing prefix.
func ResolveModel(model string) string {
	if model == "" {
		return model
	}
	stripped := model
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		stripped = model[idx+1:]
	}
	if alias, ok := AntigravityModelAliases[stripped]; ok {
		return alias
	}
	return stripped
}

// HandleListModels returns the list of supported Antigravity models.
func HandleListModels(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	keys []schemas.Key,
	baseURL string,
	extraHeaders map[string]string,
	logger schemas.Logger,
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	models := make([]schemas.Model, len(AntigravityPublicModels))
	copy(models, AntigravityPublicModels)

	return &schemas.BifrostListModelsResponse{
		Data: models,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Antigravity,
			Latency:  0,
		},
	}, nil
}
