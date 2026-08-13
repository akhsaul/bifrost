// Package poolside implements Poolside's OpenAI-compatible API.
package poolside

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider is the shared OpenAI-compatible provider implementation.
type Provider = groq.GroqProvider

// NewPoolsideProvider creates a Poolside provider.
func NewPoolsideProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.Poolside, "https://inference.poolside.ai/v1", "/models", "/chat/completions")
}
