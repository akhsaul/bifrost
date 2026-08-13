// Package tokenharbor implements TokenHarbor's OpenAI-compatible API.
package tokenharbor

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

type Provider = groq.GroqProvider

func NewTokenHarborProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.TokenHarbor, "https://tokenharbor.ai/v1", "/models", "/chat/completions")
}
