/**
 * CEL Fields Configuration for Guardrails Rules
 * Defines available fields for building guardrail rule expressions
 */

import { getProviderLabel } from "@/lib/constants/logs";
import { CELFieldDefinition } from "./celFieldsRouting";

export const baseGuardrailsFields: CELFieldDefinition[] = [
	{
		name: "model",
		label: "Model",
		placeholder: "e.g., gpt-4, claude-3-sonnet",
		inputType: "text",
		valueEditorType: (operator: string) =>
			operator === "=" || operator === "!=" ? "select" : operator === "in" || operator === "notIn" ? "select" : "text",
		operators: ["=", "!=", "in", "notIn", "contains", "beginsWith", "endsWith", "matches"],
		defaultOperator: "=",
	},
	{
		name: "provider",
		label: "Provider",
		placeholder: "Select provider",
		inputType: "select",
		valueEditorType: (operator: string) =>
			operator === "matches" ? "text" : operator === "in" || operator === "notIn" ? "select" : "select",
		operators: ["=", "!=", "in", "notIn", "matches", "null", "notNull"],
		defaultOperator: "=",
	},
	{
		name: "headers",
		label: "Header",
		placeholder: "e.g., authorization, x-custom-header (use lowercase)",
		inputType: "keyValue",
		valueEditorType: "keyValue",
		operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
		defaultOperator: "=",
	},
	{
		name: "params",
		label: "Query Parameter",
		placeholder: "e.g., api_key, user_id",
		inputType: "keyValue",
		valueEditorType: "keyValue",
		operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
		defaultOperator: "=",
	},
	{
		name: "virtual_key_id",
		label: "Virtual Key ID",
		placeholder: "e.g., vk-12345",
		inputType: "text",
		valueEditorType: "text",
		operators: ["=", "!=", "in", "notIn", "contains"],
		defaultOperator: "=",
	},
	{
		name: "virtual_key_name",
		label: "Virtual Key Name",
		placeholder: "e.g., prod-key",
		inputType: "text",
		valueEditorType: "text",
		operators: ["=", "!=", "in", "notIn", "contains"],
		defaultOperator: "=",
	},
];

/**
 * Get guardrails fields with dynamic providers and models
 */
export function getGuardrailsFields(providers: string[] = [], models: string[] = []): CELFieldDefinition[] {
	// Create provider field values
	const providerValues =
		providers.length > 0
			? providers.map((provider) => ({
					name: provider,
					label: getProviderLabel(provider),
				}))
			: [{ name: "_no_providers", label: "No providers configured", disabled: true }];

	// Create model field values
	const modelValues =
		models.length > 0
			? models.map((model) => ({
					name: model,
					label: model,
				}))
			: [];

	// Update provider field with dynamic values
	const fieldsWithDynamicValues = baseGuardrailsFields.map((field) => {
		if (field.name === "provider") {
			return {
				...field,
				values: providerValues,
			};
		}
		if (field.name === "model") {
			return {
				...field,
				values: modelValues,
			};
		}
		return field;
	});

	return fieldsWithDynamicValues;
}
