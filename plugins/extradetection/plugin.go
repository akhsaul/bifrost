// Package extradetection implements the Bifrost LLM plugin that adds extra
// request detection before routing runs.
//
// The plugin runs in PreRequestHook (before the routing plugin) and performs
// two kinds of lightweight detection:
//
//  1. Token counting: resolves the request's (provider, model), finds the
//     first matching rule in the user's counting config, and asks that rule's
//     endpoint (OpenAI input_tokens or Gemini countTokens) for an exact input
//     token count. The count lands in the request headers map consumed by
//     routing CEL rules. Counting is opt-in per model: a request whose
//     (provider, model) matches no rule, or whose count call fails, simply gets
//     no estimate header — it is never failed and never blocked.
//  2. User-configured detection rules: each rule scans a window of the most
//     recent user-role messages (most recent first, assistant/tool/system
//     messages are skipped and never counted) and, on match, writes a
//     configured header key/value pair.
//
// Routing rules target the stamped headers, e.g.
// headers["complexity_tier"] == "simple" or
// int(headers["estimated_tokens"]) > 4000. Because header-based CEL rules do
// not reference the bare complexity_tier identifier, the routing engine never
// triggers the complexity-router embedding classifier for them.
//
// The counting call is a direct outbound HTTP request to the configured
// endpoint, not a Bifrost inference request: it does not re-enter the plugin
// pipeline, so it cannot recurse, and it produces no governance or billing
// side effects. A request that matches no counting rule, or whose count call
// fails, is logged in the request's Plugin logs and continues to routing
// exactly as it would have without this plugin.
package extradetection

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// PluginName is the canonical name registered for the extra-detection plugin.
	PluginName = "extra-detection"

	// DefaultTokenEstimateHeader is the header key carrying the padded token estimate.
	DefaultTokenEstimateHeader = "estimated_tokens"
	// TokenEstimateHeaderAlias is a second key carrying the same estimate for
	// callers that prefer the x- prefixed spelling.
	TokenEstimateHeaderAlias = "x-estimated-tokens"

	// DefaultMatchScope scans the window of most recent user messages.
	DefaultMatchScope = "last_user_messages"

	// MaxMessagesToScanCap bounds per-request string work: a window larger than
	// this is a full-history scan and should use scope "full_input" instead.
	MaxMessagesToScanCap = 20
)

// Plugin implements schemas.LLMPlugin for extra detection.
type Plugin struct {
	config *Config
	logger schemas.Logger

	// clientOnce guards lazy creation of the counting HTTP client, which is
	// shared by every request so the counting endpoint's connection stays pooled.
	clientOnce sync.Once
	client     atomic.Pointer[fasthttp.Client]
}

// Init constructs the plugin from the stored plugin config. A nil config uses defaults.
func Init(cfg *Config, logger schemas.Logger) (*Plugin, error) {
	resolved := cfg
	if resolved == nil {
		resolved = DefaultConfig()
	} else {
		// Copy so later mutations of the caller's config cannot race requests.
		cp := *resolved
		cp.Rules = append([]Rule(nil), resolved.Rules...)
		resolved = &cp
	}
	if err := resolved.normalize(logger); err != nil {
		return nil, fmt.Errorf("invalid extra-detection config: %w", err)
	}
	return &Plugin{config: resolved, logger: logger}, nil
}

// GetName returns the plugin identifier ("extra-detection").
func (p *Plugin) GetName() string {
	return PluginName
}

// Cleanup releases the plugin's pooled counting connections.
func (p *Plugin) Cleanup() error {
	p.closeCountClient()
	return nil
}

// PreRequestHook implements schemas.LLMPlugin (no-op — required for plugin indexing).
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if ctx == nil || req == nil {
		return nil
	}
	if p == nil || p.config == nil || !p.config.Enabled {
		return nil
	}

	tokenHeader := strings.ToLower(strings.TrimSpace(p.config.TokenHeader))
	if tokenHeader == "" {
		tokenHeader = DefaultTokenEstimateHeader
	}

	headers := requestHeadersCopy(ctx)

	// 1. Token counting. The header is stamped only when a counting rule
	// matches this request's (provider, model) and the call succeeds. Every
	// other outcome — no rule, no key, timeout, error status, unparseable
	// response — withholds the header and leaves the request untouched on its
	// way to routing. A counting rule can never fail a request.
	if p.config.EstimateTokens && p.config.TokenCounting != nil {
		p.stampTokenEstimate(ctx, req, headers, tokenHeader)
	}

	// 2. Evaluate enabled detection rules in order; later matches overwrite
	// earlier ones for the same header (last-write-wins, so UI order matters).
	userTexts := collectUserTexts(req)
	fullText := collectFullInputText(req)
	for i := range p.config.Rules {
		rule := &p.config.Rules[i]
		if !rule.Enabled {
			continue
		}
		if ruleMatches(rule, userTexts, fullText) {
			headers[rule.Header] = rule.Value
		}
	}

	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	return nil
}

// PreLLMHook implements schemas.LLMPlugin (no-op — detection runs once per request).
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}

// PostLLMHook implements schemas.LLMPlugin (no-op — the plugin never touches responses).
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// stampTokenEstimate resolves the request to a (provider, model) target,
// finds the first matching counting rule, and on a successful count stamps
// the estimate header. It is a no-op on every failure path.
func (p *Plugin) stampTokenEstimate(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, headers map[string]string, tokenHeader string) {
	cfg := p.config.TokenCounting
	target := ResolveTarget(req)
	if target.Model == "" {
		return
	}
	rule := cfg.MatchCountingRule(target)
	if rule == nil {
		// Debug, not warn: an unlisted model is the overwhelmingly common
		// case, so warning here would bury the real failures. Mirrors the
		// "nothing to do" level used by the model catalog in plugins/compat.
		ctx.Log(schemas.LogLevelDebug, fmt.Sprintf(
			"token counting: no rule matched provider %q model %q, no token estimate set",
			target.Provider, target.Model))
		return
	}
	count, ok := p.countInputTokens(ctx, req, rule)
	if !ok {
		return
	}
	estimate := strconv.Itoa(cfg.estimateFromCount(count))
	headers[tokenHeader] = estimate
	if tokenHeader != TokenEstimateHeaderAlias {
		headers[TokenEstimateHeaderAlias] = estimate
	}
}

// requestHeadersCopy returns a defensive copy of the request headers map from
// the context. The map is copied (never mutated in place) because other
// plugins and the transport may hold references to the original; keys are
// lowercased to match the routing engine's case-insensitive CEL matching.
func requestHeadersCopy(ctx *schemas.BifrostContext) map[string]string {
	out := make(map[string]string, 8)
	if existing, ok := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string); ok {
		for k, v := range existing {
			out[strings.ToLower(k)] = v
		}
	}
	return out
}

// ruleMatches reports whether a rule matches given the collected texts.
func ruleMatches(rule *Rule, userTexts []string, fullText string) bool {
	if rule == nil || !rule.Enabled {
		return false
	}
	match := &rule.Match
	switch strings.ToLower(strings.TrimSpace(match.Scope)) {
	case "", DefaultMatchScope, "last_user_message":
		window := rule.MaxMessagesToScan
		if window <= 0 {
			window = 1
		}
		if window > len(userTexts) {
			window = len(userTexts)
		}
		// Any-of-N semantics: the rule matches if any one of the last N user
		// messages satisfies the match block. Requiring all N to match would
		// make N > 1 nearly useless (e.g. a git-commit intent from 2 turns ago
		// is still the active task).
		for _, text := range userTexts[:window] {
			if textMatches(match, text) {
				return true
			}
		}
		return false
	case "full_input":
		return textMatches(match, fullText)
	default:
		return false
	}
}

// textMatches reports whether normalized text satisfies all configured match
// operators (all_of AND any_of AND starts_with must all pass).
func textMatches(match *Match, text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	for _, kw := range match.AllOf {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		if !strings.Contains(normalized, kw) {
			return false
		}
	}
	if len(match.AnyOf) > 0 {
		found := false
		for _, kw := range match.AnyOf {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw == "" {
				continue
			}
			if strings.Contains(normalized, kw) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(match.StartsWith) > 0 {
		found := false
		for _, prefix := range match.StartsWith {
			prefix = strings.ToLower(strings.TrimSpace(prefix))
			if prefix == "" {
				continue
			}
			if strings.HasPrefix(normalized, prefix) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
