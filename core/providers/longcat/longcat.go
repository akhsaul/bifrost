// Package longcat implements Longcat's OpenAI-compatible API.
package longcat

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider is the shared OpenAI-compatible provider implementation.
type Provider = groq.GroqProvider

// NewLongcatProvider creates a Longcat provider.
func NewLongcatProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.Longcat, "https://api.longcat.chat/openai/v1", "/models", "/chat/completions")
}
