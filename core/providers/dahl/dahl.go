// Package dahl implements Dahl's OpenAI-compatible API.
package dahl

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

type Provider = groq.GroqProvider

func NewDahlProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.Dahl, "https://inference.dahl.global/v1", "/models", "/chat/completions")
}
