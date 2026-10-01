package extradetection

import (
	"regexp"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// ProviderWildcard matches any provider in a counting rule.
const ProviderWildcard = "*"

// Target is the (provider, model) pair a counting rule is tested against.
type Target struct {
	// Provider is the request's resolved provider, or the model string's own
	// prefix when the request carries none (see ResolveTarget).
	Provider string
	// Model is the provider-prefix-stripped model, the value a rule's
	// ModelPattern is matched against.
	Model string
}

// ResolveTarget derives the (provider, model) a counting rule should match
// against from a request. req.Provider is authoritative when set. When it is
// empty — the common case for a request whose model carries the prefix, since
// routing has not run yet — the prefix is taken off the model string instead,
// so "openrouter/gemini-3.8-flash" still matches a provider: "openrouter" rule.
//
// Only a prefix that names a known Bifrost provider is split off, so model
// namespaces that merely contain a slash ("meta-llama/Llama-3.1-8B") survive
// whole and stay matchable as themselves.
func ResolveTarget(req *schemas.BifrostRequest) Target {
	if req == nil {
		return Target{}
	}
	provider, model, _ := req.GetRequestFields()
	if provider != "" {
		// GetRequestFields returns the model as parsed by the transport, which
		// has already stripped a known provider prefix. Strip once more in case
		// the caller handed us an unparsed string.
		if p, bare := schemas.ParseModelString(model, schemas.ModelProvider("")); p != "" {
			provider, model = p, bare
		}
		return Target{Provider: strings.ToLower(string(provider)), Model: model}
	}
	if p, bare := schemas.ParseModelString(model, schemas.ModelProvider("")); p != "" {
		return Target{Provider: string(p), Model: bare}
	}
	return Target{Model: model}
}

// MatchCountingRule returns the first enabled rule matching the target. Rules
// are evaluated in array order, so list order is priority.
func (t *TokenCountingConfig) MatchCountingRule(target Target) *CountingRule {
	if t == nil {
		return nil
	}
	for i := range t.Rules {
		rule := &t.Rules[i]
		if !rule.Enabled {
			continue
		}
		if rule.Provider != ProviderWildcard && rule.Provider != strings.ToLower(target.Provider) {
			continue
		}
		if !rule.matchesModel(target.Model) {
			continue
		}
		return rule
	}
	return nil
}

// matchesModel reports whether the rule's pattern hits the model name.
func (r *CountingRule) matchesModel(model string) bool {
	if model == "" {
		return false
	}
	switch r.ModelPatternType {
	case ModelPatternRegex:
		re, err := compilePattern(r.ModelPattern)
		if err != nil {
			return false
		}
		return re.MatchString(model)
	case ModelPatternExact:
		return strings.EqualFold(r.ModelPattern, model)
	default: // ModelPatternGlob
		return globMatch(r.ModelPattern, model)
	}
}

// patternCache memoizes compiled regexes. A rule set is fixed for the life of
// the plugin, so this is bounded by the number of rules and never grows per
// request.
var patternCache sync.Map // pattern string -> *regexp.Regexp or error

func compilePattern(pattern string) (*regexp.Regexp, error) {
	if v, ok := patternCache.Load(pattern); ok {
		if re, isRe := v.(*regexp.Regexp); isRe {
			return re, nil
		}
		return nil, v.(error)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		patternCache.Store(pattern, err)
		return nil, err
	}
	patternCache.Store(pattern, re)
	return re, nil
}

// globMatch reports whether name matches a glob pattern where "*" stands for
// any run of characters (including none) and "?" for a single character.
// Matching is case-insensitive. This is deliberately not path.Match: that
// treats "/" as a separator, which would make "gemini/*" fail to match the
// nested "cline/cline-free/gemini-3.8-flash" the pattern is meant to catch.
func globMatch(pattern, name string) bool {
	pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	var (
		p, n         int
		starP, starN = -1, -1
	)
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			// Record the backtrack point, then try to match the rest without it.
			starP, starN = p, n
			p++
		case starP >= 0:
			// Backtrack: let the last '*' absorb one more character.
			starN++
			n = starN
			p = starP + 1
		default:
			return false
		}
	}
	// Trailing '*'s in the pattern may be unconsumed by a fully-matched name.
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
