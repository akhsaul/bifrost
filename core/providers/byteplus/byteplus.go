// Package byteplus implements BytePlus Ark's OpenAI-compatible API.
package byteplus

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider is the shared OpenAI-compatible provider implementation.
type Provider = groq.GroqProvider

func NewBytePlusProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.BytePlus, "https://ark.ap-southeast.bytepluses.com/api/v3", "/models", "/chat/completions")
}
