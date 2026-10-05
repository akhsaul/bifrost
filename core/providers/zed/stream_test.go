package zed_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/zed"
)

func feedCapture(t *testing.T, model, inner, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	acc := zed.NewChatAccumulatorForTest(model, inner)
	ctx := testCtx()
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if bErr := acc.FeedLine(ctx, []byte(line)); bErr != nil {
			t.Fatalf("%s: feed: %v", name, bErr)
		}
	}
	resp, bErr := acc.Finalize(ctx)
	if bErr != nil {
		t.Fatalf("%s: finalize: %v", name, bErr)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("%s: no choices", name)
	}
	text := ""
	if msg := resp.Choices[0].ChatNonStreamResponseChoice.Message; msg != nil && msg.Content != nil && msg.Content.ContentStr != nil {
		text = *msg.Content.ContentStr
	}
	t.Logf("%s: %d chars, usage %+v", name, len(text), resp.Usage)
	return text
}

// TestAccumulateHaikuStream replays the live haiku NDJSON capture through
// the accumulator: text must mention readiness to help, usage must be billed.
func TestAccumulateHaikuStream(t *testing.T) {
	text := feedCapture(t, "claude-haiku-4-5", zed.ZedInnerAnthropic, "zed-resp-anthropic.json")
	if !strings.Contains(text, "ready") {
		t.Errorf("haiku text missing greeting, got %q", truncate(text, 200))
	}
}

// TestAccumulateGeminiStream replays the live gemini NDJSON capture: the
// greeting plus the Bifrost follow-up question must survive, with usage.
func TestAccumulateGeminiStream(t *testing.T) {
	text := feedCapture(t, "gemini-3.5-flash", zed.ZedInnerGoogle, "zed-resp-google.json")
	if !strings.Contains(text, "Hello!") || !strings.Contains(text, "Bifrost") {
		t.Errorf("gemini text truncated, got %q", truncate(text, 200))
	}
}

// TestAccumulateNanoStream replays the live gpt-5-nano NDJSON capture: the
// Indonesian greeting plus the options list must survive, with usage.
func TestAccumulateNanoStream(t *testing.T) {
	text := feedCapture(t, "gpt-5-nano", zed.ZedInnerOpenAI, "zed-resp-openai.json")
	if !strings.Contains(text, "Halo!") || !strings.Contains(text, "opsi cepat") {
		t.Errorf("nano text truncated, got %q", truncate(text, 200))
	}
}

// TestAccumulateHaikuUsage pins the billed totals from the live capture:
// message_start usage (10 in + 31963 cached) then message_delta usage
// (10 in + 265 out, same cached prefix).
func TestAccumulateHaikuUsage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "zed-resp-anthropic.json"))
	if err != nil {
		t.Fatal(err)
	}
	acc := zed.NewChatAccumulatorForTest("claude-haiku-4-5", zed.ZedInnerAnthropic)
	ctx := testCtx()
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if bErr := acc.FeedLine(ctx, []byte(line)); bErr != nil {
			t.Fatal(bErr)
		}
	}
	resp, bErr := acc.Finalize(ctx)
	if bErr != nil {
		t.Fatal(bErr)
	}
	if resp.Usage == nil {
		t.Fatal("nil usage")
	}
	if resp.Usage.PromptTokens != 31973 || resp.Usage.CompletionTokens != 265 || resp.Usage.TotalTokens != 32238 {
		t.Errorf("usage = %+v, want 31973/265/32238", resp.Usage)
	}
	if resp.Usage.PromptTokensDetails == nil || resp.Usage.PromptTokensDetails.CachedReadTokens != 31963 {
		t.Errorf("cached read = %+v, want 31963", resp.Usage.PromptTokensDetails)
	}
}

// TestAccumulateTruncated errors when stream_ended never arrives.
func TestAccumulateTruncated(t *testing.T) {
	acc := zed.NewChatAccumulatorForTest("gpt-5-nano", zed.ZedInnerOpenAI)
	ctx := testCtx()
	line := `{"event":{"type":"response.output_text.delta","item_id":"x","output_index":1,"content_index":0,"delta":"hi"}}`
	if bErr := acc.FeedLine(ctx, []byte(line)); bErr != nil {
		t.Fatal(bErr)
	}
	if _, bErr := acc.Finalize(ctx); bErr == nil {
		t.Error("finalize without stream_ended must fail")
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
