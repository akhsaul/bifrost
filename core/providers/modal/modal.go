// Package modal implements Modal's OpenAI-compatible endpoint API.
package modal

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

// Provider is the shared OpenAI-compatible provider implementation.
type Provider = groq.GroqProvider

func NewModalProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.Modal, "https://api.us-west-2.modal.direct", "/v1/models", "/v1/chat/completions")
}
