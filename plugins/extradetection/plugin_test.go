package extradetection

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

// pluginLogText joins the request's plugin log entries, which is what the UI's
// "Plugin logs" tab renders.
func pluginLogText(t *testing.T, ctx *schemas.BifrostContext) string {
	t.Helper()
	entries := ctx.GetPluginLogs()
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Message)
		b.WriteByte('\n')
	}
	return b.String()
}

func chatReq(messages []schemas.ChatMessage) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Input: messages},
	}
}

func userMsg(text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr(text)}}
}

func assistantMsg(text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: strPtr(text)}}
}

// ctxWithHeaders builds a plugin-scoped context so ctx.Log records entries, the
// way the pipeline scopes it before calling PreRequestHook. Without the scope,
// ctx.Log is a no-op and there would be nothing to assert on.
func ctxWithHeaders(t *testing.T, headers map[string]string) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	scoped := ctx.WithPluginScope(schemas.Ptr(PluginName))
	t.Cleanup(scoped.ReleasePluginScope)
	if headers != nil {
		scoped.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	}
	return scoped
}

func headersOf(t *testing.T, ctx *schemas.BifrostContext) map[string]string {
	t.Helper()
	h, ok := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	require.True(t, ok, "expected headers map in context")
	return h
}

func gitCommitPlugin(t *testing.T) *Plugin {
	t.Helper()
	p, err := Init(DefaultConfig(), nil)
	require.NoError(t, err)
	return p
}

func TestPreRequestHook_GitCommitSetsSimple(t *testing.T) {
	for _, text := range []string{
		"please git commit this change",
		"commit these git changes",
		"GIT COMMIT -m 'fix'",
		"  git commit  ",
	} {
		p := gitCommitPlugin(t)
		ctx := ctxWithHeaders(t, nil)
		require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{userMsg(text)})))
		h := headersOf(t, ctx)
		require.Equal(t, "simple", h["complexity_tier"], "text %q should match", text)
	}
}

func TestPreRequestHook_SingleKeywordDoesNotMatch(t *testing.T) {
	for _, text := range []string{
		"please git push this change",
		"commit this change",
		"what is a commit?",
	} {
		p := gitCommitPlugin(t)
		ctx := ctxWithHeaders(t, nil)
		require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{userMsg(text)})))
		h := headersOf(t, ctx)
		_, ok := h["complexity_tier"]
		require.False(t, ok, "text %q must not match", text)
	}
}

func TestPreRequestHook_QuestionPrefix(t *testing.T) {
	for _, text := range []string{
		"question: how does this work?",
		"Question how to deploy?",
		"  QUESTIONS about pricing",
	} {
		p := gitCommitPlugin(t)
		ctx := ctxWithHeaders(t, nil)
		require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{userMsg(text)})))
		h := headersOf(t, ctx)
		require.Equal(t, "simple", h["complexity_tier"], "text %q should match", text)
	}
}

func TestPreRequestHook_QuestionMidSentenceDoesNotMatch(t *testing.T) {
	p := gitCommitPlugin(t)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{userMsg("I have a question about pricing")})))
	_, ok := headersOf(t, ctx)["complexity_tier"]
	require.False(t, ok)
}

func TestPreRequestHook_OnlyLastUserMessageScanned(t *testing.T) {
	p := gitCommitPlugin(t)
	ctx := ctxWithHeaders(t, nil)
	// Earlier turn matches, last turn does not — window of 1 must not match.
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{
		userMsg("please git commit this"),
		assistantMsg("done"),
		userMsg("now explain quantum physics in depth"),
	})))
	_, ok := headersOf(t, ctx)["complexity_tier"]
	require.False(t, ok)
}

func TestPreRequestHook_WindowScansPreviousUserMessages(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true,
		Rules: []Rule{{
			ID: 1, Name: "git", Enabled: true, Header: "complexity_tier", Value: "simple",
			MaxMessagesToScan: 2,
			Match:             Match{Scope: "last_user_messages", AllOf: []string{"git", "commit"}},
		}},
	}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{
		userMsg("please git commit this"),
		assistantMsg("done"),
		userMsg("thanks"),
	})))
	require.Equal(t, "simple", headersOf(t, ctx)["complexity_tier"])
}

func TestPreRequestHook_AssistantAndToolMessagesSkipped(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true,
		Rules: []Rule{{
			ID: 1, Name: "git", Enabled: true, Header: "x-detect", Value: "yes",
			MaxMessagesToScan: 1,
			Match:             Match{Scope: "last_user_messages", AllOf: []string{"secret-tool-output"}},
		}},
	}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{
		userMsg("hello"),
		{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: strPtr("secret-tool-output")}},
		assistantMsg("secret-tool-output"),
		userMsg("hello again"),
	})))
	_, ok := headersOf(t, ctx)["x-detect"]
	require.False(t, ok, "tool/assistant text must never match a user window")
}

func TestPreRequestHook_TokenEstimateStamped(t *testing.T) {
	srv, _ := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":4321}`))
	})
	p := countingPlugin(t, srv.URL, CountingRule{
		ID: 1, Name: "openai", Enabled: true,
		Provider: "openai", ModelPattern: "gpt-5*", ModelPatternType: ModelPatternGlob,
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})
	ctx := ctxWithHeaders(t, map[string]string{"content-type": "application/json"})
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI, Model: "gpt-5.1",
			Input: []schemas.ChatMessage{userMsg("hello world")},
		},
	}
	require.NoError(t, p.PreRequestHook(ctx, req))
	h := headersOf(t, ctx)
	// Original headers preserved, keys lowercased.
	require.Equal(t, "application/json", h["content-type"])
	require.Equal(t, "4321", h["estimated_tokens"])
	require.Equal(t, h["estimated_tokens"], h["x-estimated-tokens"])
}

func TestPreRequestHook_EstimateFormula(t *testing.T) {
	// Padding is off by default: these endpoints count exactly.
	require.Equal(t, 100, (&TokenCountingConfig{}).estimateFromCount(100))
	// Conservative padding stays available for proxy counting, rounded up.
	require.Equal(t, 110, (&TokenCountingConfig{PaddingRatio: 0.10}).estimateFromCount(100))
	require.Equal(t, 2, (&TokenCountingConfig{PaddingRatio: 0.10}).estimateFromCount(1))
}

func TestPreRequestHook_DisabledPluginStampsNothing(t *testing.T) {
	p, err := Init(&Config{Enabled: false}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{userMsg("git commit please")})))
	h, ok := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	require.False(t, ok && len(h) > 0, "disabled plugin must not stamp headers")
}

func TestPreRequestHook_NilSafe(t *testing.T) {
	p := gitCommitPlugin(t)
	require.NoError(t, p.PreRequestHook(nil, chatReq([]schemas.ChatMessage{userMsg("hi")})))
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, nil))
}

func TestPreRequestHook_LastWriteWins(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true,
		Rules: []Rule{
			{ID: 1, Name: "a", Enabled: true, Header: "x-tier", Value: "first", Match: Match{AnyOf: []string{"hello"}}},
			{ID: 2, Name: "b", Enabled: true, Header: "x-tier", Value: "second", Match: Match{AnyOf: []string{"hello"}}},
		},
	}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{userMsg("hello")})))
	require.Equal(t, "second", headersOf(t, ctx)["x-tier"])
}

func TestPreRequestHook_DisabledRuleSkipped(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true,
		Rules: []Rule{
			{ID: 1, Name: "a", Enabled: false, Header: "x-tier", Value: "nope", Match: Match{AnyOf: []string{"hello"}}},
		},
	}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{userMsg("hello")})))
	_, ok := headersOf(t, ctx)["x-tier"]
	require.False(t, ok)
}

func TestPreRequestHook_FullInputScope(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true,
		Rules: []Rule{{
			ID: 1, Name: "sys", Enabled: true, Header: "x-tier", Value: "sys",
			Match: Match{Scope: "full_input", AllOf: []string{"codename"}},
		}},
	}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, chatReq([]schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: strPtr("codename phoenix")}},
		userMsg("hello"),
	})))
	require.Equal(t, "sys", headersOf(t, ctx)["x-tier"])
}

func TestPreRequestHook_InvalidRulesDropped(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true,
		Rules: []Rule{
			{ID: 1, Name: "bad", Enabled: true, Header: "", Value: "x", Match: Match{AnyOf: []string{"a"}}},
			{ID: 2, Name: "good", Enabled: true, Header: "x-ok", Value: "yes", Match: Match{AnyOf: []string{"hi"}}},
			{ID: 3, Name: "empty-match", Enabled: true, Header: "x-bad", Value: "x", Match: Match{}},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, p.config.Rules, 1)
	require.Equal(t, "x-ok", p.config.Rules[0].Header)
}

func TestPreRequestHook_ResponsesRequest(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	p := gitCommitPlugin(t)
	ctx := ctxWithHeaders(t, nil)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{
				{Role: &userRole, Content: &schemas.ResponsesMessageContent{ContentStr: strPtr("please git commit this")}},
			},
		},
	}
	require.NoError(t, p.PreRequestHook(ctx, req))
	require.Equal(t, "simple", headersOf(t, ctx)["complexity_tier"])
}

func TestPreRequestHook_TextCompletionRequest(t *testing.T) {
	p := gitCommitPlugin(t)
	ctx := ctxWithHeaders(t, nil)
	req := &schemas.BifrostRequest{
		RequestType:           schemas.TextCompletionRequest,
		TextCompletionRequest: &schemas.BifrostTextCompletionRequest{Input: &schemas.TextCompletionInput{PromptStr: strPtr("question: what is this?")}},
	}
	require.NoError(t, p.PreRequestHook(ctx, req))
	require.Equal(t, "simple", headersOf(t, ctx)["complexity_tier"])
}

// --- token counting -------------------------------------------------------

// countServer starts a stub counting endpoint and records the last request it
// received, so tests can assert both the response handling and the outbound
// shape (URL, auth header, body) for each endpoint style.
type countRecorder struct {
	mu       sync.Mutex
	path     string
	auth     string
	body     []byte
	requests int
}

func countServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *countRecorder) {
	t.Helper()
	rec := &countRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.path, rec.auth, rec.body = r.URL.Path, r.Header.Get("Authorization"), body
		rec.requests++
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *countRecorder) snapshot() (string, string, []byte, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.path, r.auth, r.body, r.requests
}

func (r *countRecorder) rawBody() []byte {
	_, _, body, _ := r.snapshot()
	return body
}

func (r *countRecorder) bodyJSON(t *testing.T) map[string]any {
	t.Helper()
	_, _, body, _ := r.snapshot()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out), "counting body must be JSON: %s", body)
	return out
}

// countingPlugin builds a plugin with one counting rule pointed at a stub
// endpoint. The OpenAI base URL override carries the test server address, so
// the real endpoint URLs are never contacted.
func countingPlugin(t *testing.T, baseURL string, rules ...CountingRule) *Plugin {
	t.Helper()
	p, err := Init(&Config{
		Enabled:        true,
		EstimateTokens: true,
		TokenCounting: &TokenCountingConfig{
			APIKeys:  map[Endpoint]*schemas.SecretVar{EndpointOpenAI: schemas.NewSecretVar("sk-test")},
			BaseURLs: map[Endpoint]string{EndpointOpenAI: baseURL},
			Rules:    rules,
		},
	}, nil)
	require.NoError(t, err)
	return p
}

func openAIChatReq(provider schemas.ModelProvider, model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: provider, Model: model,
			Input: []schemas.ChatMessage{userMsg("hello world")},
		},
	}
}

// The operator's worked example: "cline/cline-free/gemini-3.8-flash" is not
// listed, so it gets no estimate — and the request still completes untouched.
func TestTokenCounting_UnlistedModelSkipsHeaderAndSucceeds(t *testing.T) {
	srv, rec := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":10}`))
	})
	p := countingPlugin(t, srv.URL, CountingRule{
		ID: 1, Name: "openrouter", Enabled: true,
		Provider: "openrouter", ModelPattern: "*", ModelPatternType: ModelPatternGlob,
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.Cline, "cline/cline-free/gemini-3.8-flash")))

	h := headersOf(t, ctx)
	require.NotContains(t, h, "estimated_tokens", "unlisted model must get no estimate")
	_, _, _, requests := rec.snapshot()
	require.Zero(t, requests, "no counting call may be made for an unlisted model")
}

// Provider "openrouter" with model "*" covers every openrouter/... request,
// including a nested namespace the model name itself contains.
func TestTokenCounting_ProviderWildcardCoversPrefixedModel(t *testing.T) {
	srv, rec := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":777}`))
	})
	p := countingPlugin(t, srv.URL, CountingRule{
		ID: 1, Name: "openrouter all", Enabled: true,
		Provider: "openrouter", ModelPattern: "*", ModelPatternType: ModelPatternGlob,
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.OpenRouter, "openrouter/google/gemini-3-flash")))

	require.Equal(t, "777", headersOf(t, ctx)["estimated_tokens"])
	path, auth, _, requests := rec.snapshot()
	require.Equal(t, 1, requests)
	require.Equal(t, "/v1/responses/input_tokens", path)
	require.Equal(t, "Bearer sk-test", auth)
	// The rule's count_model is sent, not the request's own model name.
	require.Equal(t, "gpt-5", rec.bodyJSON(t)["model"])
}

// An SDK-style request that carries its provider only in the model string must
// still match, since routing has not run yet at PreRequestHook time.
func TestTokenCounting_MatchesProviderFromModelString(t *testing.T) {
	srv, _ := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":55}`))
	})
	p := countingPlugin(t, srv.URL, CountingRule{
		ID: 1, Name: "openrouter", Enabled: true,
		Provider: "openrouter", ModelPattern: "gpt-*", ModelPatternType: ModelPatternGlob,
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})
	ctx := ctxWithHeaders(t, nil)
	// Provider empty; the model string carries the prefix.
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq("", "openrouter/gpt-4o-mini")))
	require.Equal(t, "55", headersOf(t, ctx)["estimated_tokens"])
}

func TestTokenCounting_FirstMatchingRuleWins(t *testing.T) {
	srv, rec := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":1}`))
	})
	p := countingPlugin(t, srv.URL,
		CountingRule{ID: 1, Name: "first", Enabled: true, Provider: "*", ModelPattern: "*",
			Endpoint: EndpointOpenAI, CountModel: "gpt-5"},
		CountingRule{ID: 2, Name: "second", Enabled: true, Provider: "*", ModelPattern: "*",
			Endpoint: EndpointOpenAI, CountModel: "gpt-4o"},
	)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.OpenAI, "gpt-5.1")))
	require.Equal(t, "gpt-5", rec.bodyJSON(t)["model"], "array order is priority")
}

func TestTokenCounting_DisabledRuleSkipped(t *testing.T) {
	srv, rec := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":1}`))
	})
	p := countingPlugin(t, srv.URL, CountingRule{
		ID: 1, Name: "off", Enabled: false, Provider: "*", ModelPattern: "*",
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.OpenAI, "gpt-5.1")))
	require.NotContains(t, headersOf(t, ctx), "estimated_tokens")
	_, _, _, requests := rec.snapshot()
	require.Zero(t, requests)
}

func TestTokenCounting_ModelPatternTypes(t *testing.T) {
	cases := []struct {
		pattern, patternType, model string
		want                        bool
	}{
		{"gemini-*", ModelPatternGlob, "gemini-3.8-flash", true},
		{"gemini-*", ModelPatternGlob, "gpt-5", false},
		{"*", ModelPatternGlob, "anything/at/all", true},
		{"^gemini-[0-9.]+$", ModelPatternRegex, "gemini-3.8", true},
		{"^gemini-[0-9.]+$", ModelPatternRegex, "gemini-flash", false},
		{"gpt-4", ModelPatternExact, "gpt-4", true},
		{"gpt-4", ModelPatternExact, "gpt-4o", false},
	}
	for _, tc := range cases {
		rule := &CountingRule{ModelPattern: tc.pattern, ModelPatternType: tc.patternType}
		require.Equal(t, tc.want, rule.matchesModel(tc.model),
			"pattern %q (%s) vs %q", tc.pattern, tc.patternType, tc.model)
	}
}

// Every failure path must withhold the header, log the reason to the request's
// plugin logs, and still return nil so the request reaches routing.
func TestTokenCounting_FailuresNeverStampOrError(t *testing.T) {
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		apiKey  *schemas.SecretVar
		wantLog string
	}{
		{
			name:    "upstream 500",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
			apiKey:  schemas.NewSecretVar("sk-test"),
			wantLog: "returned HTTP 500",
		},
		{
			name:    "unparseable body",
			handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`not json`)) },
			apiKey:  schemas.NewSecretVar("sk-test"),
			wantLog: `could not read "input_tokens"`,
		},
		{
			name:    "missing token field",
			handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"input_tokens":0}`)) },
			apiKey:  schemas.NewSecretVar("sk-test"),
			wantLog: `could not read "input_tokens"`,
		},
		{
			name:    "missing api key",
			handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"input_tokens":9}`)) },
			apiKey:  nil,
			wantLog: "no api key configured",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := countServer(t, tc.handler)
			p, err := Init(&Config{
				Enabled: true, EstimateTokens: true,
				TokenCounting: &TokenCountingConfig{
					APIKeys:  map[Endpoint]*schemas.SecretVar{EndpointOpenAI: tc.apiKey},
					BaseURLs: map[Endpoint]string{EndpointOpenAI: srv.URL},
					Rules: []CountingRule{{ID: 1, Name: "r", Enabled: true,
						Provider: "*", ModelPattern: "*", Endpoint: EndpointOpenAI, CountModel: "gpt-5"}},
				},
			}, nil)
			require.NoError(t, err)

			ctx := ctxWithHeaders(t, nil)
			require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.OpenAI, "gpt-5.1")),
				"a counting failure must never fail the request")
			require.NotContains(t, headersOf(t, ctx), "estimated_tokens")
			require.Contains(t, pluginLogText(t, ctx), tc.wantLog)
		})
	}
}

func TestTokenCounting_TimeoutWithholdsHeader(t *testing.T) {
	srv, _ := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte(`{"input_tokens":9}`))
	})
	p, err := Init(&Config{
		Enabled: true, EstimateTokens: true,
		TokenCounting: &TokenCountingConfig{
			TimeoutMS: 50,
			APIKeys:   map[Endpoint]*schemas.SecretVar{EndpointOpenAI: schemas.NewSecretVar("sk-test")},
			BaseURLs:  map[Endpoint]string{EndpointOpenAI: srv.URL},
			Rules: []CountingRule{{ID: 1, Name: "r", Enabled: true,
				Provider: "*", ModelPattern: "*", Endpoint: EndpointOpenAI, CountModel: "gpt-5"}},
		},
	}, nil)
	require.NoError(t, err)

	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.OpenAI, "gpt-5.1")))
	require.NotContains(t, headersOf(t, ctx), "estimated_tokens")
	require.Contains(t, pluginLogText(t, ctx), "timed out after 50ms")
}

func TestTokenCounting_DisabledPluginAndNoConfigSkipCounting(t *testing.T) {
	srv, rec := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":9}`))
	})
	rule := CountingRule{ID: 1, Name: "r", Enabled: true, Provider: "*", ModelPattern: "*",
		Endpoint: EndpointOpenAI, CountModel: "gpt-5"}

	// estimate_tokens off: the whole feature is gated.
	off, err := Init(&Config{Enabled: true, EstimateTokens: false,
		TokenCounting: &TokenCountingConfig{
			APIKeys:  map[Endpoint]*schemas.SecretVar{EndpointOpenAI: schemas.NewSecretVar("sk-test")},
			BaseURLs: map[Endpoint]string{EndpointOpenAI: srv.URL},
			Rules:    []CountingRule{rule},
		}}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, off.PreRequestHook(ctx, openAIChatReq(schemas.OpenAI, "gpt-5.1")))
	require.NotContains(t, headersOf(t, ctx), "estimated_tokens")

	// No token_counting block at all.
	none, err := Init(&Config{Enabled: true, EstimateTokens: true}, nil)
	require.NoError(t, err)
	ctx2 := ctxWithHeaders(t, nil)
	require.NoError(t, none.PreRequestHook(ctx2, openAIChatReq(schemas.OpenAI, "gpt-5.1")))
	require.NotContains(t, headersOf(t, ctx2), "estimated_tokens")

	_, _, _, requests := rec.snapshot()
	require.Zero(t, requests)
}

// An unknown endpoint drops the rule at load with a warning, and the plugin
// still loads: the operator loses a token estimate, not their gateway.
func TestTokenCounting_UnknownEndpointDropsRuleAndStillLoads(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true, EstimateTokens: true,
		TokenCounting: &TokenCountingConfig{
			Rules: []CountingRule{{ID: 1, Name: "anthropic", Enabled: true,
				Provider: "*", ModelPattern: "*", Endpoint: Endpoint("anthropic"), CountModel: "claude"}},
		},
	}, nil)
	require.NoError(t, err, "an unusable counting rule must not fail plugin load")
	require.Empty(t, p.config.TokenCounting.Rules)

	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.Anthropic, "claude-sonnet-4")))
	require.NotContains(t, headersOf(t, ctx), "estimated_tokens")
}

func TestTokenCounting_InvalidRulesDropped(t *testing.T) {
	p, err := Init(&Config{
		Enabled: true, EstimateTokens: true,
		TokenCounting: &TokenCountingConfig{
			Rules: []CountingRule{
				{ID: 1, Name: "no-model", Enabled: true, Provider: "*", ModelPattern: "",
					Endpoint: EndpointOpenAI, CountModel: "gpt-5"},
				{ID: 2, Name: "no-count-model", Enabled: true, Provider: "*", ModelPattern: "*",
					Endpoint: EndpointOpenAI, CountModel: ""},
				{ID: 3, Name: "bad-type", Enabled: true, Provider: "*", ModelPattern: "*",
					ModelPatternType: "fuzzy", Endpoint: EndpointOpenAI, CountModel: "gpt-5"},
				{ID: 4, Name: "good", Enabled: true, Provider: "*", ModelPattern: "gpt-*",
					Endpoint: EndpointOpenAI, CountModel: "gpt-5"},
			},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, p.config.TokenCounting.Rules, 1)
	require.Equal(t, "good", p.config.TokenCounting.Rules[0].Name)
}

func TestTokenCounting_DefaultsNormalized(t *testing.T) {
	p, err := Init(&Config{Enabled: true, EstimateTokens: true,
		TokenCounting: &TokenCountingConfig{Rules: []CountingRule{{ID: 1, Name: "r", Enabled: true,
			Provider: "", ModelPattern: " gpt-* ", Endpoint: EndpointOpenAI, CountModel: " gpt-5 "}}}}, nil)
	require.NoError(t, err)
	tc := p.config.TokenCounting
	require.Equal(t, DefaultCountTimeoutMS, tc.TimeoutMS)
	require.Equal(t, ProviderWildcard, tc.Rules[0].Provider, "empty provider becomes the wildcard")
	require.Equal(t, ModelPatternGlob, tc.Rules[0].ModelPatternType)
	require.Equal(t, "gpt-*", tc.Rules[0].ModelPattern)
	require.Equal(t, "gpt-5", tc.Rules[0].CountModel)
}

// The Gemini arm sends a countTokens envelope, not a Responses body.
func TestTokenCounting_GeminiEndpointShape(t *testing.T) {
	srv, rec := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"totalTokens":2048}`))
	})
	p, err := Init(&Config{
		Enabled: true, EstimateTokens: true,
		TokenCounting: &TokenCountingConfig{
			APIKeys:  map[Endpoint]*schemas.SecretVar{EndpointGemini: schemas.NewSecretVar("goog-key")},
			BaseURLs: map[Endpoint]string{EndpointGemini: srv.URL},
			Rules: []CountingRule{{ID: 1, Name: "gemini", Enabled: true, Provider: "*",
				ModelPattern: "gemini-*", Endpoint: EndpointGemini, CountModel: "gemini-3-flash"}},
		},
	}, nil)
	require.NoError(t, err)

	assistant := schemas.ResponsesInputMessageRoleAssistant
	developer := schemas.ResponsesInputMessageRoleDeveloper
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Gemini, Model: "gemini-3.8-flash",
			Input: []schemas.ResponsesMessage{
				{Role: &developer, Content: &schemas.ResponsesMessageContent{ContentStr: strPtr("be terse")}},
				{Role: &assistant, Content: &schemas.ResponsesMessageContent{ContentStr: strPtr("hi")}},
			},
		},
	}))

	require.Equal(t, "2048", headersOf(t, ctx)["estimated_tokens"])
	path, auth, _, requests := rec.snapshot()
	require.Equal(t, 1, requests)
	require.Equal(t, "/models/gemini-3-flash:countTokens", path,
		"the model is a path segment; the v1beta prefix comes from the base URL")
	require.Empty(t, auth, "gemini authenticates with x-goog-api-key, not a bearer token")

	body := rec.bodyJSON(t)
	inner, ok := body["generateContentRequest"].(map[string]any)
	require.True(t, ok, "body must carry the generateContentRequest envelope: %v", body)
	require.Equal(t, "models/gemini-3-flash", inner["model"], "inner model must be fully qualified")
	contents, ok := body["contents"].([]any)
	require.True(t, ok)
	require.Len(t, contents, 1, "developer/assistant turns do not belong in contents")
	require.NotNil(t, inner["systemInstruction"], "developer turn belongs in systemInstruction")
}

// The OpenAI body carries only what a count reads.
func TestTokenCounting_OpenAIBodyShape(t *testing.T) {
	srv, rec := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":12}`))
	})
	p := countingPlugin(t, srv.URL, CountingRule{
		ID: 1, Name: "r", Enabled: true, Provider: "*", ModelPattern: "*",
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI, Model: "gpt-5.1",
			Params: &schemas.ChatParameters{Temperature: schemas.Ptr(0.7), MaxCompletionTokens: schemas.Ptr(100)},
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: strPtr("sys")}},
				userMsg("hi"),
			},
		},
	}))

	body := rec.bodyJSON(t)
	require.Equal(t, "gpt-5", body["model"])
	require.Contains(t, body, "input")
	require.NotContains(t, body, "temperature", "sampling params are not sent to a count endpoint")
	require.NotContains(t, body, "max_output_tokens")
}

// Padding applies to the count the endpoint returned.
func TestTokenCounting_PaddingApplied(t *testing.T) {
	srv, _ := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":100}`))
	})
	p, err := Init(&Config{
		Enabled: true, EstimateTokens: true,
		TokenCounting: &TokenCountingConfig{
			PaddingRatio: 0.10,
			APIKeys:      map[Endpoint]*schemas.SecretVar{EndpointOpenAI: schemas.NewSecretVar("sk-test")},
			BaseURLs:     map[Endpoint]string{EndpointOpenAI: srv.URL},
			Rules: []CountingRule{{ID: 1, Name: "r", Enabled: true, Provider: "*",
				ModelPattern: "*", Endpoint: EndpointOpenAI, CountModel: "gpt-5"}},
		},
	}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.OpenAI, "gpt-5.1")))
	require.Equal(t, "110", headersOf(t, ctx)["estimated_tokens"])
}

// A custom token header replaces the default but keeps the alias.
func TestTokenCounting_CustomTokenHeader(t *testing.T) {
	srv, _ := countServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	})
	p, err := Init(&Config{
		Enabled: true, EstimateTokens: true, TokenHeader: "My-Tokens",
		TokenCounting: &TokenCountingConfig{
			APIKeys:  map[Endpoint]*schemas.SecretVar{EndpointOpenAI: schemas.NewSecretVar("sk-test")},
			BaseURLs: map[Endpoint]string{EndpointOpenAI: srv.URL},
			Rules: []CountingRule{{ID: 1, Name: "r", Enabled: true, Provider: "*",
				ModelPattern: "*", Endpoint: EndpointOpenAI, CountModel: "gpt-5"}},
		},
	}, nil)
	require.NoError(t, err)
	ctx := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctx, openAIChatReq(schemas.OpenAI, "gpt-5.1")))
	h := headersOf(t, ctx)
	require.Equal(t, "7", h["my-tokens"], "header key is lowercased for CEL matching")
	require.Equal(t, "7", h[TokenEstimateHeaderAlias])
}

// A chat request is converted to Responses shape before counting, so both
// request types count identically.
func TestTokenCounting_ChatAndRequestsCountIdentically(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	rec := &countRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.body, rec.requests = body, rec.requests+1
		rec.mu.Unlock()
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"input_tokens":31}`))
	}))
	t.Cleanup(srv.Close)
	p := countingPlugin(t, srv.URL, CountingRule{
		ID: 1, Name: "r", Enabled: true, Provider: "*", ModelPattern: "*",
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})

	userRole := schemas.ResponsesInputMessageRoleUser
	ctxChat := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctxChat, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-5.1",
			Input: []schemas.ChatMessage{userMsg("hello world")}},
	}))
	ctxResp := ctxWithHeaders(t, nil)
	require.NoError(t, p.PreRequestHook(ctxResp, &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-5.1",
			Input: []schemas.ResponsesMessage{{Role: &userRole,
				Content: &schemas.ResponsesMessageContent{ContentStr: strPtr("hello world")}}}},
	}))

	require.Equal(t, "31", headersOf(t, ctxChat)["estimated_tokens"])
	require.Equal(t, "31", headersOf(t, ctxResp)["estimated_tokens"])
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2)
	// The bodies differ only in the item discriminator ChatRequest.ToResponsesRequest
	// adds; what the endpoint counts is the text, which must be identical.
	require.Equal(t, countedText(t, bodies[0]), countedText(t, bodies[1]),
		"a chat request must count as the same input as the equivalent responses request")
}

// countedText returns the input text of a counting body, ignoring shape details
// that do not affect the count.
func countedText(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Input []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	var parts []string
	for _, in := range parsed.Input {
		parts = append(parts, in.Role+":"+in.Content)
	}
	return strings.Join(parts, "|")
}

func TestCleanupClosesCountingClient(t *testing.T) {
	p := countingPlugin(t, "http://127.0.0.1:1", CountingRule{
		ID: 1, Name: "r", Enabled: true, Provider: "*", ModelPattern: "*",
		Endpoint: EndpointOpenAI, CountModel: "gpt-5",
	})
	require.NotNil(t, p.countClient())
	require.NoError(t, p.Cleanup())
	require.Nil(t, p.client.Load(), "cleanup must release the pooled client")
}
