import { describe, expect, it } from "vitest";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { ProviderFormSchema } from "./providerForm";

// Antigravity OAuth stores only a refresh token (and project ID) in the key
// config; the API key field stays blank. Zod strips undeclared keys, so a
// credential the schema does not know about never reaches the API — the form
// would save, report success, and the key would arrive with no usable auth.
describe("ProviderFormSchema antigravity credentials", () => {
	const literal = (value: string) => ({ value, ref: "" });
	const envRef = (ref: string) => ({ value: "", ref, type: "env" as const });

	const oauthConfig = (overrides: Record<string, unknown> = {}) => ({
		refresh_token: literal("refresh-token-value"),
		project_id: literal("my-project"),
		...overrides,
	});

	const form = (keyOverrides: Record<string, unknown>) => ({
		selectedProvider: "antigravity",
		isDirty: true,
		networkConfig: DefaultNetworkConfig,
		keys: [{ id: "k1", name: "antigravity", value: "", models: ["*"], weight: 1, ...keyOverrides }],
	});

	const parse = (keyOverrides: Record<string, unknown>) => ProviderFormSchema.safeParse(form(keyOverrides));

	it("keeps antigravity_key_config through parsing", () => {
		const parsed = parse({ antigravity_key_config: oauthConfig() });

		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
		if (!parsed.success) return;

		const key = parsed.data.keys[0];
		expect(key.antigravity_key_config, "credentials were stripped, so the key would save with no auth").toBeDefined();
		expect(key.antigravity_key_config?.refresh_token?.value).toBe("refresh-token-value");
		expect(key.antigravity_key_config?.project_id?.value).toBe("my-project");
	});

	it("accepts a manual Antigravity token/json in value with no oauth config", () => {
		expect(parse({ value: "manual-token" }).success).toBe(true);
	});

	it("rejects a key with neither value nor oauth refresh_token", () => {
		expect(parse({}).success, "a key with no credential at all must not save").toBe(false);
	});

	it("accepts an env-referenced refresh token with empty value", () => {
		const parsed = parse({ antigravity_key_config: { refresh_token: envRef("ANTIGRAVITY_REFRESH_TOKEN") } });
		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
	});
});