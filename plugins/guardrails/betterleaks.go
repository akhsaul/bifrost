package guardrails

import (
	"fmt"
	"strings"
	"sync"

	"github.com/betterleaks/betterleaks/config"
	"github.com/betterleaks/betterleaks/detect"
	"github.com/betterleaks/betterleaks/report"
	"github.com/maximhq/bifrost/core/schemas"
)

// betterleaksDetector wraps a Betterleaks scanner with guardrail actions.
// The underlying detector is process-wide: default rules are immutable and
// the scanner is safe for concurrent DetectString calls.
type betterleaksDetector struct {
	cfg SecretsConfig
}

var (
	blDetectorOnce sync.Once
	blDetector     *detect.Detector
	blDetectorErr  error
)

// sharedDetector lazily builds the default-config Betterleaks detector once.
// All SecretsConfig instances share it; per-config behavior (action, ignore
// keywords) is applied by the wrapper, not the detector.
func sharedDetector() (*detect.Detector, error) {
	blDetectorOnce.Do(func() {
		cfg, err := config.Default()
		if err != nil {
			blDetectorErr = fmt.Errorf("failed to load betterleaks default config: %w", err)
			return
		}
		blDetector = detect.NewDetector(cfg)
	})
	return blDetector, blDetectorErr
}

// newBetterleaksDetector prepares a scanner wrapper for the given secrets config.
func newBetterleaksDetector(cfg SecretsConfig) (*betterleaksDetector, error) {
	if _, err := sharedDetector(); err != nil {
		return nil, err
	}
	return &betterleaksDetector{cfg: cfg}, nil
}

// detect runs the shared Betterleaks detector over text and filters findings
// whose Secret or Match contains any ignored keyword.
func (b *betterleaksDetector) detect(text string) []report.Finding {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	d, err := sharedDetector()
	if err != nil {
		return nil
	}
	findings := d.DetectString(text)
	if len(b.cfg.IgnoredSecretKeywords) == 0 {
		return findings
	}
	filtered := make([]report.Finding, 0, len(findings))
	for _, f := range findings {
		suppressed := false
		for _, kw := range b.cfg.IgnoredSecretKeywords {
			if kw == "" {
				continue
			}
			if strings.Contains(f.Secret, kw) || strings.Contains(f.Match, kw) {
				suppressed = true
				break
			}
		}
		if !suppressed {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// evaluate applies the configured action over text, mirroring the semantics
// of evaluatePatterns for regex providers:
//   - blocked: non-empty when action is block and any finding matched
//   - newText: text after redaction rewrites (identical when no redaction)
//   - detected: one label per finding (ruleID) for logging
func (b *betterleaksDetector) evaluate(ctx *schemas.BifrostContext, text string, phase schemas.RedactionPhase) (blocked []string, newText string, detected []string) {
	newText = text
	if text == "" {
		return nil, text, nil
	}
	findings := b.detect(text)
	if len(findings) == 0 {
		return nil, text, nil
	}

	labels := make([]string, 0, len(findings))
	for _, f := range findings {
		labels = append(labels, f.RuleID)
	}

	switch b.cfg.Action {
	case PatternActionBlock:
		return labels, text, labels
	case PatternActionRedact:
		tracker := GetOrCreateTracker(ctx)
		type edit struct {
			start, end int
			repl       string
		}
		var edits []edit
		seenSecrets := make(map[string]bool)
		for _, f := range findings {
			if f.Secret == "" || seenSecrets[f.Secret] {
				continue
			}
			seenSecrets[f.Secret] = true
			repl := tracker.Redact(ctx, "SECRET", f.Secret, b.cfg.RedactionStrategy, b.cfg.RedactionMode, phase)

			// Scan all occurrences of this secret in the text to fix duplicate omission.
			offset := 0
			for {
				idx := strings.Index(text[offset:], f.Secret)
				if idx < 0 {
					break
				}
				start := offset + idx
				end := start + len(f.Secret)
				edits = append(edits, edit{start: start, end: end, repl: repl})
				offset = end
			}
		}

		// Sort edits descending by start index and apply back-to-front.
		for i := 0; i < len(edits); i++ {
			for j := i + 1; j < len(edits); j++ {
				if edits[j].start > edits[i].start {
					edits[i], edits[j] = edits[j], edits[i]
				}
			}
		}
		bb := []byte(text)
		lastStart := len(bb) + 1
		for _, e := range edits {
			if e.start < 0 || e.end > len(bb) || e.start >= e.end {
				continue
			}
			if e.end > lastStart {
				// Overlaps with an edit already applied to the right; skip
				continue
			}
			bb = append(bb[:e.start], append([]byte(e.repl), bb[e.end:]...)...)
			lastStart = e.start
		}
		newText = string(bb)
		return nil, newText, labels
	default: // detect_only
		return nil, text, labels
	}
}
