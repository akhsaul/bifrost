// Package morphllm implements Morphllm's OpenAI-compatible API.
package morphllm

import (
	"github.com/maximhq/bifrost/core/providers/groq"
	"github.com/maximhq/bifrost/core/schemas"
)

type Provider = groq.GroqProvider

func NewMorphllmProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*Provider, error) {
	return groq.NewCompatibleProvider(config, logger, schemas.Morphllm, "https://api.morphllm.com/v1", "/models", "/chat/completions")
}
