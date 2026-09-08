package guardrails

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
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
	blocked, newText, detected := evaluatePatterns(nil, []compiledPattern{cp}, text, schemas.RedactionPhaseInput)
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
	blocked, _, _ := evaluatePatterns(nil, []compiledPattern{cp}, "my secret token", schemas.RedactionPhaseInput)
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
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	_, newText, _ := evaluatePatterns(ctx, []compiledPattern{cp}, "mail me at a@b.com ok", schemas.RedactionPhaseInput)
	want := "mail me at [EMAIL-1] ok"
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
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	_, newText, _ := evaluatePatterns(ctx, []compiledPattern{cp}, "a@b.com", schemas.RedactionPhaseInput)
	if !strings.Contains(newText, "[Email address-1]") {
		t.Fatalf("fallback to description expected, got %q", newText)
	}
}

func TestEvaluatePatterns_RedactReplaceFallsBackToGenericToken(t *testing.T) {
	cp := mustCompile(t, &Pattern{Pattern: `a@b.com`, Action: PatternActionRedact})
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	_, newText, _ := evaluatePatterns(ctx, []compiledPattern{cp}, "a@b.com", schemas.RedactionPhaseInput)
	if !strings.Contains(newText, "[REGEX_MATCH-1]") {
		t.Fatalf("generic REGEX_MATCH token expected, got %q", newText)
	}
}

func TestEvaluatePatterns_RedactMask(t *testing.T) {
	cp := mustCompile(t, &Pattern{
		Pattern:           `\b\d{4}\b`,
		Action:            PatternActionRedact,
		RedactionStrategy: RedactionMask,
	})
	_, newText, _ := evaluatePatterns(nil, []compiledPattern{cp}, "pin 1234 here", schemas.RedactionPhaseInput)
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
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	_, newText, _ := evaluatePatterns(ctx, []compiledPattern{cp}, "pin 1234 here", schemas.RedactionPhaseInput)
	if !regexp.MustCompile(`pin \[[A-Za-z0-9_]+:[0-9a-f]{16}\] here`).MatchString(newText) {
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
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	_, newText, detected := evaluatePatterns(ctx, []compiledPattern{email, phone}, "a@b.com or 555-1234", schemas.RedactionPhaseInput)
	if newText != "[EMAIL-1] or [PHONE-1]" {
		t.Fatalf("got %q", newText)
	}
	if len(detected) != 2 {
		t.Fatalf("expected 2 detections, got %v", detected)
	}
}

func TestEvaluatePatterns_RedactAppliesAcrossWholeText(t *testing.T) {
	// Multiple occurrences of the same pattern are all rewritten to the same indexed placeholder.
	cp := mustCompile(t, &Pattern{
		Pattern:    `foo`,
		EntityType: "X",
		Action:     PatternActionRedact,
	})
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	_, newText, _ := evaluatePatterns(ctx, []compiledPattern{cp}, "foo bar foo", schemas.RedactionPhaseInput)
	if newText != "[X-1] bar [X-1]" {
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
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	_, newText, _ := evaluatePatterns(ctx, []compiledPattern{cp}, "x abc y", schemas.RedactionPhaseInput)
	if newText != "x [X-1] y" {
		t.Fatalf("got %q", newText)
	}
}
