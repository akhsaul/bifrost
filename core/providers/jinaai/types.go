package jinaai

import (
	"encoding/json"
)

// JinaEmbeddingRequest represents an embedding request payload for Jina AI.
type JinaEmbeddingRequest struct {
	Model         string                 `json:"model"`
	Input         interface{}            `json:"input"`
	Task          *string                `json:"task,omitempty"`
	Normalized    *bool                  `json:"normalized,omitempty"`
	Dimensions    *int                   `json:"dimensions,omitempty"`
	EmbeddingType *string                `json:"embedding_type,omitempty"`
	LateChunking  *bool                  `json:"late_chunking,omitempty"`
	ExtraParams   map[string]interface{} `json:"-"`
}

// GetExtraParams returns extra dynamic parameters for serialization.
func (r *JinaEmbeddingRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// JinaUsage represents token usage information in Jina AI responses.
type JinaUsage struct {
	TotalTokens  int `json:"total_tokens"`
	PromptTokens int `json:"prompt_tokens,omitempty"`
	ImageTokens  int `json:"image_tokens,omitempty"`
}

// JinaEmbeddingData represents a single embedding item in Jina AI response.
type JinaEmbeddingData struct {
	Object    string          `json:"object"`
	Index     int             `json:"index"`
	Embedding json.RawMessage `json:"embedding"`
}

// JinaEmbeddingResponse represents the response structure from Jina AI embeddings endpoint.
type JinaEmbeddingResponse struct {
	Object string              `json:"object"`
	Data   []JinaEmbeddingData `json:"data"`
	Model  string              `json:"model"`
	Usage  JinaUsage           `json:"usage"`
}

// JinaRerankRequest represents a reranking request payload for Jina AI.
type JinaRerankRequest struct {
	Model           string                 `json:"model"`
	Query           string                 `json:"query"`
	Documents       []interface{}          `json:"documents"`
	TopN            *int                   `json:"top_n,omitempty"`
	ReturnDocuments *bool                  `json:"return_documents,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns extra dynamic parameters for serialization.
func (r *JinaRerankRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// JinaRerankDocument represents a document in Jina AI rerank result.
type JinaRerankDocument struct {
	Text string `json:"text,omitempty"`
}

// JinaRerankResult represents a single ranked result item.
type JinaRerankResult struct {
	Index          int                 `json:"index"`
	RelevanceScore float64             `json:"relevance_score"`
	Document       *JinaRerankDocument `json:"document,omitempty"`
}

// JinaRerankResponse represents the response structure from Jina AI rerank endpoint.
type JinaRerankResponse struct {
	Model   string             `json:"model"`
	Object  string             `json:"object,omitempty"`
	Results []JinaRerankResult `json:"results"`
	Usage   JinaUsage          `json:"usage"`
}

// JinaModelEntry represents a model descriptor in Jina AI's /v1/models response.
type JinaModelEntry struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	ContextLength    int      `json:"context_length"`
	MaxOutputLength  int      `json:"max_output_length"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

// JinaModelsResponse represents the response from Jina AI's GET /v1/models endpoint.
type JinaModelsResponse struct {
	Data []JinaModelEntry `json:"data"`
}

// JinaValidationError represents a field-level validation error detail from FastAPI.
type JinaValidationError struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
}

// JinaErrorResponse represents error responses returned by Jina AI.
type JinaErrorResponse struct {
	Detail    interface{}           `json:"detail,omitempty"`
	Code      *string               `json:"code,omitempty"`
	Message   *string               `json:"message,omitempty"`
	RequestID *string               `json:"request_id,omitempty"`
	Errors    []JinaValidationError `json:"errors,omitempty"`
}
