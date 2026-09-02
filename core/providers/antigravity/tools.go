package antigravity

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// convertToolsToAntigravityFormat rewrites Gemini tool declarations from the
// plain JSON Schema form ("parametersJsonSchema" with lowercase type names)
// to the Gemini proto Schema form ("parameters" with UPPERCASE type names).
//
// The Antigravity internal API (daily-cloudcode-pa.googleapis.com
// /v1internal:generateContent and :streamGenerateContent) rejects requests
// carrying parametersJsonSchema; it only accepts the proto Schema form, the
// same wire format the agy CLI sends (see captured data-agy-gemini.json).
func convertToolsToAntigravityFormat(tools []gemini.Tool) error {
	for _, tool := range tools {
		for _, fd := range tool.FunctionDeclarations {
			if fd.ParametersJSONSchema == nil {
				continue
			}
			raw, err := sonic.Marshal(fd.ParametersJSONSchema)
			if err != nil {
				return fmt.Errorf("marshal parameters for tool %q: %w", fd.Name, err)
			}
			var params schemas.ToolFunctionParameters
			if err := sonic.Unmarshal(raw, &params); err != nil {
				return fmt.Errorf("unmarshal parameters for tool %q: %w", fd.Name, err)
			}
			schema := gemini.ConvertFunctionParametersToSchema(params)
			uppercaseSchemaTypes(schema)
			fd.Parameters = schema
			fd.ParametersJSONSchema = nil
		}
	}
	return nil
}

// applyDefaultThinkingConfig injects the agy CLI default thinking config
// (includeThoughts=true, thinkingBudget=-1 dynamic) when the client sent no
// reasoning parameters and the Gemini conversion produced no ThinkingConfig.
// Client-provided reasoning is never overwritten.
func applyDefaultThinkingConfig(geminiReq *gemini.GeminiGenerationRequest) {
	if geminiReq == nil || geminiReq.GenerationConfig.ThinkingConfig != nil {
		return
	}
	budget := int32(gemini.DynamicReasoningBudget)
	geminiReq.GenerationConfig.ThinkingConfig = &gemini.GenerationConfigThinkingConfig{
		IncludeThoughts: true,
		ThinkingBudget:  &budget,
	}
}

// uppercaseSchemaTypes recursively uppercases Type enum values in a Gemini
// Schema tree ("object" -> "OBJECT", "string" -> "STRING"), matching the agy
// CLI wire format, which uses the proto Type enum names exclusively.
func uppercaseSchemaTypes(schema *gemini.Schema) {
	if schema == nil {
		return
	}
	if schema.Type != "" {
		schema.Type = gemini.Type(strings.ToUpper(string(schema.Type)))
	}
	for _, child := range schema.Properties {
		uppercaseSchemaTypes(child)
	}
	uppercaseSchemaTypes(schema.Items)
	for _, child := range schema.AnyOf {
		uppercaseSchemaTypes(child)
	}
}
