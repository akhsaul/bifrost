// Package tokenrouter implements TokenRouter's OpenAI-compatible API.
package tokenrouter

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider is the shared OpenAI-compatible provider implementation.
type Provider = groq.GroqProvider

func NewTokenRouterProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.TokenRouter, "https://api.tokenrouter.com/v1", "/models", "/chat/completions")
}
