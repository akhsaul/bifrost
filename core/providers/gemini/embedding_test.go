package gemini_test

import (
	"os"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToBifrostEmbeddingResponse_UsageMetadata(t *testing.T) {
	geminiResp := &gemini.GeminiEmbeddingResponse{
		Embeddings: []gemini.GeminiEmbedding{
			{
				Values: []float64{0.1, 0.2, 0.3},
			},
		},
		UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 2,
			PromptTokenDetails: []*gemini.ModalityTokenCount{
				{
					Modality:   gemini.ModalityText,
					TokenCount: 2,
				},
			},
		},
	}

	bifrostResp := gemini.ToBifrostEmbeddingResponse(geminiResp, "gemini-embedding-2")
	require.NotNil(t, bifrostResp)
	require.NotNil(t, bifrostResp.Usage)

	assert.Equal(t, 2, bifrostResp.Usage.PromptTokens)
	assert.Equal(t, 2, bifrostResp.Usage.TotalTokens)
	require.NotNil(t, bifrostResp.Usage.PromptTokensDetails)
	assert.Equal(t, 2, bifrostResp.Usage.PromptTokensDetails.TextTokens)
}

func TestToBifrostEmbeddingResponse_RealDebugPayload(t *testing.T) {
	// Read real response debug file if present
	debugPath := "../../../debug/gemini/resp-gemini-embedding-2.json"
	rawJSON, err := os.ReadFile(debugPath)
	if err != nil {
		t.Skip("debug file not available, skipping real payload test")
	}

	var geminiResp gemini.GeminiEmbeddingResponse
	err = sonic.Unmarshal(rawJSON, &geminiResp)
	require.NoError(t, err)

	require.NotEmpty(t, geminiResp.Embeddings)
	require.NotNil(t, geminiResp.UsageMetadata)
	assert.Equal(t, int32(2), geminiResp.UsageMetadata.PromptTokenCount)

	bifrostResp := gemini.ToBifrostEmbeddingResponse(&geminiResp, "gemini-embedding-2")
	require.NotNil(t, bifrostResp)
	require.NotNil(t, bifrostResp.Usage)
	assert.Equal(t, 2, bifrostResp.Usage.PromptTokens)
	assert.Equal(t, 2, bifrostResp.Usage.TotalTokens)
	require.NotNil(t, bifrostResp.Usage.PromptTokensDetails)
	assert.Equal(t, 2, bifrostResp.Usage.PromptTokensDetails.TextTokens)
}

func TestToBifrostEmbeddingResponse_PrioritizeEmbedContentMetadata(t *testing.T) {
	geminiResp := &gemini.GeminiEmbeddingResponse{
		Embeddings: []gemini.GeminiEmbedding{
			{
				Values: []float64{0.1, 0.2},
			},
		},
		Metadata: &gemini.EmbedContentMetadata{
			BillableCharacterCount: 15,
		},
		UsageMetadata: &gemini.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 5,
		},
	}

	bifrostResp := gemini.ToBifrostEmbeddingResponse(geminiResp, "gemini-embedding-2")
	require.NotNil(t, bifrostResp)
	require.NotNil(t, bifrostResp.Usage)

	// EmbedContentMetadata must take priority over UsageMetadata
	assert.Equal(t, 15, bifrostResp.Usage.PromptTokens)
	assert.Equal(t, 15, bifrostResp.Usage.TotalTokens)
}

func TestToBifrostEmbeddingResponse_FallbackStatistics(t *testing.T) {
	geminiResp := &gemini.GeminiEmbeddingResponse{
		Embeddings: []gemini.GeminiEmbedding{
			{
				Values: []float64{0.1, 0.2},
				Statistics: &gemini.ContentEmbeddingStatistics{
					TokenCount: 4,
				},
			},
			{
				Values: []float64{0.3, 0.4},
				Statistics: &gemini.ContentEmbeddingStatistics{
					TokenCount: 6,
				},
			},
		},
	}

	bifrostResp := gemini.ToBifrostEmbeddingResponse(geminiResp, "text-embedding-004")
	require.NotNil(t, bifrostResp)
	require.NotNil(t, bifrostResp.Usage)

	// Fallback to aggregated statistics across embeddings
	assert.Equal(t, 10, bifrostResp.Usage.PromptTokens)
	assert.Equal(t, 10, bifrostResp.Usage.TotalTokens)
}

func TestToGeminiEmbeddingResponse_IncludesUsageMetadata(t *testing.T) {
	bifrostResp := &schemas.BifrostEmbeddingResponse{
		Data: []schemas.EmbeddingData{
			{
				Embedding: schemas.EmbeddingStruct{
					EmbeddingArray: []float64{0.5, 0.6},
				},
			},
		},
		Usage: &schemas.BifrostLLMUsage{
			PromptTokens: 8,
			TotalTokens:  8,
		},
	}

	geminiResp := gemini.ToGeminiEmbeddingResponse(bifrostResp)
	require.NotNil(t, geminiResp)
	require.NotNil(t, geminiResp.Metadata)
	assert.Equal(t, int32(8), geminiResp.Metadata.BillableCharacterCount)

	require.NotNil(t, geminiResp.UsageMetadata)
	assert.Equal(t, int32(8), geminiResp.UsageMetadata.PromptTokenCount)
	assert.Equal(t, int32(8), geminiResp.UsageMetadata.TotalTokenCount)
}
