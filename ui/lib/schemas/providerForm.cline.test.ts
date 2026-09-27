import { describe, expect, it } from "vitest";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { ProviderFormSchema } from "./providerForm";

// Mirrors providerForm.copilot.test.ts for Cline's dual auth: a static API key
// in `value`, or the OAuth refresh token in `cline_key_config`. Zod strips
// undeclared keys, so a credential the schema does not know about never reaches
// the API — the form would save, report success, and the key would arrive with
// no usable authentication at all.
describe("ProviderFormSchema cline credentials", () => {
	const literal = (value: string) => ({ value, ref: "" });
	const envRef = (ref: string) => ({ value: "", ref, type: "env" as const });

	const oauthConfig = (overrides: Record<string, unknown> = {}) => ({
		refresh_token: literal("refresh-token-value"),
		...overrides,
	});

	const form = (keyOverrides: Record<string, unknown>) => ({
		selectedProvider: "cline",
		isDirty: true,
		networkConfig: DefaultNetworkConfig,
		keys: [{ id: "k1", name: "cline", value: "", models: ["*"], weight: 1, ...keyOverrides }],
	});

	const parse = (keyOverrides: Record<string, unknown>) => ProviderFormSchema.safeParse(form(keyOverrides));

	it("keeps cline_key_config through parsing", () => {
		const parsed = parse({ cline_key_config: oauthConfig() });

		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
		if (!parsed.success) return;

		const key = parsed.data.keys[0];
		expect(key.cline_key_config, "credentials were stripped, so the key would save with no auth").toBeDefined();
		expect(key.cline_key_config?.refresh_token?.value).toBe("refresh-token-value");
	});

	it("accepts a static Cline key with no oauth config", () => {
		expect(parse({ value: "cline-key" }).success).toBe(true);
	});

	it("rejects a key with neither a static key nor oauth credentials", () => {
		expect(parse({}).success, "a key with no credential at all must not save").toBe(false);
	});

	it("rejects an oauth config without a refresh token", () => {
		expect(
			parse({ cline_key_config: { client_id: literal("client_01K3A541FN8TA3EPPHTD2325AR") } }).success,
			"client_id alone is not a credential",
		).toBe(false);
	});

	it("accepts an env-referenced refresh token", () => {
		const parsed = parse({ cline_key_config: { refresh_token: envRef("CLINE_REFRESH_TOKEN") } });
		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
	});
});