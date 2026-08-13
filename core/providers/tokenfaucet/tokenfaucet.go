// Package tokenfaucet implements TokenFaucet's OpenAI-compatible API.
package tokenfaucet

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider is the shared OpenAI-compatible provider implementation.
type Provider = groq.GroqProvider

// NewTokenFaucetProvider creates a TokenFaucet provider.
func NewTokenFaucetProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.TokenFaucet, "https://freetokenfaucet.com/v1", "/models", "/chat/completions")
}
