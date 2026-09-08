package guardrails

import (
	"regexp"

	"github.com/maximhq/bifrost/core/schemas"
)

// compiledPattern is a validated pattern with its pre-compiled regex.
type compiledPattern struct {
	re  *regexp.Regexp
	cfg Pattern
}

// redactionToken is the placeholder written for a redacted match:
// entity_type, else description, else the generic REGEX_MATCH.
func (cp *compiledPattern) redactionToken() string {
	if cp.cfg.EntityType != "" {
		return cp.cfg.EntityType
	}
	if cp.cfg.Description != "" {
		return cp.cfg.Description
	}
	return "REGEX_MATCH"
}

// label is the human-readable identity used in detection logs / block reasons.
func (cp *compiledPattern) label() string {
	if cp.cfg.Description != "" {
		return cp.cfg.Description
	}
	if cp.cfg.EntityType != "" {
		return cp.cfg.EntityType
	}
	return cp.cfg.Pattern
}

// rewriteMatch produces the replacement text for one matched substring
// according to the pattern's action, redaction strategy, and mode via the request tracker.
func (cp *compiledPattern) rewriteMatch(match string, ctx *schemas.BifrostContext, phase schemas.RedactionPhase) string {
	if cp.cfg.Action != PatternActionRedact {
		return match
	}
	tracker := GetOrCreateTracker(ctx)
	return tracker.Redact(ctx, cp.redactionToken(), match, cp.cfg.RedactionStrategy, cp.cfg.RedactionMode, phase)
}

// evaluatePatterns runs every compiled pattern over text.
//
// Returns:
//   - blocked: patterns with action "block" that matched (caller decides the error)
//   - newText: the text after applying all redact rewrites (identical to text
//     when no redact pattern matched)
//   - detected: one label per (pattern, occurrence) match, for detect_only
//     logging and intervention metadata
//
// All patterns are evaluated even when some block, so a single pass reports
// every finding.
func evaluatePatterns(ctx *schemas.BifrostContext, patterns []compiledPattern, text string, phase schemas.RedactionPhase) (blocked []compiledPattern, newText string, detected []string) {
	newText = text
	if text == "" {
		return nil, text, nil
	}
	// Replacements must not cascade: a replacement written by an earlier
	// pattern must not be re-scanned by a later one. Collect non-overlapping
	// edit ranges first, then apply right-to-left.
	type edit struct {
		start, end int
		repl       string
	}
	var edits []edit
	for i := range patterns {
		cp := &patterns[i]
		matches := cp.re.FindAllStringIndex(text, -1)
		if len(matches) == 0 {
			continue
		}
		switch cp.cfg.Action {
		case PatternActionBlock:
			blocked = append(blocked, *cp)
			for range matches {
				detected = append(detected, cp.label())
			}
		case PatternActionRedact:
			for _, m := range matches {
				match := text[m[0]:m[1]]
				repl := cp.rewriteMatch(match, ctx, phase)
				edits = append(edits, edit{start: m[0], end: m[1], repl: repl})
				detected = append(detected, cp.label())
			}
		default: // detect_only
			for range matches {
				detected = append(detected, cp.label())
			}
		}
	}
	if len(edits) > 0 {
		b := []byte(newText)
		// Sort by start descending and apply back-to-front so indices stay valid.
		for i := 0; i < len(edits); i++ {
			for j := i + 1; j < len(edits); j++ {
				if edits[j].start > edits[i].start {
					edits[i], edits[j] = edits[j], edits[i]
				}
			}
		}
		for _, e := range edits {
			if e.start > e.end || e.end > len(b) {
				continue
			}
			b = append(b[:e.start], append([]byte(e.repl), b[e.end:]...)...)
		}
		newText = string(b)
	}
	return blocked, newText, detected
}
