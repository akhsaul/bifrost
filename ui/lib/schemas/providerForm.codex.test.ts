import { describe, expect, it } from "vitest";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { ProviderFormSchema } from "./providerForm";

// Mirrors providerForm.cline.test.ts for Codex's dual auth: a static access
// token in `value`, or the OAuth refresh token in `codex_key_config`. Zod
// strips undeclared keys, so a credential the schema does not know about
// never reaches the API — the form would save, report success, and the key
// would arrive with no usable authentication at all.
describe("ProviderFormSchema codex credentials", () => {
	const form = (keyOverrides: Record<string, unknown>) => ({
		selectedProvider: "codex",
		isDirty: true,
		networkConfig: DefaultNetworkConfig,
		keys: [{ id: "k1", name: "codex", value: "", models: ["*"], weight: 1, ...keyOverrides }],
	});

	const oauthConfig = (overrides: Record<string, unknown> = {}) => ({
		refresh_token: "refresh-token-value",
		...overrides,
	});

	const parse = (keyOverrides: Record<string, unknown>) => ProviderFormSchema.safeParse(form(keyOverrides));

	it("keeps codex_key_config through parsing", () => {
		const parsed = parse({ codex_key_config: oauthConfig() });

		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
		if (!parsed.success) return;

		const key = parsed.data.keys[0];
		expect(key.codex_key_config, "credentials were stripped, so the key would save with no auth").toBeDefined();
		expect(key.codex_key_config?.refresh_token).toBe("refresh-token-value");
	});

	it("accepts a static Codex access token with no oauth config", () => {
		expect(parse({ value: "codex-token" }).success).toBe(true);
	});

	it("rejects a key with neither a static token nor oauth credentials", () => {
		expect(parse({}).success, "a key with no credential at all must not save").toBe(false);
	});

	it("rejects an oauth config without a refresh token", () => {
		expect(parse({ codex_key_config: { client_id: "app_EMoamEEZ73f0CkXaXp7hrann" } }).success, "client_id alone is not a credential").toBe(
			false,
		);
	});

	it("accepts an env-referenced refresh token", () => {
		const parsed = parse({ codex_key_config: { refresh_token: "env.CODEX_REFRESH_TOKEN" } });
		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
	});

	it("keeps the optional account_id override", () => {
		const parsed = parse({ codex_key_config: oauthConfig({ account_id: "1a3b5c7d-1" }) });

		expect(parsed.success, parsed.success ? "" : JSON.stringify(parsed.error.issues)).toBe(true);
		if (!parsed.success) return;
		expect(parsed.data.keys[0].codex_key_config?.account_id).toBe("1a3b5c7d-1");
	});
});