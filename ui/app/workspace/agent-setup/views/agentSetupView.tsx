import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SearchSelect } from "@/components/ui/searchSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import ModelParameters from "@/components/ui/custom/modelParameters";
import { NoPermissionView } from "@/components/noPermissionView";
import { maskSecret } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
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
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Check, Copy, Download, KeyRound, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
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
	type ModelSource,
} from "./agentSetupTypes";

function defaultEnvVar(agent: AgentId): string {
	return agent === "claude-code" ? DEFAULT_CLAUDE_ENV_VAR : DEFAULT_BIFROST_ENV_VAR;
}

/** Bifrost origin: current window origin, owned by the caller via an editable override. */
function defaultBaseUrl(): string {
	if (typeof window !== "undefined" && window.location.origin) return window.location.origin.replace(/\/+$/, "");
	return "";
}

export default function AgentSetupView() {
	const hasAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);

	// ── Wizard state ─────────────────────────────────────────────────
	const [agent, setAgent] = useState<AgentId>("opencode");
	const [platform, setPlatform] = useState<AgentPlatform>("linux");
	const [source, setSource] = useState<ModelSource>("direct");
	const [virtualKeyId, setVirtualKeyId] = useState<string | null>(null);
	const [provider, setProvider] = useState("");
	const [model, setModel] = useState("");
	const [ruleId, setRuleId] = useState("");
	const [baseUrlOverride, setBaseUrlOverride] = useState("");
	const [envVar, setEnvVar] = useState(DEFAULT_BIFROST_ENV_VAR);
	const [datasheetParams, setDatasheetParams] = useState<Record<string, any>>({});
	const [manualMetadata, setManualMetadata] = useState<ManualModelMetadata>(EMPTY_MANUAL_METADATA);
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

	// ── Model source data ────────────────────────────────────────────
	// When a virtual key is revealed, its secret rides the x-bf-vk header so
	// the server scopes both listings to what that key may reach. The secret
	// stays in the RTK Query cache only for these two keyed queries.
	const [vkRevealed, setVkRevealed] = useState(false);
	const { data: providers } = useGetProvidersQuery(undefined, { skip: !hasAccess });
	const { data: modelsData, isFetching: isFetchingModels } = useGetModelsQuery(
		{
			...(vkRevealed && virtualKeyId && selectedVk ? { virtualKeyValue: selectedVk.value } : { unfiltered: true }),
			...(provider ? { provider } : {}),
			limit: 2000,
		},
		{ skip: !hasAccess || (!!virtualKeyId && !selectedVk) },
	);
	const { data: rulesData } = useGetRoutingRulesQuery({ limit: 200 }, { skip: !hasAccess });
	const { data: detailsData } = useGetModelDetailsQuery(
		{
			...(vkRevealed && virtualKeyId && selectedVk ? { virtualKeyValue: selectedVk.value } : { unfiltered: true }),
			...(provider ? { provider } : {}),
			limit: 2000,
		},
		{ skip: !hasAccess || (!!virtualKeyId && !selectedVk) },
	);

	const providerNames = useMemo(() => (providers ?? []).map((p) => p.name).sort(), [providers]);

	// Direct-model rows, narrowed by provider. When a VK is revealed the
	// server already scoped the listing via x-bf-vk; the client-side pass
	// below only applies when the secret is still hidden (unscoped rows).
	const directModels = useMemo(() => {
		const rows = modelsData?.models ?? [];
		const byProvider = provider ? rows.filter((m) => m.provider === provider) : rows;
		if (vkRevealed || !selectedVk || selectedVk.allow_all_providers) return byProvider;
		const allowed = new Map<string, Set<string>>();
		for (const cfg of selectedVk.provider_configs ?? []) {
			allowed.set(cfg.provider, new Set(cfg.allowed_models ?? []));
		}
		if (allowed.size === 0) return byProvider;
		return byProvider.filter((m) => {
			const set = allowed.get(m.provider);
			if (!set) return false;
			if (!(set.has("*") || set.has(m.name))) return false;
			const cfg = (selectedVk.provider_configs ?? []).find((c) => c.provider === m.provider);
			if (cfg?.blacklisted_models?.includes(m.name)) return false;
			return true;
		});
	}, [modelsData, provider, selectedVk, vkRevealed]);

	// Rules whose CEL is a simple `model == "<trigger>"` — the only shape an
	// agent can fire through its model field. Shown by trigger alias; a VK
	// that pins providers hides rules routing outside those providers.
	const triggerableRules = useMemo(() => {
		const rules = (rulesData?.rules ?? []).filter((r) => r.enabled);
		const withTrigger = rules
			.map((r) => ({ rule: r, trigger: extractModelTrigger(r.cel_expression) }))
			.filter((e): e is { rule: (typeof rules)[number]; trigger: string } => e.trigger !== null);
		// Revealed VK: the rules list itself is not VK-scoped server-side, so
		// still narrow by provider allowlist; unrevealed VKs use the same
		// client-side allowlist pass.
		if (!selectedVk || selectedVk.allow_all_providers) return withTrigger;
		const allowedProviders = new Set((selectedVk.provider_configs ?? []).map((c) => c.provider));
		if (allowedProviders.size === 0) return withTrigger;
		return withTrigger.filter(({ rule }) => rule.targets.some((t) => !t.provider || allowedProviders.has(t.provider)));
	}, [rulesData, selectedVk]);

	const selectedRule = useMemo(() => triggerableRules.find(({ rule }) => rule.id === ruleId)?.rule, [triggerableRules, ruleId]);
	const selectedTrigger = useMemo(() => (selectedRule ? (extractModelTrigger(selectedRule.cel_expression) ?? "") : ""), [selectedRule]);

	// Datasheet limits for the direct-model path (feeds opencode `limit`).
	const datasheetLimits = useMemo(() => {
		if (source !== "direct" || !model) return undefined;
		const row = (detailsData?.models ?? []).find((m) => m.name === model && (!provider || m.provider === provider));
		if (!row) return undefined;
		const context = row.max_input_tokens ?? row.context_length;
		const output = row.max_output_tokens;
		if (context === undefined && output === undefined) return undefined;
		return { ...(context !== undefined ? { context } : {}), ...(output !== undefined ? { output } : {}) };
	}, [detailsData, model, provider, source]);

	const activeAgent = AGENTS.find((a) => a.id === agent) ?? AGENTS[0];
	const baseUrl = (baseUrlOverride.trim() || defaultBaseUrl()).replace(/\/+$/, "");

	const modelForConfig = source === "direct" ? model : selectedTrigger;
	const providerForConfig = source === "direct" ? provider || "bifrost" : "bifrost";

	const extraParamsRecord = useMemo(() => {
		const record: Record<string, string> = {};
		for (const { key, value } of extraParams) {
			if (key.trim()) record[key.trim()] = value;
		}
		return record;
	}, [extraParams]);

	const builderInput: AgentConfigInput | null =
		baseUrl && modelForConfig
			? {
					baseUrl,
					model: modelForConfig,
					providerName: providerForConfig,
					...(source === "direct" && datasheetLimits ? { limit: datasheetLimits } : {}),
					...(source === "rule" ? { manualMetadata } : {}),
					extraParams: { ...datasheetParamsString(datasheetParams), ...extraParamsRecord },
					envVar: envVar.trim() || defaultEnvVar(agent),
					...(selectedVk ? { virtualKeyName: selectedVk.name } : {}),
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

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-col gap-6 p-4">
			<PageTitle>Generate copy-ready model configs for opencode, Claude Code and Codex pointing at Bifrost</PageTitle>

			{/* ── Step 0: virtual key ─────────────────────────────────── */}
			<section className="flex flex-col gap-2">
				<div className="flex items-center gap-2 text-sm font-medium">
					<KeyRound className="size-4" />
					<span>Virtual key (optional)</span>
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
							setVkRevealed(false);
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
								<span className="truncate">{selectedVk?.name ?? "No virtual key (server default)"}</span>
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
						<Button
							type="button"
							variant="ghost"
							size="sm"
							onClick={() => {
								setVirtualKeyId(null);
								setVkRevealed(false);
							}}
							data-testid="agent-setup-vk-clear"
						>
							Clear
						</Button>
					)}
					{selectedVk && (
						<Button
							type="button"
							variant={vkRevealed ? "secondary" : "outline"}
							size="sm"
							onClick={() => setVkRevealed((v) => !v)}
							data-testid="agent-setup-vk-reveal"
							title={
								vkRevealed
									? "Server-scoped listing is active"
									: "Reveal once so model lists are scoped server-side to what this key may reach"
							}
						>
							{vkRevealed ? "Scoped ✓" : "Scope lists to this key"}
						</Button>
					)}
				</div>
				{vkHint && <p className="text-muted-foreground text-xs">{vkHint} — lists below are narrowed to what this key can use.</p>}
				{selectedVk && !vkRevealed && (
					<p className="text-muted-foreground text-xs">
						Preview narrowed from the key’s allowlist. Click “Scope lists to this key” for the exact server-side listing.
					</p>
				)}
				<p className="text-muted-foreground text-xs">
					The key secret is never written into the config — set it once via the shell exports below.
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

			{/* ── Step 2: model source ────────────────────────────────── */}
			<section className="flex flex-col gap-3">
				<div className="text-sm font-medium">Model</div>
				<Tabs value={source} onValueChange={(value) => setSource(value as ModelSource)}>
					<TabsList className="rounded-sm" data-testid="agent-setup-source-tabs">
						<TabsTrigger value="direct" data-testid="agent-setup-source-direct">
							Direct model
						</TabsTrigger>
						<TabsTrigger value="rule" data-testid="agent-setup-source-rule">
							Routing rule
						</TabsTrigger>
					</TabsList>
				</Tabs>

				{source === "direct" ? (
					<div className="grid gap-3 sm:grid-cols-2">
						<div className="flex flex-col gap-1.5">
							<Label>Provider</Label>
							<Select
								value={provider}
								onValueChange={(v) => {
									setProvider(v);
									setModel("");
									setDatasheetParams({});
								}}
							>
								<SelectTrigger data-testid="agent-setup-provider-select">
									<SelectValue placeholder="Select provider" />
								</SelectTrigger>
								<SelectContent>
									{providerNames.map((name) => (
										<SelectItem key={name} value={name}>
											{name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label>Model</Label>
							<Select
								value={model}
								disabled={!provider}
								onValueChange={(v) => {
									setModel(v);
									setDatasheetParams({});
								}}
							>
								<SelectTrigger data-testid="agent-setup-model-select">
									<SelectValue placeholder={provider ? "Select model" : "Select a provider first"} />
								</SelectTrigger>
								<SelectContent>
									{directModels.map((m) => (
										<SelectItem key={`${m.provider}/${m.name}`} value={m.name}>
											{m.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{provider &&
								(directModels.length > 0 ? (
									<p className="text-muted-foreground text-xs">
										{directModels.length} model{directModels.length === 1 ? "" : "s"} available
										{isFetchingModels ? " (refreshing…)" : ""}
									</p>
								) : (
									<p className="text-muted-foreground text-xs">
										{isFetchingModels ? "Loading models…" : "No models returned for this provider."}
									</p>
								))}
						</div>
					</div>
				) : (
					<div className="flex flex-col gap-1.5">
						<Label>Routing rule (agent sends the trigger as its model)</Label>
						<Select
							value={ruleId}
							onValueChange={(v) => {
								setRuleId(v);
								setManualMetadata(EMPTY_MANUAL_METADATA);
							}}
						>
							<SelectTrigger data-testid="agent-setup-rule-select">
								<SelectValue placeholder="Select routing rule" />
							</SelectTrigger>
							<SelectContent>
								{triggerableRules.map(({ rule, trigger }) => (
									<SelectItem key={rule.id} value={rule.id}>
										{trigger} → {rule.targets.map((t) => [t.provider, t.model].filter(Boolean).join("/")).join(", ")}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						{selectedRule && (
							<p className="text-muted-foreground text-xs">
								Rule “{selectedRule.name}” · scope {selectedRule.scope} · CEL{" "}
								<code className="font-mono">{selectedRule.cel_expression}</code>
							</p>
						)}
						<p className="text-muted-foreground text-xs">
							Only rules with a simple <code className="font-mono">model == "…"</code> trigger are listed — header- or regex-based rules
							can’t be fired from an agent’s model field.
						</p>
					</div>
				)}
			</section>

			{/* ── Step 3: parameters ──────────────────────────────────── */}
			<section className="flex flex-col gap-3">
				<div className="text-sm font-medium">Parameters</div>
				{source === "direct" && model ? (
					<ModelParameters model={model} config={datasheetParams} onChange={setDatasheetParams} />
				) : source === "rule" && selectedRule ? (
					<div className="flex flex-col gap-3 rounded-sm border p-3">
						<p className="text-muted-foreground text-xs">
							A rule can route to targets with different context windows, so set metadata manually to match your target (
							{selectedRule.targets.map((t) => [t.provider, t.model].filter(Boolean).join("/")).join(", ")}).
						</p>
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
										value={manualMetadata[field]}
										onChange={(e) => setManualMetadata((prev) => ({ ...prev, [field]: e.target.value }))}
										placeholder="Leave empty to omit"
										data-testid={`agent-setup-manual-${field}`}
									/>
								</div>
							))}
						</div>
					</div>
				) : (
					<p className="text-muted-foreground text-xs">Pick a model or routing rule above to set parameters.</p>
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
					<Label>Credential env var</Label>
					<Input
						value={envVar}
						onChange={(e) => setEnvVar(e.target.value)}
						placeholder={defaultEnvVar(agent)}
						className="font-mono"
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
						<Button
							variant="outline"
							size="sm"
							onClick={() => output && void copy(output.shellExports)}
							disabled={!canGenerate}
							data-testid="agent-setup-copy-shell"
						>
							<Copy className="mr-1.5 h-4 w-4" />
							Copy shell exports
						</Button>
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
									{activeAgent.label} · {modelForConfig}
								</div>
								<Button
									variant="ghost"
									size="sm"
									onClick={() => void copy(output.shellExports)}
									data-testid="agent-setup-copy-shell-inline"
								>
									<Copy className="mr-1.5 h-3.5 w-3.5" />
									Shell exports
								</Button>
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
						Select a model or routing rule above to generate the {activeAgent.label} config.
					</div>
				)}
			</section>
		</div>
	);
}

/** Flatten datasheet field values into string extras (objects become JSON). */
function datasheetParamsString(params: Record<string, any>): Record<string, string> {
	const record: Record<string, string> = {};
	for (const [key, value] of Object.entries(params)) {
		if (value === undefined) continue;
		record[key] = typeof value === "string" ? value : JSON.stringify(value);
	}
	return record;
}