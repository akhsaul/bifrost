package guardrails

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// extractRequestText concatenates all text content of a chat request.
// Non-chat request types return "" (v1 scans chat requests only).
func extractRequestText(req *schemas.BifrostRequest) string {
	if req == nil || req.ChatRequest == nil {
		return ""
	}
	var b strings.Builder
	for i := range req.ChatRequest.Input {
		appendMessageText(&b, &req.ChatRequest.Input[i])
	}
	return b.String()
}

// appendMessageText appends the message's text content (string or text blocks).
func appendMessageText(b *strings.Builder, msg *schemas.ChatMessage) {
	if msg == nil || msg.Content == nil {
		return
	}
	if msg.Content.ContentStr != nil {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(*msg.Content.ContentStr)
		return
	}
	for i := range msg.Content.ContentBlocks {
		blk := &msg.Content.ContentBlocks[i]
		if blk.Text != nil {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(*blk.Text)
		}
	}
}

// redactRequestInPlace replaces every occurrence of match with repl in the
// request's message contents (string and text blocks). Returns true when any
// replacement happened.
func redactRequestInPlace(req *schemas.BifrostRequest, match, repl string) bool {
	if req == nil || req.ChatRequest == nil || match == "" {
		return false
	}
	replaced := false
	for i := range req.ChatRequest.Input {
		msg := &req.ChatRequest.Input[i]
		if msg.Content == nil {
			continue
		}
		if msg.Content.ContentStr != nil && strings.Contains(*msg.Content.ContentStr, match) {
			msg.Content.ContentStr = ptr(strings.ReplaceAll(*msg.Content.ContentStr, match, repl))
			replaced = true
		}
		for j := range msg.Content.ContentBlocks {
			blk := &msg.Content.ContentBlocks[j]
			if blk.Text != nil && strings.Contains(*blk.Text, match) {
				blk.Text = ptr(strings.ReplaceAll(*blk.Text, match, repl))
				replaced = true
			}
		}
	}
	return replaced
}

// extractResponseText concatenates all text content of a chat response.
func extractResponseText(resp *schemas.BifrostResponse) string {
	if resp == nil || resp.ChatResponse == nil {
		return ""
	}
	var b strings.Builder
	for i := range resp.ChatResponse.Choices {
		c := &resp.ChatResponse.Choices[i]
		if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil {
			appendMessageText(&b, c.ChatNonStreamResponseChoice.Message)
		}
		if c.TextCompletionResponseChoice != nil && c.TextCompletionResponseChoice.Text != nil {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(*c.TextCompletionResponseChoice.Text)
		}
	}
	return b.String()
}

// redactResponseInPlace replaces every occurrence of match with repl in the
// response's message / completion texts. Returns true when any replacement
// happened. Only string content fields are rewritten — structure is untouched.
func redactResponseInPlace(resp *schemas.BifrostResponse, match, repl string) bool {
	if resp == nil || resp.ChatResponse == nil || match == "" {
		return false
	}
	replaced := false
	for i := range resp.ChatResponse.Choices {
		c := &resp.ChatResponse.Choices[i]
		if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil {
			replaced = redactMessageInPlace(c.ChatNonStreamResponseChoice.Message, match, repl) || replaced
		}
		if c.TextCompletionResponseChoice != nil && c.TextCompletionResponseChoice.Text != nil &&
			strings.Contains(*c.TextCompletionResponseChoice.Text, match) {
			c.TextCompletionResponseChoice.Text = ptr(strings.ReplaceAll(*c.TextCompletionResponseChoice.Text, match, repl))
			replaced = true
		}
	}
	return replaced
}

// redactMessageInPlace redacts one message's contents.
func redactMessageInPlace(msg *schemas.ChatMessage, match, repl string) bool {
	if msg == nil || msg.Content == nil {
		return false
	}
	replaced := false
	if msg.Content.ContentStr != nil && strings.Contains(*msg.Content.ContentStr, match) {
		msg.Content.ContentStr = ptr(strings.ReplaceAll(*msg.Content.ContentStr, match, repl))
		replaced = true
	}
	for j := range msg.Content.ContentBlocks {
		blk := &msg.Content.ContentBlocks[j]
		if blk.Text != nil && strings.Contains(*blk.Text, match) {
			blk.Text = ptr(strings.ReplaceAll(*blk.Text, match, repl))
			replaced = true
		}
	}
	return replaced
}
