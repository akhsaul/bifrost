import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MultiSelect, type MultiSelectOption } from "@/components/ui/multiSelect";
import { SearchSelect } from "@/components/ui/searchSelect";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import ModelParameters from "@/components/ui/custom/modelParameters";
import { NoPermissionView } from "@/components/noPermissionView";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
	useGetModelDetailsQuery,
	useGetModelsQuery,
	useGetProvidersQuery,
	useGetVirtualKeyQuery,
	useGetVirtualKeysQuery,
} from "@/lib/store";
import { useGetRoutingRulesQuery } from "@/lib/store/apis/routingRulesApi";
import type { VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Check, Copy, Download, KeyRound, Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { buildAgentConfig, type AgentConfigInput } from "./agentConfigBuilders";
import {
	AGENT_PLATFORMS,
	AGENTS,
	DEFAULT_BIFROST_ENV_VAR,
	DEFAULT_CLAUDE_ENV_VAR,
	EMPTY_MANUAL_METADATA,
	extractModelTrigger,
	type AgentId,
	type AgentPlatform,
	type ManualModelMetadata,
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

export default function AgentSetupView() {
	const hasAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);

	// ── Wizard state ─────────────────────────────────────────────────
	const [agent, setAgent] = useState<AgentId>("opencode");
	const [platform, setPlatform] = useState<AgentPlatform>("linux");
	const [virtualKeyId, setVirtualKeyId] = useState<string | null>(null);
	// Multi-select state: many providers, many models/rules, at most one VK.
	const [selectedProviders, setSelectedProviders] = useState<string[]>([]);
	const [selectedModelIds, setSelectedModelIds] = useState<string[]>([]);
	// Per-rule manual metadata (rules have no datasheet; targets may differ).
	const [ruleMetadata, setRuleMetadata] = useState<Record<string, ManualModelMetadata>>({});
	const [baseUrlOverride, setBaseUrlOverride] = useState("");
	const [envVar, setEnvVar] = useState(DEFAULT_BIFROST_ENV_VAR);
	const [datasheetParams, setDatasheetParams] = useState<Record<string, Record<string, any>>>({});
	const [extraParams, setExtraParams] = useState<{ key: string; value: string }[]>([]);

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
	const selectedVk = vkDetail?.virtual_key;
	const vkOptions = useMemo(
		() => activeVks.map((vk) => ({ value: vk.id, label: vk.name || vk.id, description: vk.description })),
		[activeVks],
	);

	// ── Source data ──────────────────────────────────────────────────
	// A picked VK scopes both listings server-side via its secret on x-bf-vk
	// (the list endpoint already returns provider_configs incl. the value, so
	// no separate reveal step is needed). No VK → unfiltered full catalog.
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
	// agent can fire through its model field. With a VK, only rules with at
	// least one reachable target are offered (the rules list itself is not
	// VK-scoped server-side, so this is approximated via the allowlist).
	// Without a VK every triggerable rule is listed.
	const triggerableRules = useMemo(() => {
		const rules = (rulesData?.rules ?? []).filter((r) => r.enabled);
		const withTrigger = rules
			.map((r) => ({ rule: r, trigger: extractModelTrigger(r.cel_expression) }))
			.filter((e): e is { rule: (typeof rules)[number]; trigger: string } => e.trigger !== null);
		if (!selectedVk) return withTrigger;
		return withTrigger.filter(({ rule }) => isRuleReachableForVk(selectedVk, rule.targets));
	}, [rulesData, selectedVk]);

	// Unified Models picker: direct `provider/model` rows (VK-filtered when a
	// key is picked — by the server via x-bf-vk, which is exact) plus every
	// triggerable rule as `trigger → targets`.
	const modelOptions = useMemo<MultiSelectOption[]>(() => {
		const inScopeProviders = selectedProviders.length > 0 ? new Set(selectedProviders) : null;
		const direct = (modelsData?.models ?? [])
			.filter((m) => !inScopeProviders || inScopeProviders.has(m.provider))
			// Belt-and-braces: the server already scoped VK listings, but the
			// client-side allowlist pass keeps the picker honest while the
			// scoped query is in flight.
			.filter((m) => isModelAllowedForVk(selectedVk, m.provider, m.name))
			.map((m) => ({
				value: `direct:${m.provider}/${m.name}`,
				label: `${m.provider}/${m.name}`,
				description: undefined as string | undefined,
			}));
		const rules = triggerableRules.map(({ rule, trigger }) => ({
			value: `rule:${rule.id}`,
			label: trigger,
			description: `Rule → ${rule.targets.map((t) => [t.provider, t.model].filter(Boolean).join("/")).join(", ")}`,
		}));
		return [...direct, ...rules];
	}, [modelsData, selectedProviders, selectedVk, triggerableRules]);

	// Drop selections that fall out of scope (VK change narrows the world).
	useEffect(() => {
		const valid = new Set(modelOptions.map((o) => o.value));
		setSelectedModelIds((prev) => prev.filter((id) => valid.has(id)));
	}, [modelOptions]);

	// Datasheet limits per direct model (feeds opencode `limit`).
	const limitsByModel = useMemo(() => {
		const map = new Map<string, { context?: number; output?: number }>();
		for (const m of detailsData?.models ?? []) {
			const context = m.max_input_tokens ?? m.context_length;
			const output = m.max_output_tokens;
			if (context === undefined && output === undefined) continue;
			map.set(`${m.provider}/${m.name}`, {
				...(context !== undefined ? { context } : {}),
				...(output !== undefined ? { output } : {}),
			});
		}
		return map;
	}, [detailsData]);

	// Resolve the selection into builder entries. Rules carry their own
	// manual metadata; empty values are omitted from the output.
	const selectionItems = useMemo<ModelSelectionItem[]>(() => {
		const items: ModelSelectionItem[] = [];
		for (const id of selectedModelIds) {
			if (id.startsWith("rule:")) {
				const ruleId = id.slice("rule:".length);
				const found = triggerableRules.find(({ rule }) => rule.id === ruleId);
				if (!found) continue;
				items.push({
					id: found.trigger,
					kind: "rule",
					ruleId: found.rule.id,
					ruleTargets: found.rule.targets.map((t) => [t.provider, t.model].filter(Boolean).join("/")).join(", "),
					manualMetadata: ruleMetadata[found.rule.id],
				});
				continue;
			}
			const qualified = id.startsWith("direct:") ? id.slice("direct:".length) : id;
			const slash = qualified.indexOf("/");
			const provider = slash >= 0 ? qualified.slice(0, slash) : "";
			const model = slash >= 0 ? qualified.slice(slash + 1) : qualified;
			items.push({ id: qualified, kind: "direct", provider, model, limit: limitsByModel.get(qualified) });
		}
		return items;
	}, [selectedModelIds, triggerableRules, limitsByModel, ruleMetadata]);

	const activeAgent = AGENTS.find((a) => a.id === agent) ?? AGENTS[0];
	const baseUrl = (baseUrlOverride.trim() || defaultBaseUrl()).replace(/\/+$/, "");

	const extraParamsRecord = useMemo(() => {
		const record: Record<string, string> = {};
		for (const { key, value } of extraParams) {
			if (key.trim()) record[key.trim()] = value;
		}
		return record;
	}, [extraParams]);

	const builderInput: AgentConfigInput | null =
		baseUrl && selectionItems.length > 0
			? {
					baseUrl,
					models: selectionItems,
					...(selectedVk ? { virtualKeyName: selectedVk.name } : {}),
					extraParams: { ...flattenFirstParams(datasheetParams, selectedModelIds), ...extraParamsRecord },
					envVar: envVar.trim() || defaultEnvVar(agent),
				}
			: null;

	const output = useMemo(
		() => (builderInput ? buildAgentConfig(agent, builderInput, platform) : null),
		// builderInput is rebuilt every render; stringify the stable parts for memo.
		// eslint-disable-next-line react-hooks/exhaustive-deps
		[agent, platform, JSON.stringify(builderInput)],
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

	// First direct selection drives the datasheet-first parameters panel.
	const firstDirect = selectionItems.find((i) => i.kind === "direct");
	const selectedRuleItems = selectionItems.filter((i) => i.kind === "rule");

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-col gap-6 p-4">
			<PageTitle>Generate copy-ready model configs for opencode, Claude Code and Codex pointing at Bifrost</PageTitle>

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

			{/* ── Step 1: agent + platform ────────────────────────────── */}
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
				<div className="grid gap-2 sm:grid-cols-3" data-testid="agent-setup-platform">
					{AGENT_PLATFORMS.map((p) => (
						<button
							key={p}
							type="button"
							onClick={() => setPlatform(p)}
							className={cn(
								"flex h-9 items-center gap-2 rounded-sm border px-3 py-2 text-left text-sm capitalize transition-colors hover:bg-accent",
								platform === p && "border-primary bg-primary/5",
							)}
							aria-pressed={platform === p}
							data-testid={`agent-setup-platform-${p}`}
						>
							<span className="font-medium">{p === "macos" ? "macOS" : p === "windows" ? "Windows" : "Linux"}</span>
							{platform === p && <Check className="ml-auto size-4 text-green-600" />}
						</button>
					))}
				</div>
				<p className="text-muted-foreground font-mono text-xs">Merge into: {activeAgent.configPath[platform]}</p>
			</section>

			{/* ── Step 2: providers + models ──────────────────────────── */}
			<section className="flex flex-col gap-3">
				<div className="text-sm font-medium">Providers & models (multi-select)</div>
				<div className="flex flex-col gap-1.5">
					<Label>Providers</Label>
					<MultiSelect
						options={providerOptions}
						defaultValue={selectedProviders}
						resetOnDefaultValueChange
						onValueChange={(values) => setSelectedProviders(values)}
						placeholder={selectedVk ? "Select allowed providers" : "Select providers (all listed)"}
						emptyIndicator={selectedVk ? "No allowed providers for this key." : "No providers found."}
						maxCount={3}
						data-testid="agent-setup-provider-select"
					/>
					{selectedVk && providerOptions.length === 0 && (
						<p className="text-muted-foreground text-xs">This key allows no providers — nothing below can be generated for it.</p>
					)}
				</div>
				<div className="flex flex-col gap-1.5">
					<Label>Models & routing-rule triggers</Label>
					<MultiSelect
						options={modelOptions}
						defaultValue={selectedModelIds}
						resetOnDefaultValueChange
						onValueChange={(values) => setSelectedModelIds(values)}
						placeholder={
							isFetchingModels
								? "Loading models…"
								: selectedProviders.length > 0
									? "Select models and rule triggers"
									: "Select models and rule triggers (all providers)"
						}
						emptyIndicator="No models or rule triggers match."
						maxCount={3}
						data-testid="agent-setup-model-select"
					/>
					<p className="text-muted-foreground text-xs">
						{isFetchingModels ? "Loading…" : `${modelOptions.length} options`} · direct entries are qualified{" "}
						<code className="font-mono">provider/model</code>; rule entries fire via their trigger and note their targets.
						{selectedProviders.length === 0 && " Narrow by provider to browse faster."}
					</p>
				</div>
			</section>

			{/* ── Step 3: parameters ──────────────────────────────────── */}
			<section className="flex flex-col gap-3">
				<div className="text-sm font-medium">Parameters</div>
				{firstDirect ? (
					<div className="flex flex-col gap-1.5">
						<Label className="text-muted-foreground text-xs">
							Datasheet parameters for <code className="font-mono">{firstDirect.id}</code> (first direct selection)
						</Label>
						<ModelParameters
							model={firstDirect.model ?? firstDirect.id}
							config={datasheetParams[firstDirect.id] ?? {}}
							onChange={(next) => setDatasheetParams((prev) => ({ ...prev, [firstDirect.id]: next }))}
						/>
					</div>
				) : (
					<p className="text-muted-foreground text-xs">Select a direct model above for datasheet-first parameters.</p>
				)}

				{selectedRuleItems.length > 0 && (
					<div className="flex flex-col gap-3 rounded-sm border p-3">
						<p className="text-muted-foreground text-xs">
							A rule can route to targets with different context windows, so set metadata per rule manually (empty values are omitted).
						</p>
						{selectedRuleItems.map((item) => (
							<div key={item.ruleId} className="flex flex-col gap-2 rounded-sm border p-2.5">
								<div className="font-mono text-xs">
									{item.id} <span className="text-muted-foreground">→ {item.ruleTargets}</span>
								</div>
								<div className="grid gap-3 sm:grid-cols-2">
									{(
										[
											["contextWindow", "Context window (tokens)"],
											["maxInputTokens", "Max input tokens"],
											["maxOutputTokens", "Max output tokens"],
											["reasoningEffort", "Reasoning effort (low/medium/high/…)"],
										] as [keyof ManualModelMetadata, string][]
									).map(([field, label]) => (
										<div key={field} className="flex flex-col gap-1.5">
											<Label>{label}</Label>
											<Input
												value={ruleMetadata[item.ruleId ?? ""]?.[field] ?? ""}
												onChange={(e) =>
													setRuleMetadata((prev) => ({
														...prev,
														[item.ruleId ?? ""]: { ...(prev[item.ruleId ?? ""] ?? EMPTY_MANUAL_METADATA), [field]: e.target.value },
													}))
												}
												placeholder="Leave empty to omit"
												data-testid={`agent-setup-manual-${item.id}-${field}`}
											/>
										</div>
									))}
								</div>
							</div>
						))}
					</div>
				)}

				<div className="flex flex-col gap-2">
					<div className="flex items-center justify-between">
						<Label>Extra parameters (add / edit)</Label>
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={() => setExtraParams((prev) => [...prev, { key: "", value: "" }])}
							data-testid="agent-setup-extra-add"
						>
							<Plus className="size-4" /> Add
						</Button>
					</div>
					{extraParams.map((row, i) => (
						<div key={i} className="flex gap-2">
							<Input
								value={row.key}
								onChange={(e) => setExtraParams((prev) => prev.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))}
								placeholder="key"
								className="font-mono"
								data-testid={`agent-setup-extra-key-${i}`}
							/>
							<Input
								value={row.value}
								onChange={(e) => setExtraParams((prev) => prev.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
								placeholder="value"
								className="font-mono"
								data-testid={`agent-setup-extra-value-${i}`}
							/>
							<Button
								type="button"
								variant="ghost"
								size="sm"
								onClick={() => setExtraParams((prev) => prev.filter((_, j) => j !== i))}
								aria-label="Remove extra parameter"
								data-testid={`agent-setup-extra-remove-${i}`}
							>
								<Trash2 className="size-4" />
							</Button>
						</div>
					))}
				</div>
			</section>

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
						Select one or more models or routing-rule triggers above to generate the {activeAgent.label} config.
					</div>
				)}
			</section>
		</div>
	);
}

/** Merge per-model datasheet params for the current selection (first model wins per key). */
function flattenFirstParams(byModel: Record<string, Record<string, any>>, selectedIds: string[]): Record<string, string> {
	const record: Record<string, string> = {};
	const qualified = (id: string) => (id.startsWith("direct:") ? id.slice("direct:".length) : id);
	for (const id of selectedIds) {
		const params = byModel[qualified(id)];
		if (!params) continue;
		for (const [key, value] of Object.entries(params)) {
			if (value === undefined || key in record) continue;
			record[key] = typeof value === "string" ? value : JSON.stringify(value);
		}
	}
	return record;
}