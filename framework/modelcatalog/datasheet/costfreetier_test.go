package datasheet

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A "-free" provider must price against its base provider's rows. The datasheet
// files no rows of its own for the free tier — opencode's free models live under
// "opencode-zen", openrouter's under "openrouter" — so before the fold every
// free-tier response found no pricing row and recorded cost 0, under-reporting
// spend whenever a free-tier route actually served a paid model.
func TestCalculateCost_FreeProviderUsesBaseProviderPricing(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("z-ai/glm-5.2:free", "openrouter", "chat"): {
			Model: "z-ai/glm-5.2:free", Provider: "openrouter", Mode: "chat",
			InputCostPerToken: new(0.0000002),
		},
		makeKey("z-ai/glm-5.2", "openrouter", "chat"): {
			Model: "z-ai/glm-5.2", Provider: "openrouter", Mode: "chat",
			InputCostPerToken:  new(0.0000004),
			OutputCostPerToken: new(0.0000012),
		},
		makeKey("opencode-zen/muse-spark-1.3-contributor-free", "opencode-zen", "chat"): {
			Model: "opencode-zen/muse-spark-1.3-contributor-free", Provider: "opencode-zen", Mode: "chat",
			InputCostPerToken: new(0.0),
		},
	})

	t.Run("paid model on a free provider bills at the base rate", func(t *testing.T) {
		resp := makeChatResponse(schemas.OpenRouterFree, "z-ai/glm-5.2", &schemas.BifrostLLMUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		})
		// 1000 * 0.0000004 + 500 * 0.0000012 = 0.0004 + 0.0006 = 0.001
		assert.InDelta(t, 0.001, s.CalculateCost(resp, nil), 1e-12)
	})

	t.Run("genuinely free model costs nothing", func(t *testing.T) {
		resp := makeChatResponse(schemas.OpenRouterFree, "z-ai/glm-5.2:free", &schemas.BifrostLLMUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		})
		assert.InDelta(t, 0.0002, s.CalculateCost(resp, nil), 1e-12)
	})

	t.Run("opencode-zen-free resolves the zen catalog", func(t *testing.T) {
		resp := makeChatResponse(schemas.OpencodeZenFree, "opencode-zen/muse-spark-1.3-contributor-free", &schemas.BifrostLLMUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		})
		assert.InDelta(t, 0.0, s.CalculateCost(resp, nil), 1e-12)
	})

	// The base provider must be unaffected: the fold only ever resolves a
	// free-tier provider toward its base, never the reverse.
	t.Run("base provider still prices its own models", func(t *testing.T) {
		resp := makeChatResponse(schemas.OpenRouter, "z-ai/glm-5.2", &schemas.BifrostLLMUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		})
		assert.InDelta(t, 0.001, s.CalculateCost(resp, nil), 1e-12)
	})
}

// The catalog fold must not leak into override scoping. Overrides stay keyed by
// the raw provider id, so an override scoped to openrouter does not silently
// start applying to openrouter-free traffic — the two are configured and billed
// independently, and only the built-in catalog rows are shared.
func TestCalculateCost_FreeProviderOverrideScopeStaysRaw(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("z-ai/glm-5.2", "openrouter", "chat"): {
			Model: "z-ai/glm-5.2", Provider: "openrouter", Mode: "chat",
			InputCostPerToken:  new(0.0000004),
			OutputCostPerToken: new(0.0000012),
		},
	})
	openrouterID := string(schemas.OpenRouter)
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openrouter-scoped-override",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &openrouterID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "z-ai/glm-5.2",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":0.00001}`,
		},
	}))
	usage := &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}

	// openrouter pays the override on input (1000 * 0.00001 = 0.01) and keeps
	// the catalog's output rate (500 * 0.0000012 = 0.0006).
	assert.InDelta(t, 0.0106, s.CalculateCost(makeChatResponse(schemas.OpenRouter, "z-ai/glm-5.2", usage), nil), 1e-12)
	// openrouter-free resolves the same base row but must NOT inherit the
	// provider-scoped override: 1000 * 0.0000004 + 500 * 0.0000012 = 0.001
	assert.InDelta(t, 0.001, s.CalculateCost(makeChatResponse(schemas.OpenRouterFree, "z-ai/glm-5.2", usage), nil), 1e-12)
}
