package guardrails

import (
	"strings"
	"testing"
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

	blocked, _, detected := bl.evaluate(text)
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
	blocked, newText, detected := bl.evaluate(text)
	if len(blocked) != 0 {
		t.Fatal("redact action must not block")
	}
	if len(detected) == 0 {
		t.Fatal("expected detection label")
	}
	if strings.Contains(newText, sampleGitHubPAT) {
		t.Fatalf("secret was not redacted: %q", newText)
	}
	if !strings.Contains(newText, "[SECRET]") {
		t.Fatalf("expected [SECRET] placeholder, got %q", newText)
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
	_, newText, _ := bl.evaluate(text)
	if strings.Contains(newText, sampleGitHubPAT) {
		t.Fatalf("secret was not masked: %q", newText)
	}
	if !strings.Contains(newText, "*****") {
		t.Fatalf("expected asterisks mask, got %q", newText)
	}
}
