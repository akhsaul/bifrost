package extradetection

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/core/schemas"
)

// endpointSpec is everything hardcoded about one counting API. There is no
// user-extensible registry: a new format is a new spec plus a new builder.
type endpointSpec struct {
	// defaultBaseURL is used unless the config overrides it for this endpoint.
	defaultBaseURL string
	// buildURL assembles the request URL for a count model.
	buildURL func(baseURL, countModel string) string
	// buildBody renders the request body for a Responses-shaped request.
	buildBody func(req *schemas.BifrostResponsesRequest, countModel string) ([]byte, error)
	// applyAuth sets the endpoint's authentication headers/query.
	applyAuth func(h *fasthttp.RequestHeader, key string)
	// tokenField is the dotted JSON path to the count in the response body.
	tokenField string
}

// endpointSpecs holds the two implemented counting APIs.
//
// The Anthropic entry is deliberately absent; see the Endpoint doc comment for
// its verified shape and why it is deferred.
var endpointSpecs = map[Endpoint]endpointSpec{
	EndpointOpenAI: {
		// Mirrors core/providers/openai/openai.go:5021, which builds the same
		// path against the provider's configured base URL.
		defaultBaseURL: "https://api.openai.com",
		buildURL: func(baseURL, _ string) string {
			return strings.TrimSuffix(baseURL, "/") + "/v1/responses/input_tokens"
		},
		// ToOpenAIResponsesRequest sends the full Responses body, but a count
		// only reads model/input/instructions, so the sampling and tool fields
		// are left out rather than sent and ignored.
		buildBody:  buildOpenAICountBody,
		applyAuth:  applyBearerAuth,
		tokenField: "input_tokens",
	},
	EndpointGemini: {
		// Mirrors core/providers/gemini/gemini.go:85.
		defaultBaseURL: "https://generativelanguage.googleapis.com/v1beta",
		// The model is a path segment here, not a body field, and it must be
		// fully qualified (core/providers/gemini/count_tokens.go:41-46).
		buildURL: func(baseURL, countModel string) string {
			return strings.TrimSuffix(baseURL, "/") + "/models/" + normalizeGeminiModel(countModel) + ":countTokens"
		},
		buildBody:  buildGeminiCountBody,
		applyAuth:  applyGeminiKey,
		tokenField: "totalTokens",
	},
}

func applyBearerAuth(h *fasthttp.RequestHeader, key string) {
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
}

func applyGeminiKey(h *fasthttp.RequestHeader, key string) {
	if key != "" {
		h.Set("x-goog-api-key", key)
	}
}

// normalizeGeminiModel trims a "models/" prefix and any provider prefix so the
// path segment is a bare model name, matching core/providers/gemini's
// NormalizeModelName.
func normalizeGeminiModel(model string) string {
	_, bare := schemas.ParseModelString(model, "")
	bare = strings.TrimPrefix(bare, "models/")
	return bare
}

// openaiCountBody is the subset of the Responses body a count reads.
type openaiCountBody struct {
	Model        string                     `json:"model"`
	Input        []schemas.ResponsesMessage `json:"input"`
	Instructions string                     `json:"instructions,omitempty"`
}

// buildOpenAICountBody renders the OpenAI input_tokens body. Chat requests are
// converted to Responses shape first so both request types share one builder.
func buildOpenAICountBody(req *schemas.BifrostResponsesRequest, countModel string) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("nil responses request")
	}
	body := openaiCountBody{Model: countModel, Input: req.Input}
	if req.Params != nil && req.Params.Instructions != nil {
		body.Instructions = *req.Params.Instructions
	}
	return sonic.Marshal(body)
}

// geminiCountBody is the countTokens envelope. The endpoint only reads
// systemInstruction, tools and generationConfig from inside
// generateContentRequest and silently ignores a top-level contents once that
// envelope is present, so the body carries the envelope and nothing beside it
// (see core/providers/gemini/count_tokens.go:12-14).
type geminiCountBody struct {
	Contents               []geminiContent `json:"contents"`
	GenerateContentRequest geminiInner     `json:"generateContentRequest"`
}

type geminiInner struct {
	Model             string         `json:"model"`
	SystemInstruction *geminiContent `json:"systemInstruction,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

// buildGeminiCountBody renders the Gemini countTokens body, text content only.
func buildGeminiCountBody(req *schemas.BifrostResponsesRequest, countModel string) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("nil responses request")
	}
	inner := geminiInner{Model: "models/" + normalizeGeminiModel(countModel)}
	body := geminiCountBody{GenerateContentRequest: inner}
	for _, msg := range req.Input {
		if msg.Role == nil {
			continue
		}
		text := responsesMessageText(msg.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		// Gemini only models "user" and "model" in contents; system and
		// developer turns belong in systemInstruction.
		switch *msg.Role {
		case schemas.ResponsesInputMessageRoleSystem, schemas.ResponsesInputMessageRoleDeveloper:
			if inner.SystemInstruction == nil {
				inner.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: text}}}
			} else {
				inner.SystemInstruction.Parts = append(inner.SystemInstruction.Parts, geminiPart{Text: text})
			}
		case schemas.ResponsesInputMessageRoleUser:
			body.Contents = append(body.Contents, geminiContent{Role: "user", Parts: []geminiPart{{Text: text}}})
		case schemas.ResponsesInputMessageRoleAssistant:
			body.Contents = append(body.Contents, geminiContent{Role: "model", Parts: []geminiPart{{Text: text}}})
		}
	}
	body.GenerateContentRequest = inner
	return sonic.Marshal(body)
}

// countInputTokens calls the rule's endpoint and returns the raw count. It
// never returns an error to the caller as a reason to fail the request: every
// failure is logged and reported as ok=false, and the caller withholds the
// header and moves on.
func (p *Plugin) countInputTokens(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, rule *CountingRule) (int, bool) {
	cfg := p.config.TokenCounting
	spec, ok := endpointSpecs[rule.Endpoint]
	if !ok {
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf(
			"token counting: rule %q targets unknown endpoint %q, no token estimate set", rule.Name, string(rule.Endpoint)))
		return 0, false
	}

	key := ""
	if v, present := cfg.APIKeys[rule.Endpoint]; present && v != nil {
		key = v.GetValue()
	}
	if strings.TrimSpace(key) == "" {
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf(
			"token counting: no api key configured for endpoint %q, no token estimate set", string(rule.Endpoint)))
		return 0, false
	}

	// Both implemented endpoints read a Responses-shaped request, so a chat
	// request is converted once here rather than in each builder.
	responsesReq := responsesShapeOf(req)
	if responsesReq == nil {
		ctx.Log(schemas.LogLevelDebug, "token counting: request has no countable input, no token estimate set")
		return 0, false
	}

	body, err := spec.buildBody(responsesReq, rule.CountModel)
	if err != nil {
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf(
			"token counting: could not build %s request body: %v, no token estimate set", rule.Endpoint, err))
		return 0, false
	}

	baseURL := spec.defaultBaseURL
	if override := strings.TrimSpace(cfg.BaseURLs[rule.Endpoint]); override != "" {
		baseURL = override
	}
	url := spec.buildURL(baseURL, rule.CountModel)

	freq := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(freq)
	freq.SetRequestURI(url)
	freq.Header.SetMethod(http.MethodPost)
	freq.Header.SetContentType("application/json")
	spec.applyAuth(&freq.Header, key)
	freq.SetBody(body)

	fresp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(fresp)

	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if err := doWithContext(ctx, p.countClient(), freq, fresp, timeout); err != nil {
		if isTimeout(err) {
			ctx.Log(schemas.LogLevelWarn, fmt.Sprintf(
				"token counting: %s timed out after %dms, no token estimate set", rule.Endpoint, cfg.TimeoutMS))
		} else {
			ctx.Log(schemas.LogLevelWarn, fmt.Sprintf(
				"token counting: %s request failed: %v, no token estimate set", rule.Endpoint, err))
		}
		return 0, false
	}

	if status := fresp.StatusCode(); status < 200 || status > 299 {
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf(
			"token counting: %s returned HTTP %d for count_model %q, no token estimate set",
			rule.Endpoint, status, rule.CountModel))
		return 0, false
	}

	count := int(gjson.GetBytes(fresp.Body(), spec.tokenField).Int())
	if count <= 0 {
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf(
			"token counting: could not read %q from %s response, no token estimate set", spec.tokenField, rule.Endpoint))
		return 0, false
	}

	ctx.Log(schemas.LogLevelDebug, fmt.Sprintf(
		"token counting: %s counted %d input tokens for model %q", rule.Endpoint, count, rule.CountModel))
	return count, true
}

// responsesShapeOf returns the request in Responses shape, converting a chat
// request if needed. Returns nil when there is nothing countable.
func responsesShapeOf(req *schemas.BifrostRequest) *schemas.BifrostResponsesRequest {
	if req == nil {
		return nil
	}
	switch {
	case req.ResponsesRequest != nil:
		return req.ResponsesRequest
	case req.ChatRequest != nil:
		return req.ChatRequest.ToResponsesRequest()
	default:
		return nil
	}
}

// estimateFromCount applies the configured padding to an exact count.
func (t *TokenCountingConfig) estimateFromCount(count int) int {
	if t == nil || t.PaddingRatio <= 0 {
		return count
	}
	return count + int(float64(count)*t.PaddingRatio+0.999999) // ceil
}
