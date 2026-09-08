package guardrails

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// sampleGitHubPAT is a realistic random-entropy token matching the github-pat rule.
const sampleGitHubPAT = "ghp_Rs75tB9xWQ8vN3mK2pL7dXcV1yHjF4gT6zAq"

func TestBetterleaks_DetectGitHubToken(t *testing.T) {
	bl, err := newBetterleaksDetector(SecretsConfig{Action: PatternActionBlock})
	if err != nil {
		t.Fatalf("newBetterleaksDetector: %v", err)
	}

	text := "my token is " + sampleGitHubPAT + " please keep it safe"
	findings := bl.detect(text)
	if len(findings) == 0 {
		t.Fatal("expected at least one finding for GitHub PAT")
	}

	blocked, _, detected := bl.evaluate(nil, text, schemas.RedactionPhaseInput)
	if len(blocked) == 0 {
		t.Fatal("expected finding to be blocked")
	}
	if len(detected) == 0 {
		t.Fatal("expected detected label to be present")
	}
}

func TestBetterleaks_IgnoredKeywords(t *testing.T) {
	bl, err := newBetterleaksDetector(SecretsConfig{
		Action:                PatternActionBlock,
		IgnoredSecretKeywords: []string{sampleGitHubPAT},
	})
	if err != nil {
		t.Fatalf("newBetterleaksDetector: %v", err)
	}

	text := sampleGitHubPAT
	findings := bl.detect(text)
	if len(findings) != 0 {
		t.Fatalf("finding should be suppressed by ignored keyword, got: %v", findings)
	}
}

func TestBetterleaks_RedactReplace(t *testing.T) {
	bl, err := newBetterleaksDetector(SecretsConfig{
		Action:            PatternActionRedact,
		RedactionStrategy: RedactionReplace,
	})
	if err != nil {
		t.Fatalf("newBetterleaksDetector: %v", err)
	}

	text := "deploy with " + sampleGitHubPAT + " now"
	blocked, newText, detected := bl.evaluate(nil, text, schemas.RedactionPhaseInput)
	if len(blocked) != 0 {
		t.Fatal("redact action must not block")
	}
	if len(detected) == 0 {
		t.Fatal("expected detection label")
	}
	if strings.Contains(newText, sampleGitHubPAT) {
		t.Fatalf("secret was not redacted: %q", newText)
	}
	if !strings.Contains(newText, "[SECRET-1]") {
		t.Fatalf("expected [SECRET-1] placeholder, got %q", newText)
	}
}

func TestBetterleaks_DuplicateOccurrences(t *testing.T) {
	bl, err := newBetterleaksDetector(SecretsConfig{
		Action:            PatternActionRedact,
		RedactionStrategy: RedactionReplace,
	})
	if err != nil {
		t.Fatalf("newBetterleaksDetector: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	text := "first: " + sampleGitHubPAT + " second: " + sampleGitHubPAT
	_, newText, _ := bl.evaluate(ctx, text, schemas.RedactionPhaseInput)
	if strings.Contains(newText, sampleGitHubPAT) {
		t.Fatalf("one of the occurrences was not redacted: %q", newText)
	}
	expected := "first: [SECRET-1] second: [SECRET-1]"
	if newText != expected {
		t.Fatalf("expected %q, got %q", expected, newText)
	}
}

func TestBetterleaks_RedactMask(t *testing.T) {
	bl, err := newBetterleaksDetector(SecretsConfig{
		Action:            PatternActionRedact,
		RedactionStrategy: RedactionMask,
	})
	if err != nil {
		t.Fatalf("newBetterleaksDetector: %v", err)
	}

	text := "deploy with " + sampleGitHubPAT + " now"
	_, newText, _ := bl.evaluate(nil, text, schemas.RedactionPhaseInput)
	if strings.Contains(newText, sampleGitHubPAT) {
		t.Fatalf("secret was not masked: %q", newText)
	}
	if !strings.Contains(newText, "*****") {
		t.Fatalf("expected asterisks mask, got %q", newText)
	}
}
