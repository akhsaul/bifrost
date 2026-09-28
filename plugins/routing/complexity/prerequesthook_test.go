package complexity_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// fakeGuardrailTextEvaluator stands in for the guardrails plugin. It blocks any
// text containing blockOn, rewrites entries in replace, and records every
// evaluation so tests can assert what the router asked it to guard.
type fakeGuardrailTextEvaluator struct {
	mu      sync.Mutex
	blockOn string
	replace map[string]string
	calls   []string
}

func (f *fakeGuardrailTextEvaluator) EvaluateInputText(_ *schemas.BifrostContext, _ *schemas.BifrostRequest, text string) (string, *schemas.BifrostError) {
	f.mu.Lock()
	f.calls = append(f.calls, text)
	f.mu.Unlock()

	if f.blockOn != "" && strings.Contains(text, f.blockOn) {
		return text, &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     schemas.Ptr(400),
			Error:          &schemas.ErrorField{Message: "blocked by fake guardrail"},
			AllowFallbacks: schemas.Ptr(false),
		}
	}
	out := text
	for from, to := range f.replace {
		out = strings.ReplaceAll(out, from, to)
	}
	return out, nil
}

func (f *fakeGuardrailTextEvaluator) evaluatedTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeGuardrailTextEvaluator) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func chatString(text string) *schemas.ChatMessageContent {
	return &schemas.ChatMessageContent{ContentStr: &text}
}

// newComplexityRuleFixture builds a routing plugin whose store carries one
// rule that fires only when a complexity tier was published.
func newComplexityRuleFixture(t *testing.T) *routing.RoutingPlugin {
	return newComplexityRuleFixtureWithConfig(t, nil)
}

func newComplexityRuleFixtureWithConfig(t *testing.T, config *routing.Config) *routing.RoutingPlugin {
	t.Helper()
	logger := rules.NewMockLogger()
	provider := "openai"
	model := "gpt-4o-mini"

	ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)
	require.NoError(t, ruleStore.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "Complexity Available",
		CelExpression: `complexity_tier != ""`,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &model, Weight: 1.0},
		},
		Enabled:  schemas.Ptr(true),
		Scope:    "global",
		Priority: 0,
	}))

	plugin, err := routing.InitFromStore(context.Background(), config, logger, nil, ruleStore, routing.NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

func newSessionComplexityRuleFixture(t *testing.T) *routing.RoutingPlugin {
	t.Helper()
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return newComplexityRuleFixtureWithConfig(t, &routing.Config{KVStore: store})
}

func chatRequest(text string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: chatString(text),
				},
			},
		},
	}
}

// TestPreRequestHook_ComplexitySkippedWithoutEmbeddingModel pins the contract
// that semantic classification is the only mechanism. A deployment with phrase
// lists but no embedding model publishes no tier at all rather than quietly
// scoring those phrases as literal keywords.
func TestPreRequestHook_ComplexitySkippedWithoutEmbeddingModel(t *testing.T) {
	plugin := newComplexityRuleFixture(t)

	req := chatRequest("What is a vector database?")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	// No embedding model is configured, so there is no mechanism left to
	// classify with: the rule's complexity_tier reference cannot match, the
	// routing-rule engine never fires, and the request keeps the model it
	// arrived with.
	engines, _ := bfCtx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	require.NotContains(t, engines, schemas.RoutingEngineRoutingRule)

	providerOut, modelOut, _ := req.GetRequestFields()
	require.Equal(t, schemas.OpenAI, providerOut)
	require.Equal(t, "gpt-4o", modelOut)

	_, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier).(string)
	require.False(t, ok, "no complexity tier should be published without an embedding model")
	mechanism, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	require.True(t, ok, "routing mechanism should be recorded in context")
	require.Equal(t, complexity.MechanismSkipped, mechanism)
	_, ok = bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore).(float64)
	require.False(t, ok, "no complexity score should be published without an embedding model")
}

// testVectorForText gives every tier's exemplar a distinct axis so nearest-
// neighbour classification is exact and deterministic.
func testVectorForText(text string) []float64 {
	switch {
	case strings.Contains(text, "casual greeting"):
		return []float64{1, 0, 0}
	case strings.Contains(text, "implementation detail"):
		return []float64{0, 1, 0}
	case strings.Contains(text, "deep architectural tradeoff"):
		return []float64{0, 0, 1}
	case strings.Contains(text, "medium request"):
		return []float64{0, 1, 0}
	case strings.Contains(text, "complex request"):
		return []float64{0, 0, 1}
	default: // request text: nearest to the SIMPLE exemplar
		return []float64{0.9, 0.1, 0}
	}
}

func sessionAnalyzerConfig() *complexity.AnalyzerConfig {
	return &complexity.AnalyzerConfig{
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords:  []string{"a casual greeting"},
			MediumKeywords:  []string{"an implementation detail question"},
			ComplexKeywords: []string{"a deep architectural tradeoff analysis"},
		},
		Semantic: &configstore.ComplexitySemanticConfig{
			Provider:       "openai",
			EmbeddingModel: "test-embedding-model",
		},
		Session: &configstore.ComplexitySessionConfig{Enabled: true},
	}
}

func waitForSemanticClassifier(t *testing.T, plugin *routing.RoutingPlugin) {
	t.Helper()
	require.Eventually(t, func() bool {
		return plugin.ComplexitySemanticStatus().State == complexity.SemanticStatusReady
	}, 5*time.Second, 10*time.Millisecond, "semantic warmup should become ready")
}

func complexitySessionContext(sessionID string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if sessionID != "" {
		ctx.SetValue(schemas.BifrostContextKeySessionID, sessionID)
	}
	return ctx
}

func testEmbeddingExecutor(_ *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	texts := req.Input.Texts
	if req.Input.Text != nil {
		texts = []string{*req.Input.Text}
	}
	data := make([]schemas.EmbeddingData, len(texts))
	for i, text := range texts {
		data[i] = schemas.EmbeddingData{Index: i, Embedding: schemas.EmbeddingStruct{EmbeddingArray: testVectorForText(text)}}
	}
	return &schemas.BifrostEmbeddingResponse{
		Data:  data,
		Usage: &schemas.BifrostLLMUsage{TotalTokens: len(texts) * 3},
	}, nil
}

// TestPreRequestHook_SemanticComplexityPublishesTierAndRoutes runs the full
// path: exemplar warmup through the embedded store, nearest-neighbour
// classification of the request, tier/mechanism/score publication, and the
// complexity_tier rule firing on the published tier.
func TestPreRequestHook_SemanticComplexityPublishesTierAndRoutes(t *testing.T) {
	plugin := newComplexityRuleFixture(t)
	plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
	plugin.ReloadComplexityAnalyzerConfig(&complexity.AnalyzerConfig{
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords:  []string{"a casual greeting"},
			MediumKeywords:  []string{"an implementation detail question"},
			ComplexKeywords: []string{"a deep architectural tradeoff analysis"},
		},
		Semantic: &configstore.ComplexitySemanticConfig{
			Provider:       "openai",
			EmbeddingModel: "test-embedding-model",
		},
	})

	require.Eventually(t, func() bool {
		return plugin.ComplexitySemanticStatus().State == complexity.SemanticStatusReady
	}, 5*time.Second, 10*time.Millisecond, "semantic warmup should become ready")

	req := chatRequest("hey, quick question")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	tier, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier).(string)
	require.True(t, ok, "complexity tier should be published")
	require.Equal(t, complexity.TierSimple, tier)
	mechanism, _ := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	require.Equal(t, complexity.MechanismSemantic, mechanism)
	score, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore).(float64)
	require.True(t, ok, "complexity score should be published")
	require.Greater(t, score, 0.0)

	// The rule fired on the published tier and rewrote the model.
	engines, _ := bfCtx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	require.Contains(t, engines, schemas.RoutingEngineRoutingRule)
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o-mini", modelOut)
}

func TestPreRequestHook_SessionComplexityOnlyEscalates(t *testing.T) {
	plugin := newSessionComplexityRuleFixture(t)
	plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(sessionAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	tests := []struct {
		requestText   string
		wantTier      string
		wantMechanism string
		wantLogParts  []string
	}{
		{
			requestText:   "a simple request",
			wantTier:      complexity.TierSimple,
			wantMechanism: complexity.MechanismSemantic,
			wantLogParts:  []string{"Session complexity initialized:", "effective=SIMPLE", "proposed=SIMPLE", "source=semantic", "proposed_similarity=", `proposed_matched="a casual greeting"`},
		},
		{
			requestText:   "a medium request",
			wantTier:      complexity.TierMedium,
			wantMechanism: complexity.MechanismSemantic,
			wantLogParts:  []string{"Session complexity escalated:", "effective=MEDIUM", "previous=SIMPLE", "proposed=MEDIUM", "source=semantic", "proposed_similarity=", `proposed_matched="an implementation detail question"`},
		},
		{
			requestText:   "another medium request",
			wantTier:      complexity.TierMedium,
			wantMechanism: complexity.MechanismSemantic,
			wantLogParts:  []string{"Session complexity confirmed:", "effective=MEDIUM", "proposed=MEDIUM", "source=semantic", "proposed_similarity=", `proposed_matched="an implementation detail question"`},
		},
		{
			requestText:   "another simple request",
			wantTier:      complexity.TierMedium,
			wantMechanism: complexity.MechanismSession,
			wantLogParts:  []string{"Session complexity held:", "effective=MEDIUM", "proposed=SIMPLE", "source=semantic", "proposed_similarity=", `proposed_matched="a casual greeting"`},
		},
		{
			requestText:   "a complex request",
			wantTier:      complexity.TierComplex,
			wantMechanism: complexity.MechanismSemantic,
			wantLogParts:  []string{"Session complexity escalated:", "effective=COMPLEX", "previous=MEDIUM", "proposed=COMPLEX", "source=semantic", "proposed_similarity=", `proposed_matched="a deep architectural tradeoff analysis"`},
		},
		{
			requestText:   "one more simple request",
			wantTier:      complexity.TierComplex,
			wantMechanism: complexity.MechanismSession,
			wantLogParts:  []string{"Session complexity reused:", "effective=COMPLEX", "reason=complex-ceiling"},
		},
	}

	for _, tt := range tests {
		ctx := complexitySessionContext("session-ladder")
		require.NoError(t, plugin.PreRequestHook(ctx, chatRequest(tt.requestText)))
		require.Equal(t, tt.wantTier, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
		require.Equal(t, tt.wantMechanism, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))

		var sessionLog string
		for _, entry := range ctx.GetRoutingEngineLogs() {
			if strings.HasPrefix(entry.Message, "Session complexity ") {
				sessionLog = entry.Message
				break
			}
		}
		require.NotEmpty(t, sessionLog)
		for _, part := range tt.wantLogParts {
			require.Contains(t, sessionLog, part)
		}
		if strings.Contains(sessionLog, "proposed=") {
			require.NotContains(t, sessionLog, " similarity=", "proposal evidence must not look like evidence for the effective tier")
			require.NotContains(t, sessionLog, " matched=", "proposal evidence must not look like evidence for the effective tier")
		}
	}
}

func TestPreRequestHook_SessionComplexityIsIsolatedBySessionID(t *testing.T) {
	plugin := newSessionComplexityRuleFixture(t)
	plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(sessionAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	complexCtx := complexitySessionContext("session-a")
	require.NoError(t, plugin.PreRequestHook(complexCtx, chatRequest("a complex request")))
	require.Equal(t, complexity.TierComplex, complexCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))

	simpleCtx := complexitySessionContext("session-b")
	require.NoError(t, plugin.PreRequestHook(simpleCtx, chatRequest("a simple request")))
	require.Equal(t, complexity.TierSimple, simpleCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSemantic, simpleCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

func TestPreRequestHook_SessionModeWithoutIdentityRemainsPerRequest(t *testing.T) {
	plugin := newSessionComplexityRuleFixture(t)
	plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(sessionAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	complexCtx := complexitySessionContext("")
	require.NoError(t, plugin.PreRequestHook(complexCtx, chatRequest("a complex request")))
	require.Equal(t, complexity.TierComplex, complexCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))

	simpleCtx := complexitySessionContext("")
	require.NoError(t, plugin.PreRequestHook(simpleCtx, chatRequest("a simple request")))
	require.Equal(t, complexity.TierSimple, simpleCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSemantic, simpleCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

func TestPreRequestHook_SessionStoreFailureFallsBackToCurrentClassification(t *testing.T) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	plugin := newComplexityRuleFixtureWithConfig(t, &routing.Config{KVStore: store})
	plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(sessionAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)
	require.NoError(t, store.Close())

	ctx := complexitySessionContext("store-failure")
	require.NoError(t, plugin.PreRequestHook(ctx, chatRequest("a medium request")))
	require.Equal(t, complexity.TierMedium, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSemantic, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

func TestPreRequestHook_SessionContinuationReusesOrClassifiesRecoveredTask(t *testing.T) {
	plugin := newSessionComplexityRuleFixture(t)
	plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(sessionAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	continuationRequest := func() *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: chatString("a complex request")},
					{Role: schemas.ChatMessageRoleAssistant, Content: chatString("Calling the tool")},
					{Role: schemas.ChatMessageRoleTool, Content: chatString("Tool result received")},
				},
			},
		}
	}

	absentCtx := complexitySessionContext("new-session")
	require.NoError(t, plugin.PreRequestHook(absentCtx, continuationRequest()))
	require.Equal(t, complexity.TierComplex, absentCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSemantic, absentCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))

	initialCtx := complexitySessionContext("existing-session")
	require.NoError(t, plugin.PreRequestHook(initialCtx, chatRequest("a medium request")))
	require.Equal(t, complexity.TierMedium, initialCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))

	continuationCtx := complexitySessionContext("existing-session")
	require.NoError(t, plugin.PreRequestHook(continuationCtx, continuationRequest()))
	require.Equal(t, complexity.TierMedium, continuationCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSession, continuationCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Nil(t, continuationCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore))
}

func TestPreRequestHook_ComplexSessionSkipsLaterClassifierCalls(t *testing.T) {
	plugin := newSessionComplexityRuleFixture(t)
	var calls atomic.Int64
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		calls.Add(1)
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(sessionAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	beforeRequest := calls.Load()
	complexCtx := complexitySessionContext("complex-ceiling")
	require.NoError(t, plugin.PreRequestHook(complexCtx, chatRequest("a complex request")))
	require.Equal(t, beforeRequest+1, calls.Load(), "the first human turn should be classified")

	secondCtx := complexitySessionContext("complex-ceiling")
	require.NoError(t, plugin.PreRequestHook(secondCtx, chatRequest("a simple request")))
	require.Equal(t, beforeRequest+1, calls.Load(), "COMPLEX is the ceiling, so later turns need no classifier call")
	require.Equal(t, complexity.TierComplex, secondCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSession, secondCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Nil(t, secondCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore))
}

func TestPreRequestHook_SemanticComplexityNotReadyLogsInfo(t *testing.T) {
	plugin := newComplexityRuleFixture(t)
	warmupStarted := make(chan struct{}, 1)
	releaseWarmup := make(chan struct{})
	defer close(releaseWarmup)

	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		select {
		case warmupStarted <- struct{}{}:
		default:
		}
		<-releaseWarmup
		return testEmbeddingExecutor(ctx, req)
	})
	plugin.ReloadComplexityAnalyzerConfig(&complexity.AnalyzerConfig{
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords:  []string{"a casual greeting"},
			MediumKeywords:  []string{"an implementation detail question"},
			ComplexKeywords: []string{"a deep architectural tradeoff analysis"},
		},
		Semantic: &configstore.ComplexitySemanticConfig{
			Provider:       "openai",
			EmbeddingModel: "test-embedding-model",
		},
	})

	select {
	case <-warmupStarted:
	case <-time.After(time.Second):
		t.Fatal("semantic warmup did not start")
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, chatRequest("hey, quick question")))

	logs := bfCtx.GetRoutingEngineLogs()
	var outcome *schemas.RoutingEngineLogEntry
	for i := range logs {
		entry := &logs[i]
		if strings.Contains(entry.Message, "Semantic complexity classification unavailable") {
			outcome = entry
			break
		}
	}
	require.NotNil(t, outcome, "expected semantic no-classification outcome log")
	require.Equal(t, schemas.LogLevelInfo, outcome.Level)
}

// TestPreRequestHook_ComplexityUnsupportedInputRecordsSkippedMechanism pins
// that a request type the extractor cannot read records mechanism=skipped
// instead of silently publishing nothing.
func TestPreRequestHook_ComplexityUnsupportedInputRecordsSkippedMechanism(t *testing.T) {
	plugin := newComplexityRuleFixture(t)

	req := &schemas.BifrostRequest{
		RequestType: schemas.EmbeddingRequest,
		EmbeddingRequest: &schemas.BifrostEmbeddingRequest{
			Provider: schemas.OpenAI,
			Model:    "text-embedding-3-small",
			Input:    &schemas.EmbeddingInput{Text: schemas.Ptr("some text")},
		},
	}
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	mechanism, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	require.True(t, ok, "skipped mechanism should be recorded for unsupported input")
	require.Equal(t, complexity.MechanismSkipped, mechanism)
}

func TestPreRequestHook_ComplexitySkippedWhenNoRulesReferenceIt(t *testing.T) {
	logger := rules.NewMockLogger()
	provider := "openai"
	model := "gpt-4o-mini"

	ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)
	require.NoError(t, ruleStore.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "Always match",
		CelExpression: "true",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &model, Weight: 1.0},
		},
		Enabled:  schemas.Ptr(true),
		Scope:    "global",
		Priority: 0,
	}))

	plugin, err := routing.InitFromStore(context.Background(), nil, logger, nil, ruleStore, routing.NewMockGovernance())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: chatString("Hello"),
				},
			},
		},
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	logs := bfCtx.GetRoutingEngineLogs()
	for _, entry := range logs {
		if entry.Engine == schemas.RoutingEngineRoutingRule && strings.Contains(entry.Message, "Complexity") {
			t.Fatalf("expected no complexity logs when no rules reference complexity_tier, got: %s", entry.Message)
		}
	}
}

// semanticAnalyzerConfig is the non-session semantic configuration the
// guardrail tests use: a usable embedding classifier with no session state.
func semanticAnalyzerConfig() *complexity.AnalyzerConfig {
	return &complexity.AnalyzerConfig{
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords:  []string{"a casual greeting"},
			MediumKeywords:  []string{"an implementation detail question"},
			ComplexKeywords: []string{"a deep architectural tradeoff analysis"},
		},
		Semantic: &configstore.ComplexitySemanticConfig{
			Provider:       "openai",
			EmbeddingModel: "test-embedding-model",
		},
	}
}

// TestPreRequestHook_GuardrailBlockSkipsClassification pins that a blocking
// guardrail rule stops classification before the embedding provider is called:
// no classification sub-request leaves the process, no tier is published, and
// the decision is named in the routing log.
func TestPreRequestHook_GuardrailBlockSkipsClassification(t *testing.T) {
	plugin := newComplexityRuleFixture(t)
	var embedCalls atomic.Int64
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embedCalls.Add(1)
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(semanticAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	before := embedCalls.Load()
	evaluator := &fakeGuardrailTextEvaluator{blockOn: "social security"}
	plugin.SetGuardrailTextEvaluator(evaluator)

	req := chatRequest("here is my social security number")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, before, embedCalls.Load(), "blocked classification must not reach the embedding provider")
	require.Equal(t, []string{"here is my social security number"}, evaluator.evaluatedTexts())
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	_, hasTier := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier).(string)
	require.False(t, hasTier, "blocked classification must not publish a tier")

	var sawGuardrailLog bool
	for _, entry := range bfCtx.GetRoutingEngineLogs() {
		if strings.Contains(entry.Message, "Guardrail rule blocked the complexity classification input") {
			sawGuardrailLog = true
		}
	}
	require.True(t, sawGuardrailLog, "the routing log should name the guardrail outcome")

	// The complexity rule could not match an unpublished tier, so the request
	// keeps the model it arrived with.
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o", modelOut)
}

// TestPreRequestHook_GuardrailRedactionReachesClassifier pins the other half of
// the contract: when guardrails redact rather than block, the embedding
// provider receives the redacted text, never the raw input.
func TestPreRequestHook_GuardrailRedactionReachesClassifier(t *testing.T) {
	plugin := newComplexityRuleFixture(t)
	var mu sync.Mutex
	var embedded []string
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		if req.Input != nil && req.Input.Text != nil {
			mu.Lock()
			embedded = append(embedded, *req.Input.Text)
			mu.Unlock()
		}
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(semanticAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	mu.Lock()
	embedded = nil
	mu.Unlock()
	plugin.SetGuardrailTextEvaluator(&fakeGuardrailTextEvaluator{replace: map[string]string{"casual greeting": "[GREETING]"}})

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, chatRequest("a casual greeting")))

	mu.Lock()
	got := append([]string(nil), embedded...)
	mu.Unlock()
	require.Contains(t, got, "a [GREETING]")
	require.NotContains(t, got, "a casual greeting", "raw text must not reach the embedding provider")
	require.Equal(t, complexity.MechanismSemantic, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

// TestPreRequestHook_GuardrailEvaluatesPriorTurns pins that the router guards
// every user turn it carries, not only the latest one: a match on an earlier
// turn skips classification rather than being forwarded.
func TestPreRequestHook_GuardrailEvaluatesPriorTurns(t *testing.T) {
	plugin := newComplexityRuleFixture(t)
	var embedCalls atomic.Int64
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embedCalls.Add(1)
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(semanticAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	before := embedCalls.Load()
	evaluator := &fakeGuardrailTextEvaluator{blockOn: "ssn"}
	plugin.SetGuardrailTextEvaluator(evaluator)

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: chatString("my ssn is 123-45-6789")},
				{Role: schemas.ChatMessageRoleAssistant, Content: chatString("noted")},
				{Role: schemas.ChatMessageRoleUser, Content: chatString("what's the weather?")},
			},
		},
	}
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, before, embedCalls.Load(), "a blocked prior turn must stop classification")
	require.Contains(t, evaluator.evaluatedTexts(), "my ssn is 123-45-6789")
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

// TestPreRequestHook_GuardrailNotConsultedOnSessionReuse pins that guardrails
// only gate classification: a session-reused tier sends no text to any provider,
// so the evaluator is not consulted at all.
func TestPreRequestHook_GuardrailNotConsultedOnSessionReuse(t *testing.T) {
	plugin := newSessionComplexityRuleFixture(t)
	plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(sessionAnalyzerConfig()))
	waitForSemanticClassifier(t, plugin)

	evaluator := &fakeGuardrailTextEvaluator{}
	plugin.SetGuardrailTextEvaluator(evaluator)

	firstCtx := complexitySessionContext("guardrail-reuse")
	require.NoError(t, plugin.PreRequestHook(firstCtx, chatRequest("a complex request")))
	require.Equal(t, complexity.TierComplex, firstCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.NotEmpty(t, evaluator.evaluatedTexts(), "a classified turn must be guarded")

	evaluator.resetCalls()
	secondCtx := complexitySessionContext("guardrail-reuse")
	require.NoError(t, plugin.PreRequestHook(secondCtx, chatRequest("a simple request")))
	require.Equal(t, complexity.MechanismSession, secondCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Empty(t, evaluator.evaluatedTexts(), "a session-reused tier sends nothing to a provider, so guardrails are not consulted")
}
