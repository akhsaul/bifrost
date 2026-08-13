// Package aisure implements AISure's OpenAI-compatible chat endpoint.
package aisure

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

type Provider = groq.GroqProvider

func NewAISureProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.AISure, "https://wtwbcruvpghcppwahiaj.supabase.co", "/models", "/functions/v1/chat")
}
