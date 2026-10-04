import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MultiSelect, type MultiSelectOption } from "@/components/ui/multiSelect";
import { SearchSelect } from "@/components/ui/searchSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { NoPermissionView } from "@/components/noPermissionView";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
	useGetModelDetailsQuery,
	useGetModelParametersQuery,
	useGetModelsQuery,
	useGetProvidersQuery,
	useGetVirtualKeyQuery,
	useGetVirtualKeysQuery,
} from "@/lib/store";
import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import type { VirtualKey } from "@/lib/types/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Check, Copy, Download, KeyRound } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { buildAgentConfig, type AgentConfigInput, type AgentOpencodeModelInput } from "./agentConfigBuilders";
import {
	AGENTS,
	CODEX_REASONING_EFFORTS,
	DEFAULT_BIFROST_ENV_VAR,
	DEFAULT_CLAUDE_ENV_VAR,
	DEFAULT_REASONING_EFFORTS,
	EMPTY_AGENT_METADATA,
	extractModelTrigger,
	extractReasoningEfforts,
	type AgentId,
	type AgentModelMetadata,
	type ModelSelectionItem,
} from "./agentSetupTypes";

function defaultEnvVar(agent: AgentId): string {
	return agent === "claude-code" ? DEFAULT_CLAUDE_ENV_VAR : DEFAULT_BIFROST_ENV_VAR;
}

/** Bifrost origin: current window origin, owned by the caller via an editable override. */
function defaultBaseUrl(): string {
	if (typeof window !== "undefined" && window.location.origin) return window.location.origin.replace(/\/+$/, "");
	return "";
}

/** Opencode modalities enum (v2 docs). Datasheet values outside this set are kept as-is. */
const OPENCODE_MODALITIES = ["text", "audio", "image", "video", "pdf"];

// ── Virtual-key allowlist helpers ──────────────────────────────────────
// From the agent's point of view a virtual key IS the api key: when one is
// picked, every picker below only offers what that key may reach. The VK
// list endpoint already returns provider_configs (incl. the secret value),
// so no extra reveal step is needed — the secret is used as the x-bf-vk
// header for exact server-side scoping and never rendered.

/** Providers the key may call. Empty set = key allows nothing. */
function allowedProvidersForVk(vk: VirtualKey | undefined): Set<string> | null {
	if (!vk) return null;
	if (vk.allow_all_providers) return null;
	return new Set((vk.provider_configs ?? []).map((c) => c.provider));
}

/** Whether the key may call `model` on `provider`. */
function isModelAllowedForVk(vk: VirtualKey | undefined, provider: string, model: string): boolean {
	if (!vk || vk.allow_all_providers) return true;
	const cfg = (vk.provider_configs ?? []).find((c) => c.provider === provider);
	if (!cfg) return false;
	if (cfg.blacklisted_models?.includes(model)) return false;
	const allowed = cfg.allowed_models ?? [];
	return allowed.includes("*") || allowed.includes(model);
}

/** Whether any target of the rule is reachable with the key. */
function isRuleReachableForVk(vk: VirtualKey | undefined, targets: { provider?: string; model?: string }[]): boolean {
	if (!vk || vk.allow_all_providers) return true;
	return targets.some((t) => {
		if (!t.provider) return true;
		const cfg = (vk.provider_configs ?? []).find((c) => c.provider === t.provider);
		if (!cfg) return false;
		if (!t.model) return true;
		if (cfg.blacklisted_models?.includes(t.model)) return false;
		const allowed = cfg.allowed_models ?? [];
		return allowed.includes("*") || allowed.includes(t.model);
	});
}

/**
 * Loads one direct model's datasheet parameters and reports its
 * reasoning-effort options (null when the datasheet has none). Rendered once
 * per selected direct model while opencode is active — the only agent whose
 * output has a variants checklist.
 */
function ReasoningEffortsLoader({ model, onEfforts }: { model: string; onEfforts: (model: string, efforts: string[] | null) => void }) {
	const { data } = useGetModelParametersQuery(model, { skip: !model });
	const efforts = useMemo(() => extractReasoningEfforts(data?.model_parameters), [data]);
	useEffect(() => {
		onEfforts(model, efforts);
		// efforts is a fresh array per params fetch; stringify for stability.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [model, JSON.stringify(efforts)]);
	return null;
}

export default function AgentSetupView() {
	const hasAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);

	// ── Wizard state ─────────────────────────────────────────────────
	const [agent, setAgent] = useState<AgentId>("opencode");
	const [virtualKeyId, setVirtualKeyId] = useState<string | null>(null);
	// Multi-select state: providers gatekeep models (pick providers first,
	// then their models). Routing rules are an independent track — they fire
	// via their CEL trigger and are unaffected by the provider picker.
	const [selectedProviders, setSelectedProviders] = useState<string[]>([]);
	const [selectedDirectIds, setSelectedDirectIds] = useState<string[]>([]);
	const [selectedRuleIds, setSelectedRuleIds] = useState<string[]>([]);
	// Agent-native per-entry metadata (opencode v2 keys only), datasheet-prefilled, user-editable.
	const [metadata, setMetadata] = useState<Record<string, AgentModelMetadata>>({});
	// Reasoning-effort options per direct model, from the datasheet (null = none found).
	const [effortsByModel, setEffortsByModel] = useState<Record<string, string[] | null>>({});
	// User-chosen default model for single-default agents (codex).
	const [defaultModelId, setDefaultModelId] = useState<string | null>(null);
	// Claude tier overrides: what the haiku alias (fast/cheap tasks) and the
	// sonnet alias (standard session model) resolve to. Null = follow the
	// first selection (single-model case → both the same model). Opus and
	// Fable tiers are optional extras — empty means the key is omitted.
	const [claudeHaikuId, setClaudeHaikuId] = useState<string | null>(null);
	const [claudeSonnetId, setClaudeSonnetId] = useState<string | null>(null);
	const [claudeOpusId, setClaudeOpusId] = useState<string | null>(null);
	const [claudeFableId, setClaudeFableId] = useState<string | null>(null);
	// Codex globals: datasheet-prefilled from the default model until the user edits.
	const [codexCtx, setCodexCtx] = useState("");
	const [codexCtxTouched, setCodexCtxTouched] = useState(false);
	const [codexMaxOut, setCodexMaxOut] = useState("");
	const [codexMaxOutTouched, setCodexMaxOutTouched] = useState(false);
	const [codexReasoningEffort, setCodexReasoningEffort] = useState<string>("medium");
	const [baseUrlOverride, setBaseUrlOverride] = useState("");
	// Credential env var, remembered per agent: opencode/codex default to
	// BIFROST_API_KEY, claude-code to ANTHROPIC_AUTH_TOKEN (the only auth
	// key Claude reads). A single shared state leaked the opencode default
	// into the claude fragment, emitting "BIFROST_API_KEY" which Claude
	// ignores.
	const [envVarByAgent, setEnvVarByAgent] = useState<Record<AgentId, string>>({
		opencode: DEFAULT_BIFROST_ENV_VAR,
		codex: DEFAULT_BIFROST_ENV_VAR,
		"claude-code": DEFAULT_CLAUDE_ENV_VAR,
	});
	const envVar = envVarByAgent[agent];
	const setEnvVar = (value: string) => setEnvVarByAgent((prev) => ({ ...prev, [agent]: value }));

	const variantsTouched = useRef<Set<string>>(new Set());
	// Per-entry fields the user edited by hand — datasheet refreshes must not clobber them (ref: mutated alongside setMetadata, no extra render).
	const metaTouched = useRef<Map<string, Set<keyof AgentModelMetadata>>>(new Map());

	// ── Virtual key picker (searchable, secret never rendered raw) ───
	const [vkSearch, setVkSearch] = useState("");
	const [vkOpen, setVkOpen] = useState(false);
	const debouncedVkSearch = useDebouncedValue(vkSearch, 250);
	const { data: vksData, isFetching: isFetchingVks } = useGetVirtualKeysQuery(
		{ limit: 50, search: debouncedVkSearch || undefined },
		{ skip: !hasAccess },
	);
	const activeVks = useMemo(() => vksData?.virtual_keys?.filter((vk) => vk.is_active) ?? [], [vksData]);
	const { data: vkDetail } = useGetVirtualKeyQuery(virtualKeyId ?? "", { skip: !virtualKeyId });
	// RTK Query keeps the last fetched detail cached when the query is skipped — gate on
	// virtualKeyId so Clear actually clears (otherwise the old VK keeps scoping the lists).
	const selectedVk = virtualKeyId ? vkDetail?.virtual_key : undefined;
	const vkOptions = useMemo(
		() => activeVks.map((vk) => ({ value: vk.id, label: vk.name || vk.id, description: vk.description })),
		[activeVks],
	);

	// ── Source data ──────────────────────────────────────────────────
	const vkSecret = selectedVk?.value || undefined;
	const { data: providers } = useGetProvidersQuery(undefined, { skip: !hasAccess });
	const { data: modelsData, isFetching: isFetchingModels } = useGetModelsQuery(
		{
			...(vkSecret ? { virtualKeyValue: vkSecret } : { unfiltered: true }),
			limit: 2000,
		},
		{ skip: !hasAccess || (!!virtualKeyId && !selectedVk) },
	);
	const { data: rulesData } = useGetRoutingRulesQuery({ limit: 200 }, { skip: !hasAccess });
	const { data: detailsData } = useGetModelDetailsQuery(
		{
			...(vkSecret ? { virtualKeyValue: vkSecret } : { unfiltered: true }),
			limit: 2000,
		},
		{ skip: !hasAccess || (!!virtualKeyId && !selectedVk) },
	);

	// Datasheet snapshot per direct model: limits, modalities, per-token costs.
	const datasheetByModel = useMemo(() => {
		const map = new Map<string, NonNullable<ModelSelectionItem["datasheet"]>>();
		for (const m of detailsData?.models ?? []) {
			const key = `${m.provider}/${m.name}`;
			const arch = m.architecture;
			// Operator precedence trap (fixed): `a ?? b !== undefined` parses as
			// `a ?? (b !== undefined)`, so a missing max_input_tokens with a
			// present context_length produced `true` instead of the number.
			const context = m.max_input_tokens ?? m.context_length;
			map.set(key, {
				...(context !== undefined ? { context } : {}),
				...(m.max_output_tokens !== undefined ? { output: m.max_output_tokens } : {}),
				...(arch?.input_modalities ? { inputModalities: arch.input_modalities } : {}),
				...(arch?.output_modalities ? { outputModalities: arch.output_modalities } : {}),
				...((m.input_cost_per_token ?? m.output_cost_per_token ?? m.cache_read_input_token_cost ?? m.cache_creation_input_token_cost) !==
				undefined
					? {
							costPerToken: {
								...(m.input_cost_per_token !== undefined ? { input: m.input_cost_per_token } : {}),
								...(m.output_cost_per_token !== undefined ? { output: m.output_cost_per_token } : {}),
								...(m.cache_read_input_token_cost !== undefined ? { cacheRead: m.cache_read_input_token_cost } : {}),
								...(m.cache_creation_input_token_cost !== undefined ? { cacheWrite: m.cache_creation_input_token_cost } : {}),
							},
						}
					: {}),
			});
		}
		return map;
	}, [detailsData]);

	// Provider allowlist for the picked VK (null = all providers allowed).
	const vkAllowedProviders = useMemo(() => allowedProvidersForVk(selectedVk), [selectedVk]);

	const providerOptions = useMemo<MultiSelectOption[]>(() => {
		const all = (providers ?? []).map((p) => p.name).sort();
		const allowed = vkAllowedProviders ? all.filter((name) => vkAllowedProviders.has(name)) : all;
		return allowed.map((name) => ({ value: name, label: name }));
	}, [providers, vkAllowedProviders]);

	// Drop providers that fall out of scope when the VK changes.
	useEffect(() => {
		if (!vkAllowedProviders) return;
		setSelectedProviders((prev) => prev.filter((p) => vkAllowedProviders.has(p)));
	}, [vkAllowedProviders]);

	// Rules whose CEL is a simple `model == "<trigger>"` — the only shape an
	// agent can fire through its model field.
	const triggerableRules = useMemo(() => {
		const rules = (rulesData?.rules ?? []).filter((r) => r.enabled);
		const withTrigger = rules
			.map((r) => ({ rule: r, trigger: extractModelTrigger(r.cel_expression) }))
			.filter((e): e is { rule: (typeof rules)[number]; trigger: string } => e.trigger !== null);
		if (!selectedVk) return withTrigger;
		return withTrigger.filter(({ rule }) => isRuleReachableForVk(selectedVk, rule.targets));
	}, [rulesData, selectedVk]);

	// Models picker: direct `provider/model` rows ONLY, strictly inside the
	// picked providers. Nothing is offered until at least one provider is
	// picked — browsing 1500+ models across all providers is the wrong flow.
	const modelOptions = useMemo<MultiSelectOption[]>(() => {
		if (selectedProviders.length === 0) return [];
		const inScopeProviders = new Set(selectedProviders);
		return (modelsData?.models ?? [])
			.filter((m) => inScopeProviders.has(m.provider))
			.filter((m) => isModelAllowedForVk(selectedVk, m.provider, m.name))
			.map((m) => ({
				value: `direct:${m.provider}/${m.name}`,
				label: `${m.provider}/${m.name}`,
				description: undefined as string | undefined,
			}));
	}, [modelsData, selectedProviders, selectedVk]);

	// Rules picker: every triggerable rule, independent of providers.
	const ruleOptions = useMemo<MultiSelectOption[]>(
		() =>
			triggerableRules.map(({ rule, trigger }) => ({
				value: `rule:${rule.id}`,
				label: trigger,
				description: `Rule → ${rule.targets.map((t) => [t.provider, t.model].filter(Boolean).join("/")).join(", ")}`,
			})),
		[triggerableRules],
	);

	// Drop selections that fall out of scope (provider unpick or VK change narrows the world).
	useEffect(() => {
		const valid = new Set(modelOptions.map((o) => o.value));
		setSelectedDirectIds((prev) => prev.filter((id) => valid.has(id)));
	}, [modelOptions]);
	useEffect(() => {
		const valid = new Set(ruleOptions.map((o) => o.value));
		setSelectedRuleIds((prev) => prev.filter((id) => valid.has(id)));
	}, [ruleOptions]);

	// Resolve the selections into entries with datasheet snapshots.
	const selectionItems = useMemo<ModelSelectionItem[]>(() => {
		const items: ModelSelectionItem[] = [];
		for (const id of selectedDirectIds) {
			const qualified = id.startsWith("direct:") ? id.slice("direct:".length) : id;
			const slash = qualified.indexOf("/");
			const provider = slash >= 0 ? qualified.slice(0, slash) : "";
			const model = slash >= 0 ? qualified.slice(slash + 1) : qualified;
			items.push({ id: qualified, kind: "direct", provider, model, datasheet: datasheetByModel.get(qualified) });
		}
		for (const id of selectedRuleIds) {
			const ruleId = id.slice("rule:".length);
			const found = triggerableRules.find(({ rule }) => rule.id === ruleId);
			if (!found) continue;
			items.push({
				id: found.trigger,
				kind: "rule",
				ruleId: found.rule.id,
				ruleTargets: found.rule.targets.map((t) => [t.provider, t.model].filter(Boolean).join("/")).join(", "),
			});
		}
		return items;
	}, [selectedDirectIds, selectedRuleIds, triggerableRules, datasheetByModel]);

	const selectionKeys = useMemo(() => selectionItems.map((i) => selectionKey(i)), [selectionItems]);

	// Datasheet-prefilled default for one entry (user edits win once stored).
	const makePrefill = useCallback(
		(item: ModelSelectionItem): AgentModelMetadata => {
			const ds = item.datasheet;
			return {
				limitContext: ds?.context !== undefined ? String(ds.context) : "",
				limitOutput: ds?.output !== undefined ? String(ds.output) : "",
				tools: true,
				inputModalities: ds?.inputModalities ?? [],
				outputModalities: ds?.outputModalities ?? [],
				variantEfforts: item.kind === "direct" ? (effortsByModel[item.id] ?? DEFAULT_REASONING_EFFORTS) : DEFAULT_REASONING_EFFORTS,
			};
		},
		[effortsByModel],
	);

	// Initialize metadata for new selections; datasheet-backed fields
	// (limits, modalities) refresh until the user edits them — the server
	// row can land after the prefill ran, so only user-touched entries win.
	useEffect(() => {
		setMetadata((prev) => {
			const next = { ...prev };
			let changed = false;
			const selected = new Set(selectionKeys);
			for (let i = 0; i < selectionItems.length; i++) {
				const key = selectionKeys[i];
				const fresh = makePrefill(selectionItems[i]);
				const existing = next[key];
				if (!existing) {
					next[key] = fresh;
					changed = true;
					continue;
				}
				const touched = metaTouched.current.get(key);
				const merged: AgentModelMetadata = {
					limitContext: touched?.has("limitContext") ? existing.limitContext : fresh.limitContext,
					limitOutput: touched?.has("limitOutput") ? existing.limitOutput : fresh.limitOutput,
					tools: touched?.has("tools") ? existing.tools : fresh.tools,
					inputModalities: touched?.has("inputModalities") ? existing.inputModalities : fresh.inputModalities,
					outputModalities: touched?.has("outputModalities") ? existing.outputModalities : fresh.outputModalities,
					variantEfforts: variantsTouched.current.has(key) ? existing.variantEfforts : fresh.variantEfforts,
				};
				if (JSON.stringify(existing) !== JSON.stringify(merged)) {
					next[key] = merged;
					changed = true;
				}
			}
			for (const k of Object.keys(next)) {
				if (!selected.has(k)) {
					delete next[k];
					variantsTouched.current.delete(k);
					metaTouched.current.delete(k);
					changed = true;
				}
			}
			return changed ? next : prev;
		});
	}, [selectionItems, selectionKeys, effortsByModel, makePrefill]);

	const handleEfforts = useCallback((model: string, efforts: string[] | null) => {
		setEffortsByModel((prev) => {
			const cur = prev[model] ?? null;
			if (JSON.stringify(cur) === JSON.stringify(efforts)) return prev;
			return { ...prev, [model]: efforts };
		});
	}, []);

	const updateMetadata = useCallback((key: string, patch: Partial<AgentModelMetadata>) => {
		setMetadata((prev) => ({ ...prev, [key]: { ...(prev[key] ?? EMPTY_AGENT_METADATA), ...patch } }));
		const set = metaTouched.current.get(key) ?? new Set<keyof AgentModelMetadata>();
		for (const field of Object.keys(patch) as (keyof AgentModelMetadata)[]) set.add(field);
		metaTouched.current.set(key, set);
	}, []);

	// Effective default model for single-default agents (user choice wins, else first selection).
	const effectiveDefaultId = useMemo(() => {
		const ids = selectionItems.map((i) => i.id);
		if (defaultModelId && ids.includes(defaultModelId)) return defaultModelId;
		return ids[0] ?? "";
	}, [defaultModelId, selectionItems]);

	useEffect(() => {
		if (!selectionItems.some((i) => i.id === defaultModelId)) setDefaultModelId(null);
		if (agent === "claude-code") return;
		// Codex has a single default radio; claude tier selects manage their own fallbacks below.
	}, [selectionItems, defaultModelId, agent]);

	// Claude tier resolution: explicit pick wins, else follow the single default radio, else first selection.
	// Optional tiers (opus/fable) stay empty unless the user picks one — empty means the key is omitted.
	const selectionIds = useMemo(() => selectionItems.map((i) => i.id), [selectionItems]);
	const effectiveClaudeHaikuId = claudeHaikuId && selectionIds.includes(claudeHaikuId) ? claudeHaikuId : effectiveDefaultId;
	const effectiveClaudeSonnetId = claudeSonnetId && selectionIds.includes(claudeSonnetId) ? claudeSonnetId : effectiveDefaultId;

	useEffect(() => {
		if (!selectionIds.some((id) => id === claudeHaikuId)) setClaudeHaikuId(null);
		if (!selectionIds.some((id) => id === claudeSonnetId)) setClaudeSonnetId(null);
		if (!selectionIds.some((id) => id === claudeOpusId)) setClaudeOpusId(null);
		if (!selectionIds.some((id) => id === claudeFableId)) setClaudeFableId(null);
	}, [selectionIds, claudeHaikuId, claudeSonnetId, claudeOpusId, claudeFableId]);

	// Codex globals follow the default model's datasheet until the user edits them.
	const defaultDatasheet = useMemo(() => {
		const found = selectionItems.find((i) => i.id === effectiveDefaultId);
		return found?.kind === "direct" ? found.datasheet : undefined;
	}, [selectionItems, effectiveDefaultId]);

	useEffect(() => {
		setCodexCtx("");
		setCodexCtxTouched(false);
		setCodexMaxOut("");
		setCodexMaxOutTouched(false);
	}, [effectiveDefaultId]);

	const effectiveCodexCtx = codexCtxTouched ? codexCtx : defaultDatasheet?.context !== undefined ? String(defaultDatasheet.context) : "";
	const effectiveCodexMaxOut = codexMaxOutTouched
		? codexMaxOut
		: defaultDatasheet?.output !== undefined
			? String(defaultDatasheet.output)
			: "";

	const activeAgent = AGENTS.find((a) => a.id === agent) ?? AGENTS[0];
	const baseUrl = (baseUrlOverride.trim() || defaultBaseUrl()).replace(/\/+$/, "");
	const resolvedEnvVar = envVar.trim() || defaultEnvVar(agent);

	// Opencode requires limit.context/output on every entry — block generation until filled.
	const opencodeMissingLimits = useMemo(() => {
		if (agent !== "opencode") return [];
		return selectionItems
			.filter((item) => {
				const meta = metadata[selectionKey(item)] ?? makePrefill(item);
				return (
					!meta.limitContext.trim() ||
					!meta.limitOutput.trim() ||
					Number.isNaN(Number(meta.limitContext)) ||
					Number.isNaN(Number(meta.limitOutput))
				);
			})
			.map((i) => i.id);
	}, [agent, selectionItems, metadata, makePrefill]);

	const builderModels: AgentOpencodeModelInput[] = useMemo(
		() =>
			selectionItems.map((item) => ({
				id: item.id,
				metadata: metadata[selectionKey(item)] ?? makePrefill(item),
				...(item.datasheet?.costPerToken ? { costPerToken: item.datasheet.costPerToken } : {}),
				...(item.kind === "rule" && item.ruleTargets ? { ruleTargets: item.ruleTargets } : {}),
			})),
		[selectionItems, metadata, makePrefill],
	);

	const builderInput: AgentConfigInput | null =
		baseUrl && builderModels.length > 0 && opencodeMissingLimits.length === 0
			? {
					baseUrl,
					models: builderModels,
					...(selectedVk ? { virtualKeyName: selectedVk.name } : {}),
					envVar: resolvedEnvVar,
					...(agent === "codex" || agent === "claude-code" ? { defaultModelId: effectiveDefaultId } : {}),
					...(agent === "claude-code"
						? {
								claudeHaikuModelId: effectiveClaudeHaikuId,
								claudeSonnetModelId: effectiveClaudeSonnetId,
								...(claudeOpusId && selectionIds.includes(claudeOpusId) ? { claudeOpusModelId: claudeOpusId } : {}),
								...(claudeFableId && selectionIds.includes(claudeFableId) ? { claudeFableModelId: claudeFableId } : {}),
							}
						: {}),
					...(agent === "codex"
						? {
								codexContextWindow: effectiveCodexCtx,
								codexMaxOutputTokens: effectiveCodexMaxOut,
								codexReasoningEffort,
							}
						: {}),
				}
			: null;

	const output = useMemo(
		() => (builderInput ? buildAgentConfig(agent, builderInput) : null),
		// builderInput is rebuilt every render; stringify the stable parts for memo.
		// eslint-disable-next-line react-hooks/exhaustive-deps
		[agent, JSON.stringify(builderInput)],
	);

	const canGenerate = !!output;
	const { copy, copied } = useCopyToClipboard({ successMessage: "Copied to clipboard" });

	const handleDownload = () => {
		if (!output) return;
		try {
			const blob = new Blob([output.config], { type: "text/plain" });
			const url = URL.createObjectURL(blob);
			const link = document.createElement("a");
			link.href = url;
			link.download = activeAgent.downloadFileName;
			document.body.appendChild(link);
			link.click();
			document.body.removeChild(link);
			URL.revokeObjectURL(url);
			toast.success(`Downloaded ${activeAgent.downloadFileName}`);
		} catch {
			toast.error("Failed to download config");
		}
	};

	const vkHint = useMemo(() => {
		if (!selectedVk) return null;
		if (selectedVk.allow_all_providers) return `${selectedVk.name}: all providers allowed`;
		const parts = (selectedVk.provider_configs ?? []).map((c) => {
			const models = c.allowed_models?.includes("*") ? "all models" : `${c.allowed_models?.length ?? 0} models`;
			const blocked = c.blacklisted_models?.length ? ` (${c.blacklisted_models.length} blocked)` : "";
			return `${c.provider} (${models}${blocked})`;
		});
		return `${selectedVk.name}: ${parts.join(", ") || "no providers"}`;
	}, [selectedVk]);

	if (!hasAccess) return <NoPermissionView entity="agent setup" />;

	const directItems = selectionItems.filter((i) => i.kind === "direct");

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-col gap-6 p-4">
			<PageTitle>Generate copy-ready model configs for opencode, Claude Code and Codex pointing at Bifrost</PageTitle>

			{/* Datasheet reasoning-effort loaders (opencode variants checklist). */}
			{agent === "opencode" && directItems.map((i) => <ReasoningEffortsLoader key={i.id} model={i.id} onEfforts={handleEfforts} />)}

			{/* ── Step 0: virtual key ─────────────────────────────────── */}
			<section className="flex flex-col gap-2">
				<div className="flex items-center gap-2 text-sm font-medium">
					<KeyRound className="size-4" />
					<span>Virtual key — the api key from the agent’s point of view (optional, pick at most one)</span>
				</div>
				<div className="flex flex-col gap-2 sm:flex-row sm:items-center">
					<SearchSelect
						async
						open={vkOpen}
						onOpenChange={setVkOpen}
						options={vkOptions}
						onSearchChange={setVkSearch}
						isSearching={isFetchingVks}
						isLoading={isFetchingVks && vkOptions.length === 0}
						onValueSelect={(option) => {
							setVirtualKeyId(option.value);
							setVkOpen(false);
						}}
						label={
							<Button
								type="button"
								variant="outline"
								className="h-9 w-full justify-start bg-transparent sm:w-80"
								data-testid="agent-setup-vk-select"
							>
								<KeyRound className="text-muted-foreground size-4" />
								<span className="truncate">{selectedVk?.name ?? "No virtual key (no api key in output)"}</span>
							</Button>
						}
						entryView={(option) => (
							<div className="flex min-w-0 flex-1 items-center gap-2">
								<div className="flex min-w-0 flex-col">
									<span className="truncate font-medium">{option.label}</span>
									{option.description && <span className="text-muted-foreground truncate text-xs">{option.description}</span>}
								</div>
								{virtualKeyId === option.value && <Check className="ml-auto size-4 text-green-600" />}
							</div>
						)}
						searchPlaceholder="Search virtual keys..."
						emptyMessage="No active virtual keys found."
						align="start"
						className="w-full sm:w-80"
						contentClassName="w-[var(--radix-popover-trigger-width)]"
					/>
					{virtualKeyId && (
						<Button type="button" variant="ghost" size="sm" onClick={() => setVirtualKeyId(null)} data-testid="agent-setup-vk-clear">
							Clear
						</Button>
					)}
				</div>
				{vkHint && (
					<p className="text-muted-foreground text-xs">{vkHint} — providers, models and rules below only show what this key can use.</p>
				)}
				<p className="text-muted-foreground text-xs">
					{selectedVk
						? "The key secret is never written into the config — set it once via the shell exports below."
						: "No virtual key picked: the generated config carries no credential at all. Pick one to add env-var auth."}
				</p>
			</section>

			{/* ── Step 1: agent ───────────────────────────────────────── */}
			<section className="flex flex-col gap-2">
				<div className="text-sm font-medium">Agent</div>
				<Tabs value={agent} onValueChange={(value) => setAgent(value as AgentId)}>
					<TabsList className="flex w-full flex-row justify-start rounded-sm" data-testid="agent-setup-agent-tabs">
						{AGENTS.map((a) => (
							<TabsTrigger key={a.id} value={a.id} className="flex flex-none shrink-0 gap-2" data-testid={`agent-setup-agent-${a.id}`}>
								<img src={a.logoSrc} alt="" aria-hidden="true" className="size-4 rounded-[2px]" />
								{a.label}
							</TabsTrigger>
						))}
					</TabsList>
				</Tabs>
				<p className="text-muted-foreground font-mono text-xs">Merge into: {activeAgent.configPathNote}</p>
				{selectedVk && (
					<p className="font-mono text-xs">
						<span className="text-muted-foreground">Then run: </span>
						<code>export {resolvedEnvVar}="&lt;paste-your-virtual-key-value&gt;"</code>
					</p>
				)}
			</section>

			{/* ── Step 2: providers → models, plus independent rules ─── */}
			<section className="flex flex-col gap-3">
				<div className="text-sm font-medium">Providers → models (pick providers first, then their models)</div>
				<div className="flex flex-col gap-1.5">
					<Label>Providers</Label>
					<MultiSelect
						options={providerOptions}
						defaultValue={selectedProviders}
						resetOnDefaultValueChange
						onValueChange={(values) => setSelectedProviders(values)}
						placeholder={selectedVk ? "Select allowed providers" : "Select providers"}
						emptyIndicator={selectedVk ? "No allowed providers for this key." : "No providers found."}
						maxCount={3}
						data-testid="agent-setup-provider-select"
					/>
					{selectedVk && providerOptions.length === 0 && (
						<p className="text-muted-foreground text-xs">This key allows no providers — nothing below can be generated for it.</p>
					)}
				</div>
				<div className="flex flex-col gap-1.5">
					<Label>Models</Label>
					<MultiSelect
						options={modelOptions}
						defaultValue={selectedDirectIds}
						resetOnDefaultValueChange
						onValueChange={(values) => setSelectedDirectIds(values)}
						placeholder={isFetchingModels ? "Loading models…" : selectedProviders.length > 0 ? "Select models" : "Pick providers first"}
						emptyIndicator={selectedProviders.length > 0 ? "No allowed models for these providers." : "Pick at least one provider above."}
						maxCount={3}
						data-testid="agent-setup-model-select"
						disabled={selectedProviders.length === 0}
					/>
					<p className="text-muted-foreground text-xs">
						{isFetchingModels
							? "Loading…"
							: selectedProviders.length > 0
								? `${modelOptions.length} models in the picked providers`
								: "Models unlock once providers are picked"}{" "}
						· direct entries are qualified <code className="font-mono">provider/model</code>.
					</p>
				</div>
			</section>

			{/* ── Step 2b: routing rules (independent of providers) ────── */}
			<section className="flex flex-col gap-3">
				<div className="text-sm font-medium">Routing rules (optional, independent of providers)</div>
				<div className="flex flex-col gap-1.5">
					<Label>Rule triggers</Label>
					<MultiSelect
						options={ruleOptions}
						defaultValue={selectedRuleIds}
						resetOnDefaultValueChange
						onValueChange={(values) => setSelectedRuleIds(values)}
						placeholder={ruleOptions.length > 0 ? "Select rule triggers" : "No triggerable rules"}
						emptyIndicator="No triggerable rules match."
						maxCount={3}
						data-testid="agent-setup-rule-select"
					/>
					<p className="text-muted-foreground text-xs">
						{ruleOptions.length} triggerable rules · each fires via its <code className="font-mono">model == &quot;trigger&quot;</code> CEL
						and notes its targets. Not affected by the provider picker.
					</p>
				</div>
			</section>

			{/* ── Step 3: agent-native metadata ───────────────────────── */}
			{agent === "opencode" && selectionItems.length > 0 && (
				<section className="flex flex-col gap-3">
					<div className="text-sm font-medium">Model metadata (opencode keys, datasheet-prefilled, editable)</div>
					{selectionItems.map((item) => {
						const key = selectionKey(item);
						const meta = metadata[key] ?? makePrefill(item);
						const availableEfforts =
							item.kind === "direct" ? (effortsByModel[item.id] ?? DEFAULT_REASONING_EFFORTS) : DEFAULT_REASONING_EFFORTS;
						const ds = item.datasheet;
						return (
							<div key={key} className="flex flex-col gap-2.5 rounded-sm border p-3">
								<div className="font-mono text-xs">
									{item.id}{" "}
									{item.kind === "rule" && (
										<span className="text-muted-foreground">→ {item.ruleTargets} (no datasheet — fill manually)</span>
									)}
									{item.kind === "direct" && !ds && <span className="text-muted-foreground">(no datasheet — fill manually)</span>}
								</div>
								<div className="grid gap-3 sm:grid-cols-2">
									<div className="flex flex-col gap-1.5">
										<Label>
											limit.context <span className="text-muted-foreground">(required)</span>
										</Label>
										<Input
											value={meta.limitContext}
											onChange={(e) => updateMetadata(key, { limitContext: e.target.value })}
											placeholder={ds?.context !== undefined ? String(ds.context) : "e.g. 200000"}
											className="font-mono"
											inputMode="numeric"
											data-testid={`agent-setup-meta-${item.id}-limit-context`}
										/>
									</div>
									<div className="flex flex-col gap-1.5">
										<Label>
											limit.output <span className="text-muted-foreground">(required)</span>
										</Label>
										<Input
											value={meta.limitOutput}
											onChange={(e) => updateMetadata(key, { limitOutput: e.target.value })}
											placeholder={ds?.output !== undefined ? String(ds.output) : "e.g. 32000"}
											className="font-mono"
											inputMode="numeric"
											data-testid={`agent-setup-meta-${item.id}-limit-output`}
										/>
									</div>
									<div className="flex flex-col gap-1.5">
										<Label>capabilities.input (comma-separated)</Label>
										<ModalityInput
											committed={meta.inputModalities.join(", ")}
											onCommit={(raw) => updateMetadata(key, { inputModalities: splitModalities(raw) })}
											testId={`agent-setup-meta-${item.id}-input`}
											placeholder="text, image"
										/>
										<p className="text-muted-foreground text-[11px]">Known values: {OPENCODE_MODALITIES.join(", ")}</p>
									</div>
									<div className="flex flex-col gap-1.5">
										<Label>capabilities.output (comma-separated)</Label>
										<ModalityInput
											committed={meta.outputModalities.join(", ")}
											onCommit={(raw) => updateMetadata(key, { outputModalities: splitModalities(raw) })}
											testId={`agent-setup-meta-${item.id}-output`}
											placeholder="text"
										/>
									</div>
								</div>
								<label className="flex cursor-pointer items-center gap-2 text-sm">
									<Checkbox
										checked={meta.tools}
										onCheckedChange={(v) => updateMetadata(key, { tools: v === true })}
										data-testid={`agent-setup-meta-${item.id}-tools`}
									/>
									<span>
										capabilities.tools <span className="text-muted-foreground">(tool calling; uncheck to disable)</span>
									</span>
								</label>
								<div className="flex flex-col gap-1.5">
									<Label>
										variants reasoning efforts{" "}
										<span className="text-muted-foreground">
											({item.kind === "direct" && effortsByModel[item.id] ? "from datasheet" : "defaults"} — uncheck to omit)
										</span>
									</Label>
									<div className="flex flex-wrap gap-3">
										{availableEfforts.map((effort) => (
											<label key={effort} className="flex cursor-pointer items-center gap-1.5 font-mono text-xs">
												<Checkbox
													checked={meta.variantEfforts.includes(effort)}
													onCheckedChange={(v) => {
														variantsTouched.current.add(key);
														const next = v === true ? [...meta.variantEfforts, effort] : meta.variantEfforts.filter((e) => e !== effort);
														updateMetadata(key, { variantEfforts: next });
													}}
													data-testid={`agent-setup-variant-${item.id}-${effort}`}
												/>
												{effort}
											</label>
										))}
									</div>
								</div>
								{ds?.costPerToken && Object.keys(ds.costPerToken).length > 0 && (
									<p className="text-muted-foreground text-[11px]">
										cost (per-1M, auto):{" "}
										{[
											ds.costPerToken.input !== undefined ? `input ${formatPerMillion(ds.costPerToken.input)}` : null,
											ds.costPerToken.output !== undefined ? `output ${formatPerMillion(ds.costPerToken.output)}` : null,
											ds.costPerToken.cacheRead !== undefined ? `cache_read ${formatPerMillion(ds.costPerToken.cacheRead)}` : null,
											ds.costPerToken.cacheWrite !== undefined ? `cache_write ${formatPerMillion(ds.costPerToken.cacheWrite)}` : null,
										]
											.filter(Boolean)
											.join(" · ")}
									</p>
								)}
							</div>
						);
					})}
					{opencodeMissingLimits.length > 0 && (
						<p className="text-xs text-amber-600">
							Fill limit.context and limit.output for: {opencodeMissingLimits.join(", ")} — opencode requires both on every model entry.
						</p>
					)}
				</section>
			)}

			{/* ── Step 3b: default model + codex globals ─────────────── */}
			{(agent === "codex" || agent === "claude-code") && selectionItems.length > 0 && (
				<section className="flex flex-col gap-3">
					<div className="text-sm font-medium">Default model</div>
					{agent === "codex" ? (
						<div className="flex flex-col gap-1.5" data-testid="agent-setup-default-model">
							{selectionItems.map((item) => (
								<label key={item.id} className="flex cursor-pointer items-center gap-2 font-mono text-xs">
									<input
										type="radio"
										name="agent-setup-default-model"
										checked={effectiveDefaultId === item.id}
										onChange={() => setDefaultModelId(item.id)}
										className="accent-primary size-4"
										data-testid={`agent-setup-default-model-${item.id}`}
									/>
									{item.id}
									{item.kind === "rule" && <span className="text-muted-foreground">→ {item.ruleTargets}</span>}
								</label>
							))}
						</div>
					) : (
						<div className="grid gap-3 sm:grid-cols-2">
							<div className="flex flex-col gap-1.5">
								<Label>
									Haiku-tier model <span className="text-muted-foreground">(required)</span>
								</Label>
								<Select value={effectiveClaudeHaikuId} onValueChange={setClaudeHaikuId}>
									<SelectTrigger className="font-mono" data-testid="agent-setup-claude-haiku">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{selectionItems.map((item) => (
											<SelectItem key={item.id} value={item.id} className="font-mono">
												{item.id}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-[11px]">
									What the <code className="font-mono">haiku</code> alias resolves to → ANTHROPIC_DEFAULT_HAIKU_MODEL
								</p>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label>
									Sonnet-tier model <span className="text-muted-foreground">(required)</span>
								</Label>
								<Select value={effectiveClaudeSonnetId} onValueChange={setClaudeSonnetId}>
									<SelectTrigger className="font-mono" data-testid="agent-setup-claude-sonnet">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{selectionItems.map((item) => (
											<SelectItem key={item.id} value={item.id} className="font-mono">
												{item.id}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-[11px]">
									What the <code className="font-mono">sonnet</code> alias and session default resolve to → ANTHROPIC_DEFAULT_SONNET_MODEL +
									top-level <code className="font-mono">model</code>
								</p>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label>
									Opus-tier model <span className="text-muted-foreground">(optional)</span>
								</Label>
								<Select
									value={claudeOpusId && selectionIds.includes(claudeOpusId) ? claudeOpusId : "__none__"}
									onValueChange={(v) => setClaudeOpusId(v === "__none__" ? null : v)}
								>
									<SelectTrigger className="font-mono" data-testid="agent-setup-claude-opus">
										<SelectValue placeholder="Not set (key omitted)" />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="__none__">Not set (key omitted)</SelectItem>
										{selectionItems.map((item) => (
											<SelectItem key={item.id} value={item.id} className="font-mono">
												{item.id}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-[11px]">
									What the <code className="font-mono">opus</code> alias (heaviest tasks) resolves to → ANTHROPIC_DEFAULT_OPUS_MODEL
								</p>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label>
									Fable-tier model <span className="text-muted-foreground">(optional)</span>
								</Label>
								<Select
									value={claudeFableId && selectionIds.includes(claudeFableId) ? claudeFableId : "__none__"}
									onValueChange={(v) => setClaudeFableId(v === "__none__" ? null : v)}
								>
									<SelectTrigger className="font-mono" data-testid="agent-setup-claude-fable">
										<SelectValue placeholder="Not set (key omitted)" />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="__none__">Not set (key omitted)</SelectItem>
										{selectionItems.map((item) => (
											<SelectItem key={item.id} value={item.id} className="font-mono">
												{item.id}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-[11px]">
									What the <code className="font-mono">fable</code> alias resolves to → ANTHROPIC_DEFAULT_FABLE_MODEL
								</p>
							</div>
						</div>
					)}
					{agent === "codex" && (
						<div className="grid gap-3 sm:grid-cols-3">
							<div className="flex flex-col gap-1.5">
								<Label>model_context_window</Label>
								<Input
									value={effectiveCodexCtx}
									onChange={(e) => {
										setCodexCtx(e.target.value);
										setCodexCtxTouched(true);
									}}
									placeholder={defaultDatasheet?.context !== undefined ? String(defaultDatasheet.context) : "from default model"}
									className="font-mono"
									inputMode="numeric"
									data-testid="agent-setup-codex-context-window"
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label>model_max_output_tokens</Label>
								<Input
									value={effectiveCodexMaxOut}
									onChange={(e) => {
										setCodexMaxOut(e.target.value);
										setCodexMaxOutTouched(true);
									}}
									placeholder={defaultDatasheet?.output !== undefined ? String(defaultDatasheet.output) : "from default model"}
									className="font-mono"
									inputMode="numeric"
									data-testid="agent-setup-codex-max-output"
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								<Label>model_reasoning_effort</Label>
								<Select value={codexReasoningEffort} onValueChange={setCodexReasoningEffort}>
									<SelectTrigger className="font-mono" data-testid="agent-setup-codex-reasoning">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{CODEX_REASONING_EFFORTS.map((e) => (
											<SelectItem key={e} value={e} className="font-mono">
												{e}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
						</div>
					)}
				</section>
			)}

			{/* ── Connection details ──────────────────────────────────── */}
			<section className="grid gap-3 sm:grid-cols-2">
				<div className="flex flex-col gap-1.5">
					<Label>Bifrost base URL</Label>
					<Input
						value={baseUrlOverride}
						onChange={(e) => setBaseUrlOverride(e.target.value)}
						placeholder={defaultBaseUrl() || "http://127.0.0.1:8080"}
						className="font-mono"
						data-testid="agent-setup-base-url"
					/>
				</div>
				<div className="flex flex-col gap-1.5">
					<Label>Credential env var {selectedVk ? "" : "(only used with a virtual key)"}</Label>
					<Input
						value={envVar}
						onChange={(e) => setEnvVar(e.target.value)}
						placeholder={defaultEnvVar(agent)}
						className="font-mono"
						disabled={!selectedVk}
						data-testid="agent-setup-env-var"
					/>
				</div>
			</section>

			{/* ── Output: preview + copy + download ───────────────────── */}
			<section className="flex flex-col gap-3">
				<div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
					<div className="text-sm font-medium">Generated config</div>
					<div className="flex items-center gap-2">
						<Button
							variant="outline"
							size="sm"
							onClick={() => output && void copy(output.config)}
							disabled={!canGenerate}
							data-testid="agent-setup-copy-config"
						>
							{copied ? <Check className="mr-1.5 h-4 w-4 text-green-500" /> : <Copy className="mr-1.5 h-4 w-4" />}
							{copied ? "Copied" : "Copy config"}
						</Button>
						{selectedVk && (
							<Button
								variant="outline"
								size="sm"
								onClick={() => output && output.shellExports && void copy(output.shellExports)}
								disabled={!canGenerate}
								data-testid="agent-setup-copy-shell"
							>
								<Copy className="mr-1.5 h-4 w-4" />
								Copy shell exports
							</Button>
						)}
						<Button size="sm" onClick={handleDownload} disabled={!canGenerate} data-testid="agent-setup-download">
							<Download className="mr-1.5 h-4 w-4" />
							Download {activeAgent.downloadFileName}
						</Button>
					</div>
				</div>

				{output ? (
					<>
						<div className="bg-card text-card-foreground rounded-lg border shadow-sm">
							<div className="bg-muted/40 flex items-center justify-between border-b px-4 py-2.5">
								<div className="text-muted-foreground flex items-center gap-2 font-mono text-xs">
									{activeAgent.label} · {selectionItems.length} model{selectionItems.length === 1 ? "" : "s"}
									{selectedVk ? ` · ${selectedVk.name}` : " · no api key"}
								</div>
								{selectedVk && (
									<Button
										variant="ghost"
										size="sm"
										onClick={() => output.shellExports && void copy(output.shellExports)}
										data-testid="agent-setup-copy-shell-inline"
									>
										<Copy className="mr-1.5 h-3.5 w-3.5" />
										Shell exports
									</Button>
								)}
							</div>
							<CodeEditor
								className="w-full font-mono text-sm"
								code={output.config}
								lang={activeAgent.previewLang}
								readonly={true}
								wrap={true}
								height={420}
								options={{
									collapsibleBlocks: true,
									lineNumbers: "on",
									scrollBeyondLastLine: false,
									showVerticalScrollbar: true,
									showHorizontalScrollbar: true,
								}}
							/>
						</div>
						{output.sections
							.filter((s) => s.content !== output.config)
							.map((section) => (
								<div key={section.label} className="bg-card text-card-foreground rounded-lg border shadow-sm">
									<div className="bg-muted/40 flex items-center justify-between border-b px-4 py-2.5">
										<div className="text-muted-foreground font-mono text-xs">{section.label}</div>
										<Button
											variant="ghost"
											size="sm"
											onClick={() => void copy(section.content)}
											data-testid={`agent-setup-copy-section-${section.label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`}
										>
											<Copy className="mr-1.5 h-3.5 w-3.5" />
											Copy
										</Button>
									</div>
									<CodeEditor
										className="w-full font-mono text-sm"
										code={section.content}
										lang={section.label.toLowerCase().includes("toml") ? "toml" : "shell"}
										readonly={true}
										wrap={true}
										height={140}
										options={{
											lineNumbers: "on",
											scrollBeyondLastLine: false,
											showVerticalScrollbar: true,
											showHorizontalScrollbar: true,
										}}
									/>
								</div>
							))}
					</>
				) : (
					<div className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
						{opencodeMissingLimits.length > 0
							? `Fill limit.context and limit.output for: ${opencodeMissingLimits.join(", ")}.`
							: `Select one or more models or routing-rule triggers above to generate the ${activeAgent.label} config.`}
					</div>
				)}
			</section>
		</div>
	);
}

/**
 * Free-text input for comma-separated modalities. The parent stores the
 * parsed array, so binding the raw keystrokes to it would normalize away
 * intermediate states (a just-typed comma or space vanishes on the next
 * render). Instead the draft lives here while focused and is committed
 * (parsed) on blur; external prefill updates flow through while unfocused.
 */
function ModalityInput({
	committed,
	onCommit,
	testId,
	placeholder,
}: {
	committed: string;
	onCommit: (raw: string) => void;
	testId: string;
	placeholder: string;
}) {
	const [draft, setDraft] = useState(committed);
	const [focused, setFocused] = useState(false);
	useEffect(() => {
		if (!focused) setDraft(committed);
	}, [committed, focused]);
	return (
		<Input
			value={focused ? draft : committed}
			onFocus={() => {
				setDraft(committed);
				setFocused(true);
			}}
			onChange={(e) => setDraft(e.target.value)}
			onBlur={() => {
				setFocused(false);
				onCommit(draft);
			}}
			onKeyDown={(e) => {
				if (e.key === "Enter") (e.target as HTMLInputElement).blur();
			}}
			placeholder={placeholder}
			className="font-mono"
			data-testid={testId}
		/>
	);
}

/** Stable key for per-entry metadata state (direct and rule ids live in different namespaces). */
function selectionKey(item: ModelSelectionItem): string {
	return item.kind === "rule" ? `rule:${item.ruleId}` : `direct:${item.id}`;
}

/** Parse a comma-separated modalities input into a clean list. */
function splitModalities(raw: string): string[] {
	return raw
		.split(",")
		.map((s) => s.trim().toLowerCase())
		.filter(Boolean);
}

/** Format a per-token cost as a per-1M figure for display. */
function formatPerMillion(perToken: number): string {
	const v = perToken * 1_000_000;
	return Number.isInteger(v) ? String(v) : v.toPrecision(4);
}