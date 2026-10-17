package jinaai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestEmbedding_SingleTextAndPrefixStripping(t *testing.T) {
	var capturedAuth string
	var capturedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		capturedAuth = r.Header.Get("Authorization")
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)

		resp := JinaEmbeddingResponse{
			Object: "list",
			Model:  "jina-embeddings-v3",
			Data: []JinaEmbeddingData{
				{
					Object:    "embedding",
					Index:     0,
					Embedding: json.RawMessage(`[0.1, 0.2, 0.3]`),
				},
			},
			Usage: JinaUsage{
				TotalTokens: 12, // Omitted prompt_tokens
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider, err := NewJinaAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL + "/v1",
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		Value: schemas.SecretVar{Val: "test-jina-key"},
	}

	req := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.JinaAI,
		Model:    "jina-ai/jina-embeddings-v3",
		Input: &schemas.EmbeddingInput{
			Text: schemas.Ptr("Hello world"),
		},
	}

	resp, bifrostErr := provider.Embedding(ctx, key, req)
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	assert.Equal(t, "Bearer test-jina-key", capturedAuth)

	// Verify prefix was stripped in the request wire body
	var wireReq JinaEmbeddingRequest
	err = json.Unmarshal(capturedBody, &wireReq)
	require.NoError(t, err)
	assert.Equal(t, "jina-embeddings-v3", wireReq.Model)
	assert.Equal(t, "Hello world", wireReq.Input)

	// Verify PromptTokens was backfilled from TotalTokens
	assert.Equal(t, 12, resp.Usage.TotalTokens)
	assert.Equal(t, 12, resp.Usage.PromptTokens)
	assert.Len(t, resp.Data, 1)
	assert.Equal(t, 0, resp.Data[0].Index)
}

func TestEmbedding_PreservesPromptTokensWhenPresent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := JinaEmbeddingResponse{
			Object: "list",
			Model:  "jina-embeddings-v5-omni-small",
			Data: []JinaEmbeddingData{
				{
					Object:    "embedding",
					Index:     0,
					Embedding: json.RawMessage(`[0.5, 0.6]`),
				},
			},
			Usage: JinaUsage{
				PromptTokens: 8,
				TotalTokens:  10,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider, err := NewJinaAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL + "/v1",
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		Value: schemas.SecretVar{Val: "test-key"},
	}

	req := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.JinaAI,
		Model:    "jina-embeddings-v5-omni-small",
		Input: &schemas.EmbeddingInput{
			Texts: []string{"test"},
		},
	}

	resp, bifrostErr := provider.Embedding(ctx, key, req)
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	// prompt_tokens was already 8, should not be overwritten by total_tokens 10
	assert.Equal(t, 8, resp.Usage.PromptTokens)
	assert.Equal(t, 10, resp.Usage.TotalTokens)
}

func TestEmbedding_MultimodalObjects(t *testing.T) {
	var capturedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)

		resp := JinaEmbeddingResponse{
			Object: "list",
			Model:  "jina-embeddings-v5-omni-small",
			Data: []JinaEmbeddingData{
				{
					Object:    "embedding",
					Index:     0,
					Embedding: json.RawMessage(`[0.1]`),
				},
				{
					Object:    "embedding",
					Index:     1,
					Embedding: json.RawMessage(`[0.2]`),
				},
			},
			Usage: JinaUsage{
				TotalTokens: 20,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider, err := NewJinaAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL + "/v1",
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}}

	req := &schemas.BifrostEmbeddingRequest{
		Provider: schemas.JinaAI,
		Model:    "jina-ai/jina-embeddings-v5-omni-small",
		Input: &schemas.EmbeddingInput{
			Objects: []map[string]interface{}{
				{"text": "A beautiful sunset"},
				{"image": "https://example.com/img.png"},
			},
		},
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"task":       "retrieval.query",
				"normalized": true,
			},
		},
	}

	resp, bifrostErr := provider.Embedding(ctx, key, req)
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	var parsed map[string]interface{}
	err = json.Unmarshal(capturedBody, &parsed)
	require.NoError(t, err)
	assert.Equal(t, "jina-embeddings-v5-omni-small", parsed["model"])
	assert.Equal(t, "retrieval.query", parsed["task"])
	assert.Equal(t, true, parsed["normalized"])

	inputs, ok := parsed["input"].([]interface{})
	require.True(t, ok)
	assert.Len(t, inputs, 2)
}

func TestRerank(t *testing.T) {
	var capturedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/rerank", r.URL.Path)
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)

		resp := JinaRerankResponse{
			Model: "jina-reranker-v2-base-multilingual",
			Results: []JinaRerankResult{
				{
					Index:          1,
					RelevanceScore: 0.95,
					Document:       &JinaRerankDocument{Text: "doc 2"},
				},
				{
					Index:          0,
					RelevanceScore: 0.25,
					Document:       &JinaRerankDocument{Text: "doc 1"},
				},
			},
			Usage: JinaUsage{
				TotalTokens: 30,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider, err := NewJinaAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL + "/v1",
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "key"}}

	req := &schemas.BifrostRerankRequest{
		Provider: schemas.JinaAI,
		Model:    "jina-ai/jina-reranker-v2-base-multilingual",
		Query:    "search query",
		Documents: []schemas.RerankDocument{
			{Text: "doc 1"},
			{Text: "doc 2"},
		},
		Params: &schemas.RerankParameters{
			TopN:            schemas.Ptr(2),
			ReturnDocuments: schemas.Ptr(true),
		},
	}

	resp, bifrostErr := provider.Rerank(ctx, key, req)
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	var wireReq JinaRerankRequest
	err = json.Unmarshal(capturedBody, &wireReq)
	require.NoError(t, err)
	assert.Equal(t, "jina-reranker-v2-base-multilingual", wireReq.Model)
	assert.Equal(t, "search query", wireReq.Query)
	assert.Len(t, wireReq.Documents, 2)

	assert.Len(t, resp.Results, 2)
	assert.Equal(t, 1, resp.Results[0].Index)
	assert.Equal(t, 0.95, resp.Results[0].RelevanceScore)
	assert.Equal(t, "doc 2", resp.Results[0].Document.Text)
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)

		resp := JinaModelsResponse{
			Data: []JinaModelEntry{
				{
					ID:               "jina-ai/jina-embeddings-v3",
					Name:             "Jina Embeddings v3",
					ContextLength:    8192,
					OutputModalities: []string{"embeddings"},
				},
				{
					ID:               "jina-ai/jina-reranker-v2-base-multilingual",
					Name:             "Jina Reranker v2",
					ContextLength:    8192,
					OutputModalities: []string{"text"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider, err := NewJinaAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL: server.URL + "/v1",
		},
	}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	keys := []schemas.Key{
		{
			Value:  schemas.SecretVar{Val: "key"},
			Models: schemas.WhiteList{"*"},
		},
	}

	resp, bifrostErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	require.Len(t, resp.Data, 2)

	// Check embeddings model
	embModel := resp.Data[0]
	assert.Equal(t, "jina-ai/jina-embeddings-v3", embModel.ID)
	assert.Contains(t, embModel.SupportedMethods, "embedding")

	// Check reranker model
	rerankModel := resp.Data[1]
	assert.Equal(t, "jina-ai/jina-reranker-v2-base-multilingual", rerankModel.ID)
	assert.Contains(t, rerankModel.SupportedMethods, "rerank")
}

func TestParseJinaError_FastAPIValidation(t *testing.T) {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(422)
	resp.SetBodyString(`{
		"detail": [
			{
				"loc": ["body", "model"],
				"msg": "Input should be 'jina-ocr-v1'",
				"type": "value_error"
			}
		]
	}`)

	bifrostErr := parseJinaError(resp)
	require.NotNil(t, bifrostErr)
	assert.Equal(t, 422, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.Error.Message, "Input should be 'jina-ocr-v1'")
	assert.Equal(t, schemas.JinaAI, bifrostErr.ExtraFields.Provider)
}

func TestParseJinaError_DetailStringAndCode(t *testing.T) {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(401)
	resp.SetBodyString(`{
		"code": "INVALID_TOKEN",
		"message": "Bearer token is invalid",
		"request_id": "req-xyz"
	}`)

	bifrostErr := parseJinaError(resp)
	require.NotNil(t, bifrostErr)
	assert.Equal(t, 401, *bifrostErr.StatusCode)
	assert.Equal(t, "Bearer token is invalid", bifrostErr.Error.Message)
	require.NotNil(t, bifrostErr.Error.Code)
	assert.Equal(t, "INVALID_TOKEN", *bifrostErr.Error.Code)
	require.NotNil(t, bifrostErr.EventID)
	assert.Equal(t, "req-xyz", *bifrostErr.EventID)
}

func TestUnsupportedOperations(t *testing.T) {
	provider, err := NewJinaAIProvider(&schemas.ProviderConfig{}, nil)
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{}

	_, err1 := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{})
	assert.NotNil(t, err1)
	assert.Equal(t, "unsupported_operation", *err1.Error.Code)

	_, err2 := provider.TextCompletion(ctx, key, &schemas.BifrostTextCompletionRequest{})
	assert.NotNil(t, err2)
	assert.Equal(t, "unsupported_operation", *err2.Error.Code)

	_, err3 := provider.ImageGeneration(ctx, key, &schemas.BifrostImageGenerationRequest{})
	assert.NotNil(t, err3)
	assert.Equal(t, "unsupported_operation", *err3.Error.Code)
}
