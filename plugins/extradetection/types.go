package extradetection

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Config is the stored plugin configuration for extra-detection. It is
// persisted as the plugin's config JSON (plugins table / config.json) and
// edited from the Extra Detection web UI.
type Config struct {
	// Enabled gates the whole plugin. When false, PreRequestHook stamps nothing.
	Enabled bool `json:"enabled"`
	// EstimateTokens gates the token-estimate header. When true, the request's
	// (provider, model) is matched against TokenCounting.Rules and, on a match,
	// counted against the rule's Endpoint and the result stamped on TokenHeader.
	// When no rule matches — or the count call fails — no header is stamped at
	// all and the request proceeds to routing untouched.
	EstimateTokens bool `json:"estimate_tokens"`
	// TokenHeader overrides the header key carrying the estimate. Empty means
	// DefaultTokenEstimateHeader ("estimated_tokens").
	TokenHeader string `json:"token_header,omitempty"`
	// TokenCounting holds the API-based token counting configuration. Nil is
	// valid and means "no counting configured": EstimateTokens then stamps
	// nothing for every request.
	TokenCounting *TokenCountingConfig `json:"token_counting,omitempty"`
	// Rules are evaluated in array order; later matches overwrite earlier ones
	// for the same header (last-write-wins).
	Rules []Rule `json:"rules,omitempty"`
}

// Endpoint identifies a hardcoded token-counting API. There is no user-extensible
// endpoint registry: adding a fourth format is a code change here, not config.
//
// Adding an endpoint means adding a builder in counting.go plus an arm to
// extractTokenCount. The three majors are the only ones with a public count API.
//
//	anthropic — NOT YET IMPLEMENTED, deliberately deferred.
//	  URL:   POST {base}/v1/messages/count_tokens
//	  Auth:  x-api-key + anthropic-version
//	  Body:  Messages-shaped, NOT Responses-shaped: {"model", "messages",
//	         "system", "tools"} with max_tokens and temperature excluded
//	         (core/providers/anthropic/anthropic.go:2965 for the URL, :2999 for
//	         the excluded fields). The response is {"input_tokens"}
//	         (core/providers/anthropic/types.go:2415).
//	  Why deferred: it is the one major whose body cannot be built from the
//	         Responses-shaped request the other endpoints read, so it needs its
//	         own builder over the original BifrostRequest instead of sharing
//	         the single conversion. Its count would be exact, so no accuracy is
//	         lost by waiting — rules can point at another endpoint meanwhile.
type Endpoint string

const (
	// EndpointOpenAI is OpenAI's POST /v1/responses/input_tokens.
	EndpointOpenAI Endpoint = "openai"
	// EndpointGemini is Google's POST /v1beta/models/{model}:countTokens.
	EndpointGemini Endpoint = "gemini"
)

func (e Endpoint) valid() bool {
	switch e {
	case EndpointOpenAI, EndpointGemini:
		return true
	default:
		return false
	}
}

// TokenCountingConfig configures API-based token counting. Counting is opt-in
// per request: only (provider, model) pairs matched by a rule are counted, and
// every failure path withholds the header rather than failing the request.
type TokenCountingConfig struct {
	// TimeoutMS bounds a single counting call. Zero means DefaultCountTimeoutMS.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// PaddingRatio pads the count upstream returns, so
	// estimate = count * (1 + padding_ratio). Zero (the default) is honest:
	// these endpoints count exactly. Raise it to stay conservative about
	// billing when the count is a proxy for a different model's tokenizer.
	PaddingRatio float64 `json:"padding_ratio,omitempty"`
	// APIKeys holds one credential per endpoint id, as a SecretVar so plain
	// text, "env.NAME" and "vault.path" references all work.
	APIKeys map[Endpoint]*schemas.SecretVar `json:"api_keys,omitempty"`
	// BaseURLs optionally overrides an endpoint's default base URL (proxies,
	// Azure-compatible gateways). Keyed by endpoint id.
	BaseURLs map[Endpoint]string `json:"base_urls,omitempty"`
	// Rules are evaluated in array order; the first enabled rule whose provider
	// and model pattern match wins.
	Rules []CountingRule `json:"rules,omitempty"`
}

// DefaultCountTimeoutMS bounds one counting call when TimeoutMS is unset. Kept
// short because the call blocks the request before routing.
const DefaultCountTimeoutMS = 2000

// CountingRule routes one (provider, model) shape to one counting endpoint. The
// rule — not the endpoint — decides which model name is sent upstream, so the
// same endpoint can count for many models and a provider-prefixed model like
// "openrouter/google/gemini-3-flash" can be counted as a bare "gemini-3-flash".
type CountingRule struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	// Provider matches the request's resolved provider, or "*" for any. When
	// the request carries no provider, the model string's own prefix is used
	// (see ResolveTarget in match.go).
	Provider string `json:"provider"`
	// ModelPattern is tested against the provider-prefix-stripped model.
	ModelPattern string `json:"model_pattern"`
	// ModelPatternType is "glob" (default), "regex", or "exact".
	ModelPatternType string `json:"model_pattern_type,omitempty"`
	// Endpoint is the counting API to call.
	Endpoint Endpoint `json:"endpoint"`
	// CountModel is the model name sent to the counting endpoint, verbatim.
	// It is a free string on purpose: aggregators rename and suffix models
	// ("gemini-3.8-flash-free", "gpt-5-preview"), and only the operator knows
	// which upstream model tokenizes acceptably for a given request shape.
	CountModel string `json:"count_model"`
}

// Model pattern matchers.
const (
	ModelPatternGlob  = "glob"
	ModelPatternRegex = "regex"
	ModelPatternExact = "exact"
)

// Rule is a single user-configured detection rule: when the match block hits,
// headers[Header] is set to Value for routing CEL rules to target.
type Rule struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	// Header is the header key written on match (stored lowercased).
	Header string `json:"header"`
	// Value is the header value written on match.
	Value string `json:"value"`
	// MaxMessagesToScan is the user-message window: 1 = last user message only
	// (default); N > 1 = last user message plus up to N-1 previous user
	// messages. Only user-role messages count — assistant turns, tool calls,
	// tool results and system prompts are skipped, never counted.
	MaxMessagesToScan int   `json:"max_messages_to_scan,omitempty"`
	Match             Match `json:"match"`
}

// Match is the predicate block of a rule. All configured operators must pass
// (all_of AND any_of AND starts_with).
type Match struct {
	// Scope selects the scanned text: "last_user_messages" (default, honors
	// MaxMessagesToScan) or "full_input" (whole request text).
	Scope string `json:"scope,omitempty"`
	// AllOf requires every keyword to appear (case-insensitive substring).
	AllOf []string `json:"all_of,omitempty"`
	// AnyOf requires at least one keyword to appear (case-insensitive substring).
	AnyOf []string `json:"any_of,omitempty"`
	// StartsWith requires the text to start with one of the prefixes
	// (case-insensitive, after trimming leading whitespace).
	StartsWith []string `json:"starts_with,omitempty"`
}

// DefaultConfig returns the out-of-box configuration with the two seed rules
// from the original spec: git+commit and question-prefix, both stamping
// headers["complexity_tier"] = "simple".
func DefaultConfig() *Config {
	return &Config{
		Enabled:        true,
		EstimateTokens: true,
		TokenHeader:    DefaultTokenEstimateHeader,
		Rules: []Rule{
			{
				ID:                1,
				Name:              "git commit → simple",
				Description:       "Commit-style requests skip embedding",
				Enabled:           true,
				Header:            "complexity_tier",
				Value:             "simple",
				MaxMessagesToScan: 1,
				Match: Match{
					Scope: DefaultMatchScope,
					AllOf: []string{"git", "commit"},
				},
			},
			{
				ID:                2,
				Name:              "question → simple",
				Description:       "Question-prefixed requests skip embedding",
				Enabled:           true,
				Header:            "complexity_tier",
				Value:             "simple",
				MaxMessagesToScan: 1,
				Match: Match{
					Scope:      DefaultMatchScope,
					StartsWith: []string{"question"},
				},
			},
		},
	}
}

// normalize validates the config in place and fills defaults. Invalid rules
// are dropped (fail-safe: detection must never block a request); an error is
// returned only when nothing usable remains of an explicitly provided rule.
func (c *Config) normalize(logger schemas.Logger) error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if c.TokenHeader == "" {
		c.TokenHeader = DefaultTokenEstimateHeader
	} else {
		c.TokenHeader = strings.ToLower(strings.TrimSpace(c.TokenHeader))
		if c.TokenHeader == "" {
			c.TokenHeader = DefaultTokenEstimateHeader
		}
	}
	kept := c.Rules[:0]
	for i := range c.Rules {
		if err := c.Rules[i].normalize(); err != nil {
			// Drop invalid rules instead of failing the whole plugin load.
			continue
		}
		kept = append(kept, c.Rules[i])
	}
	c.Rules = kept
	c.TokenCounting.normalize(logger)
	return nil
}

// normalize validates the counting block in place. A rule naming an endpoint
// that does not exist is dropped, never an error: the plugin must still load
// so every request reaches routing and its provider. The drop is logged to the
// server log because a dropped rule is otherwise invisible — no request ever
// carries the header, and there is no per-request event to hang a message on.
func (t *TokenCountingConfig) normalize(logger schemas.Logger) {
	if t == nil {
		return
	}
	if t.TimeoutMS <= 0 {
		t.TimeoutMS = DefaultCountTimeoutMS
	}
	if t.PaddingRatio < 0 {
		t.PaddingRatio = 0
	}
	kept := t.Rules[:0]
	for i := range t.Rules {
		rule := &t.Rules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Provider = strings.ToLower(strings.TrimSpace(rule.Provider))
		if rule.Provider == "" {
			rule.Provider = "*"
		}
		rule.ModelPattern = strings.TrimSpace(rule.ModelPattern)
		rule.CountModel = strings.TrimSpace(rule.CountModel)
		switch strings.ToLower(strings.TrimSpace(rule.ModelPatternType)) {
		case "", ModelPatternGlob:
			rule.ModelPatternType = ModelPatternGlob
		case ModelPatternRegex, ModelPatternExact:
			rule.ModelPatternType = strings.ToLower(strings.TrimSpace(rule.ModelPatternType))
		default:
			logCountWarn(logger, "extra-detection: dropping counting rule %q: unknown model_pattern_type %q", rule.Name, rule.ModelPatternType)
			continue
		}
		if rule.ModelPattern == "" || rule.CountModel == "" {
			logCountWarn(logger, "extra-detection: dropping counting rule %q: model_pattern and count_model are required", rule.Name)
			continue
		}
		if !rule.Endpoint.valid() {
			// Deliberately not an error. See the note above: an unusable rule
			// costs the operator a token estimate on matching requests, nothing
			// more, and must not take the plugin — or the request — down.
			logCountWarn(logger, "extra-detection: dropping counting rule %q: unknown endpoint %q", rule.Name, string(rule.Endpoint))
			continue
		}
		kept = append(kept, *rule)
	}
	t.Rules = kept
}

// logCountWarn logs a load-time config warning, tolerating a nil logger so
// tests and library callers can construct the plugin without one.
func logCountWarn(logger schemas.Logger, format string, args ...any) {
	if logger == nil {
		return
	}
	logger.Warn(format, args...)
}

func (r *Rule) normalize() error {
	r.Header = strings.ToLower(strings.TrimSpace(r.Header))
	if r.Header == "" {
		return fmt.Errorf("rule %q: header is required", r.Name)
	}
	for _, ch := range r.Header {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
			return fmt.Errorf("rule %q: invalid header %q", r.Name, r.Header)
		}
	}
	r.Value = strings.TrimSpace(r.Value)
	if r.Value == "" {
		return fmt.Errorf("rule %q: value is required", r.Name)
	}
	if len(r.Value) > 64 {
		return fmt.Errorf("rule %q: value exceeds 64 characters", r.Name)
	}
	if r.MaxMessagesToScan <= 0 {
		r.MaxMessagesToScan = 1
	}
	if r.MaxMessagesToScan > MaxMessagesToScanCap {
		return fmt.Errorf("rule %q: max_messages_to_scan exceeds cap %d (use scope full_input for whole-history scans)", r.Name, MaxMessagesToScanCap)
	}
	scope := strings.ToLower(strings.TrimSpace(r.Match.Scope))
	switch scope {
	case "", DefaultMatchScope:
		r.Match.Scope = DefaultMatchScope
	case "last_user_message":
		// Back-compat alias for configs written with the singular v1 name.
		r.Match.Scope = DefaultMatchScope
	case "full_input":
		r.Match.Scope = "full_input"
	default:
		return fmt.Errorf("rule %q: unknown scope %q", r.Name, r.Match.Scope)
	}
	r.Match.AllOf = normalizeKeywords(r.Match.AllOf)
	r.Match.AnyOf = normalizeKeywords(r.Match.AnyOf)
	r.Match.StartsWith = normalizeKeywords(r.Match.StartsWith)
	if len(r.Match.AllOf) == 0 && len(r.Match.AnyOf) == 0 && len(r.Match.StartsWith) == 0 {
		return fmt.Errorf("rule %q: at least one of all_of/any_of/starts_with is required", r.Name)
	}
	return nil
}

func normalizeKeywords(in []string) []string {
	out := make([]string, 0, len(in))
	for _, kw := range in {
		kw = strings.TrimSpace(kw)
		if kw == "" || len(kw) > 100 {
			continue
		}
		out = append(out, kw)
		if len(out) >= 20 {
			break
		}
	}
	return out
}
