package guardrails

import (
	"regexp"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, p *Pattern) compiledPattern {
	t.Helper()
	re, err := compilePattern(p)
	if err != nil {
		t.Fatalf("compilePattern(%q): %v", p.Pattern, err)
	}
	return compiledPattern{re: re, cfg: *p}
}

func TestEvaluatePatterns_DetectOnly(t *testing.T) {
	cp := mustCompile(t, &Pattern{
		Pattern:     `\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`,
		Description: "Email address",
		EntityType:  "EMAIL",
		Flags:       "i",
		Action:      PatternActionDetectOnly,
	})
	text := "contact me at John@Example.COM thanks"
	blocked, newText, detected := evaluatePatterns([]compiledPattern{cp}, text)
	if len(blocked) != 0 {
		t.Fatalf("detect_only should not block, got %d", len(blocked))
	}
	if newText != text {
		t.Fatalf("detect_only must not mutate text, got %q", newText)
	}
	if len(detected) != 1 || !strings.Contains(detected[0], "Email address") {
		t.Fatalf("expected detection label, got %v", detected)
	}
}

func TestEvaluatePatterns_BlockActionFlagsNotRewrites(t *testing.T) {
	cp := mustCompile(t, &Pattern{Pattern: `secret`, Action: PatternActionBlock})
	blocked, _, _ := evaluatePatterns([]compiledPattern{cp}, "my secret token")
	if len(blocked) != 1 {
		t.Fatal("block action should report the matched pattern")
	}
}

func TestEvaluatePatterns_RedactReplaceUsesEntityType(t *testing.T) {
	cp := mustCompile(t, &Pattern{
		Pattern:     `\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`,
		Description: "Email address",
		EntityType:  "EMAIL",
		Flags:       "i",
		Action:      PatternActionRedact,
	})
	_, newText, _ := evaluatePatterns([]compiledPattern{cp}, "mail me at a@b.com ok")
	want := "mail me at [EMAIL] ok"
	if newText != want {
		t.Fatalf("replace redaction: got %q want %q", newText, want)
	}
}

func TestEvaluatePatterns_RedactReplaceFallsBackToDescription(t *testing.T) {
	cp := mustCompile(t, &Pattern{
		Pattern:     `\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`,
		Description: "Email address",
		Flags:       "i",
		Action:      PatternActionRedact,
	})
	_, newText, _ := evaluatePatterns([]compiledPattern{cp}, "a@b.com")
	if !strings.Contains(newText, "[Email address]") {
		t.Fatalf("fallback to description expected, got %q", newText)
	}
}

func TestEvaluatePatterns_RedactReplaceFallsBackToGenericToken(t *testing.T) {
	cp := mustCompile(t, &Pattern{Pattern: `a@b.com`, Action: PatternActionRedact})
	_, newText, _ := evaluatePatterns([]compiledPattern{cp}, "a@b.com")
	if !strings.Contains(newText, "[REGEX_MATCH]") {
		t.Fatalf("generic REGEX_MATCH token expected, got %q", newText)
	}
}

func TestEvaluatePatterns_RedactMask(t *testing.T) {
	cp := mustCompile(t, &Pattern{
		Pattern:           `\b\d{4}\b`,
		Action:            PatternActionRedact,
		RedactionStrategy: RedactionMask,
	})
	_, newText, _ := evaluatePatterns([]compiledPattern{cp}, "pin 1234 here")
	if newText != "pin **** here" {
		t.Fatalf("mask redaction: got %q", newText)
	}
}

func TestEvaluatePatterns_RedactHash(t *testing.T) {
	cp := mustCompile(t, &Pattern{
		Pattern:           `\b\d{4}\b`,
		Action:            PatternActionRedact,
		RedactionStrategy: RedactionHash,
	})
	_, newText, _ := evaluatePatterns([]compiledPattern{cp}, "pin 1234 here")
	if !regexp.MustCompile(`pin \[[A-Za-z0-9_]+:[0-9a-f]{12}\] here`).MatchString(newText) {
		t.Fatalf("hash redaction: got %q", newText)
	}
}

func TestEvaluatePatterns_MultiplePatternsAndMatches(t *testing.T) {
	email := mustCompile(t, &Pattern{
		Pattern:     `\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`,
		EntityType:  "EMAIL",
		Flags:       "i",
		Action:      PatternActionRedact,
	})
	phone := mustCompile(t, &Pattern{
		Pattern:     `\b\d{3}-\d{4}\b`,
		EntityType:  "PHONE",
		Action:      PatternActionRedact,
	})
	_, newText, detected := evaluatePatterns([]compiledPattern{email, phone}, "a@b.com or 555-1234")
	if newText != "[EMAIL] or [PHONE]" {
		t.Fatalf("got %q", newText)
	}
	if len(detected) != 2 {
		t.Fatalf("expected 2 detections, got %v", detected)
	}
}

func TestEvaluatePatterns_RedactAppliesAcrossWholeText(t *testing.T) {
	// Multiple occurrences of the same pattern are all rewritten.
	cp := mustCompile(t, &Pattern{
		Pattern:    `foo`,
		EntityType: "X",
		Action:     PatternActionRedact,
	})
	_, newText, _ := evaluatePatterns([]compiledPattern{cp}, "foo bar foo")
	if newText != "[X] bar [X]" {
		t.Fatalf("got %q", newText)
	}
}

func TestEvaluatePatterns_InvalidRedactionStrategyDefaultsToReplace(t *testing.T) {
	// compiledConfig guarantees valid strategies; the engine treats any
	// unknown strategy as replace (defense in depth).
	cp := mustCompile(t, &Pattern{
		Pattern:           `abc`,
		EntityType:        "X",
		Action:            PatternActionRedact,
		RedactionStrategy: RedactionStrategy("bogus"),
	})
	_, newText, _ := evaluatePatterns([]compiledPattern{cp}, "x abc y")
	if newText != "x [X] y" {
		t.Fatalf("got %q", newText)
	}
}
