// Package inferx implements Inferx's OpenAI-compatible API.
package inferx

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider is the shared OpenAI-compatible provider implementation.
type Provider = groq.GroqProvider

// NewInferxProvider creates an Inferx provider.
func NewInferxProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.Inferx, "https://model.inferx.net/endpoints/v1", "/models", "/chat/completions")
}
