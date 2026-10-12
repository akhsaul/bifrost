import { describe, expect, it } from "vitest";
import {
	clineKeyConfigSchema,
	codexKeyConfigSchema,
	zedKeyConfigSchema,
	modelProviderKeySchema,
} from "./schemas";

const base = { id: "k1", name: "test-key", models: ["*"], blacklisted_models: [], weight: 1 };
const secret = (value: string) => ({ value, ref: "" });

describe("clineKeyConfigSchema and modelProviderKeySchema for cline", () => {
	it("accepts _auth_type discriminator", () => {
		const oauthParsed = clineKeyConfigSchema.safeParse({
			_auth_type: "oauth",
			refresh_token: secret("refresh-token"),
		});
		expect(oauthParsed.success).toBe(true);

		const manualParsed = clineKeyConfigSchema.safeParse({
			_auth_type: "manual",
			client_id: secret("client-id"),
			refresh_token: secret("refresh-token"),
		});
		expect(manualParsed.success).toBe(true);
	});

	it("validates cline key with OAuth refresh token in modelProviderKeySchema", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "", ref: "" },
			cline_key_config: {
				refresh_token: secret("refresh-token"),
			},
		});
		expect(result.success).toBe(true);
	});

	it("validates cline key with manual API key in modelProviderKeySchema", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: secret("cline-api-key"),
			cline_key_config: {
				_auth_type: "manual",
			},
		});
		expect(result.success).toBe(true);
	});
});

describe("codexKeyConfigSchema and modelProviderKeySchema for codex", () => {
	it("accepts _auth_type discriminator", () => {
		const oauthParsed = codexKeyConfigSchema.safeParse({
			_auth_type: "oauth",
			refresh_token: secret("codex-refresh-token"),
		});
		expect(oauthParsed.success).toBe(true);

		const manualParsed = codexKeyConfigSchema.safeParse({
			_auth_type: "manual",
			refresh_token: secret("codex-refresh-token"),
			client_id: secret("app_EMoamEEZ73f0CkXaXp7hrann"),
			account_id: secret("1a3b5c7d-1"),
		});
		expect(manualParsed.success).toBe(true);
	});

	it("validates codex key with OAuth refresh token and empty value without error", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "", ref: "" },
			codex_key_config: {
				refresh_token: secret("codex-refresh-token"),
				_auth_type: "oauth",
			},
		});
		expect(result.success).toBe(true);
	});

	it("rejects codex key without refresh token or value", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "", ref: "" },
			codex_key_config: {
				client_id: secret("app_EMoamEEZ73f0CkXaXp7hrann"),
				_auth_type: "oauth",
			},
		});
		expect(result.success).toBe(false);
	});
});

describe("zedKeyConfigSchema and modelProviderKeySchema for zed", () => {
	it("accepts _auth_type discriminator", () => {
		const oauthParsed = zedKeyConfigSchema.safeParse({
			_auth_type: "oauth",
			user_id: secret("123456"),
			access_token: secret('{"version":2,"token":"xyz"}'),
		});
		expect(oauthParsed.success).toBe(true);

		const manualParsed = zedKeyConfigSchema.safeParse({
			_auth_type: "manual",
			user_id: secret("123456"),
			access_token: secret('{"version":2,"token":"xyz"}'),
		});
		expect(manualParsed.success).toBe(true);
	});

	it("validates zed key with user_id and access_token in modelProviderKeySchema", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			zed_key_config: {
				_auth_type: "oauth",
				user_id: secret("123456"),
				access_token: secret('{"version":2,"token":"xyz"}'),
			},
		});
		expect(result.success).toBe(true);
	});

	it("rejects zed key missing access_token", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			zed_key_config: {
				_auth_type: "manual",
				user_id: secret("123456"),
			},
		});
		expect(result.success).toBe(false);
	});
});
