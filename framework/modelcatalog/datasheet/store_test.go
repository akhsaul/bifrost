package datasheet

import (
	"slices"

	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestPricingLookupsNormalizeRuntimeProvider(t *testing.T) {
	const model = "deepseek-ai/DeepSeek-V4-Flash-0731"
	inputCost := 0.00000014
	provider := schemas.ModelProvider("together")
	s := NewTestStore(nil)
	s.pricingData[makeKey(model, "together_ai", "chat")] = configstoreTables.TableModelPricing{
		Model:             model,
		Provider:          "together_ai",
		Mode:              "chat",
		InputCostPerToken: &inputCost,
	}

	row := s.Get(model, provider, schemas.ChatCompletionRequest)
	if row == nil || row.InputCostPerToken == nil || *row.InputCostPerToken != inputCost {
		t.Fatalf("Get() did not resolve the catalog provider: %#v", row)
	}

	pricing := s.GetPricingEntryForModel(model, provider)
	if pricing == nil || pricing.InputCostPerToken == nil || *pricing.InputCostPerToken != inputCost {
		t.Fatalf("GetPricingEntryForModel() did not resolve the catalog provider: %#v", pricing)
	}

	capability := s.GetCapabilityEntry(model, provider)
	if capability == nil || capability.InputCostPerToken == nil || *capability.InputCostPerToken != inputCost {
		t.Fatalf("GetCapabilityEntry() did not resolve the catalog provider: %#v", capability)
	}

	s.mu.Lock()
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()
	if got := s.DatasheetModelsForProvider(provider); !slices.Equal(got, []string{model}) {
		t.Fatalf("DatasheetModelsForProvider() = %v, want [%s]", got, model)
	}
	if got := s.DatasheetProviders(); !slices.Equal(got, []schemas.ModelProvider{provider}) {
		t.Fatalf("DatasheetProviders() = %v, want [%s]", got, provider)
	}
}

// A "-free" provider is the same provider as far as the catalog is concerned:
// the datasheet files its models under the base provider, so every runtime
// lookup has to resolve there. Without the fold, free-tier traffic found no
// pricing row at all and silently recorded cost 0.
func TestPricingLookupsFoldFreeTierProviderOntoBase(t *testing.T) {
	const model = "z-ai/glm-5.2:free"
	inputCost := 0.0000004
	s := NewTestStore(nil)
	s.pricingData[makeKey(model, "openrouter", "chat")] = configstoreTables.TableModelPricing{
		Model:             model,
		Provider:          "openrouter",
		Mode:              "chat",
		InputCostPerToken: &inputCost,
	}
	s.mu.Lock()
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()

	row := s.Get(model, schemas.OpenRouterFree, schemas.ChatCompletionRequest)
	if row == nil || row.InputCostPerToken == nil || *row.InputCostPerToken != inputCost {
		t.Fatalf("Get() did not fold openrouter-free onto openrouter: %#v", row)
	}

	pricing := s.GetPricingEntryForModel(model, schemas.OpenRouterFree)
	if pricing == nil || pricing.InputCostPerToken == nil || *pricing.InputCostPerToken != inputCost {
		t.Fatalf("GetPricingEntryForModel() did not fold openrouter-free onto openrouter: %#v", pricing)
	}

	capability := s.GetCapabilityEntry(model, schemas.OpenRouterFree)
	if capability == nil || capability.InputCostPerToken == nil || *capability.InputCostPerToken != inputCost {
		t.Fatalf("GetCapabilityEntry() did not fold openrouter-free onto openrouter: %#v", capability)
	}

	// The fold is one-directional: the real provider keeps resolving its own
	// rows, and is never handed a free-only catalog.
	if got := s.DatasheetModelsForProvider(schemas.OpenRouterFree); !slices.Equal(got, []string{model}) {
		t.Fatalf("DatasheetModelsForProvider(openrouter-free) = %v, want [%s]", got, model)
	}
	if got := s.DatasheetModelsForProvider(schemas.OpenRouter); !slices.Equal(got, []string{model}) {
		t.Fatalf("DatasheetModelsForProvider(openrouter) = %v, want [%s]", got, model)
	}
}

// opencode-zen-free folds onto opencode-zen, which is why that provider is
// named for its tier's parent rather than for "opencode".
func TestPricingLookupsFoldOpencodeZenFreeOntoZen(t *testing.T) {
	const model = "opencode-zen/muse-spark-1.3-contributor-free"
	inputCost := 0.0
	s := NewTestStore(nil)
	s.pricingData[makeKey(model, "opencode-zen", "chat")] = configstoreTables.TableModelPricing{
		Model:             model,
		Provider:          "opencode-zen",
		Mode:              "chat",
		InputCostPerToken: &inputCost,
	}

	row := s.Get(model, schemas.OpencodeZenFree, schemas.ChatCompletionRequest)
	if row == nil {
		t.Fatal("Get() did not fold opencode-zen-free onto opencode-zen")
	}
}

// A free-tier provider may only claim its base provider's free models. It exists
// precisely to keep free and paid traffic separable, so handing it the paid
// catalog would defeat the point of the split.
func TestDatasheetViewGivesFreeProvidersOnlyFreeModels(t *testing.T) {
	s := NewTestStore(nil)
	rows := []struct {
		provider string
		model    string
		deprecat bool
	}{
		{"openrouter", "z-ai/glm-5.2:free", false},
		{"openrouter", "z-ai/glm-5.2", false},
		{"openrouter", "deepseek/deepseek-r1:free", true},
		{"openrouter", "meta/llama-3.3-70b-instruct", true},
		{"opencode-zen", "muse-spark-1.3-contributor-free", false},
		{"opencode-zen", "muse-spark-1.3-contributor", false},
	}
	for _, r := range rows {
		// Catalog rows carry the provider in their own column and a model name
		// stripped of the owning-provider prefix (extractModelName).
		s.pricingData[makeKey(r.model, r.provider, "chat")] = configstoreTables.TableModelPricing{
			Model:        r.model,
			Provider:     r.provider,
			Mode:         "chat",
			IsDeprecated: r.deprecat,
		}
	}
	s.mu.Lock()
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()

	// Two free spellings are in play: opencode suffixes the model, openrouter
	// appends a ":free" variant tag. Both must land on the free provider.
	wantOpenRouterFree := []string{"deepseek/deepseek-r1:free", "z-ai/glm-5.2:free"}
	if got := s.DatasheetModelsForProvider(schemas.OpenRouterFree); !slices.Equal(got, wantOpenRouterFree) {
		t.Fatalf("DatasheetModelsForProvider(openrouter-free) = %v, want %v", got, wantOpenRouterFree)
	}
	wantZenFree := []string{"muse-spark-1.3-contributor-free"}
	if got := s.DatasheetModelsForProvider(schemas.OpencodeZenFree); !slices.Equal(got, wantZenFree) {
		t.Fatalf("DatasheetModelsForProvider(opencode-zen-free) = %v, want %v", got, wantZenFree)
	}

	// Deprecated rows follow the same free-only rule.
	if got := s.DeprecatedDatasheetModelsForProvider(schemas.OpenRouterFree); !slices.Equal(got, []string{"deepseek/deepseek-r1:free"}) {
		t.Fatalf("DeprecatedDatasheetModelsForProvider(openrouter-free) = %v, want only the deprecated free model", got)
	}

	// The base providers keep the full catalog, free and paid alike.
	if got := s.DatasheetModelsForProvider(schemas.OpenRouter); len(got) != 4 {
		t.Fatalf("DatasheetModelsForProvider(openrouter) = %v, want all 4 openrouter models", got)
	}
	if got := s.DatasheetModelsForProvider(schemas.OpencodeZen); len(got) != 2 {
		t.Fatalf("DatasheetModelsForProvider(opencode-zen) = %v, want both zen models", got)
	}
}

// A free provider whose base has no free models at all must not appear in the
// view: listing it with an empty catalog would advertise a provider that can
// serve nothing.
func TestDatasheetViewOmitsFreeProviderWithNoFreeModels(t *testing.T) {
	s := NewTestStore(nil)
	s.pricingData[makeKey("openrouter/z-ai/glm-5.2", "openrouter", "chat")] = configstoreTables.TableModelPricing{
		Model:    "openrouter/z-ai/glm-5.2",
		Provider: "openrouter",
		Mode:     "chat",
	}
	s.mu.Lock()
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()

	if got := s.DatasheetModelsForProvider(schemas.OpenRouterFree); got != nil {
		t.Fatalf("DatasheetModelsForProvider(openrouter-free) = %v, want nil", got)
	}
	for _, p := range s.DatasheetProviders() {
		if p == schemas.OpenRouterFree {
			t.Fatal("DatasheetProviders() must not list a free provider with no free models")
		}
	}
}

func TestDeprecatedDatasheetModelsForProviderUsesRebuiltIndex(t *testing.T) {
	s := NewTestStore(nil)
	s.mu.Lock()
	s.pricingData[makeKey("deprecated-b", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-b",
		Provider:     "openai",
		Mode:         "chat",
		IsDeprecated: true,
	}
	s.pricingData[makeKey("deprecated-a", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-a",
		Provider:     "openai",
		Mode:         "chat",
		IsDeprecated: true,
	}
	s.pricingData[makeKey("deprecated-a", "openai", "responses")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-a",
		Provider:     "openai",
		Mode:         "responses",
		IsDeprecated: true,
	}
	s.pricingData[makeKey("active", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:    "active",
		Provider: "openai",
		Mode:     "chat",
	}
	s.pricingData[makeKey("deprecated-vertex", "vertex_ai", "chat")] = configstoreTables.TableModelPricing{
		Model:        "deprecated-vertex",
		Provider:     "vertex_ai",
		Mode:         "chat",
		IsDeprecated: true,
	}
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()

	got := s.DeprecatedDatasheetModelsForProvider(schemas.OpenAI)
	want := []string{"deprecated-a", "deprecated-b"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected deprecated OpenAI models %v, got %v", want, got)
	}

	got[0] = "mutated"
	got = s.DeprecatedDatasheetModelsForProvider(schemas.OpenAI)
	if !slices.Equal(got, want) {
		t.Fatalf("expected defensive copy from index %v, got %v", want, got)
	}

	got = s.DeprecatedDatasheetModelsForProvider(schemas.Vertex)
	want = []string{"deprecated-vertex"}
	if !slices.Equal(got, want) {
		t.Fatalf("expected deprecated Vertex models %v, got %v", want, got)
	}
}
