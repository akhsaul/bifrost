package zed

import (
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/maximhq/bifrost/core/schemas"
)

// BuildEnvelopeForTest exposes buildChatEnvelope for golden tests.
func (provider *ZedProvider) BuildEnvelopeForTest(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, innerProvider string) *ZedCompletionsEnvelope {
	envelope, bErr := provider.buildChatEnvelope(ctx, schemas.Key{}, request, innerProvider)
	if bErr != nil {
		return nil
	}
	return envelope
}

// VerifyLoginForTest exposes verifyLoginCredentials against a test server:
// the caller passes the server's base URL as baseURL.
func VerifyLoginForTest(baseURL, systemID, userID, accessToken string) (string, string, *schemas.BifrostError) {
	return verifyLoginCredentials(nil, baseURL, systemID, userID, accessToken)
}

// NewUsersMeStub spins a stub /client/users/me that requires the exact
// user-auth header and returns body with status. It returns the server URL.
func NewUsersMeStub(t interface {
	Fatalf(string, ...interface{})
	Cleanup(func())
	Helper()
}, wantUserAuth, body string, status int,
) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client/users/me" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("authorization"); got != wantUserAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("x-zed-system-id"); got == "" {
			http.Error(w, "missing system id", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	if _, err := url.Parse(srv.URL); err != nil {
		t.Fatalf("stub url does not parse: %v", err)
	}
	return srv.URL
}

// BuildResponsesEnvelopeForTest exposes the Responses envelope path for
// golden tests: convert + wrap with prompt_cache_key=thread_id.
func (provider *ZedProvider) BuildResponsesEnvelopeForTest(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) *ZedCompletionsEnvelope {
	threadID := resolveThreadID(ctx, provider.networkConfig.ExtraHeaders)
	inner, bErr := toInnerOpenAIRequest(ctx, request, threadID)
	if bErr != nil {
		return nil
	}
	envelope := buildCompletionsEnvelope(ctx, provider.networkConfig.ExtraHeaders, ZedInnerOpenAI, request.Model, inner)
	inner.PromptCacheKey = &envelope.ThreadID
	return envelope
}
