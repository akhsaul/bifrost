import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetPluginQuery, useUpdatePluginMutation } from "@/lib/store";
import {
	GUARDRAILS_PLUGIN_NAME,
	defaultGuardrailsConfig,
	type GuardrailPattern,
	type GuardrailProvider,
	type GuardrailProviderType,
	type GuardrailRule,
	type GuardrailsPluginConfig,
	type PatternAction,
	type RedactionMode,
	type RedactionStrategy,
} from "@/lib/types/guardrails";
import { cn } from "@/lib/utils";
import { ChevronDown, Code, Key, MoreHorizontal, Pencil, Plus, Shield, ShieldCheck, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

const FLAG_OPTIONS = [
	{ value: "none", label: "None" },
	{ value: "i", label: "i — case-insensitive" },
	{ value: "m", label: "m — multiline" },
	{ value: "s", label: "s — dot matches newline" },
	{ value: "im", label: "im — case-insensitive + multiline" },
	{ value: "is", label: "is — case-insensitive + dot matches newline" },
	{ value: "ms", label: "ms — multiline + dot matches newline" },
	{ value: "ims", label: "ims — all flags" },
];

const ACTION_OPTIONS: { value: PatternAction; label: string }[] = [
	{ value: "block", label: "Block" },
	{ value: "redact", label: "Redact" },
	{ value: "detect_only", label: "Detect Only" },
];

const STRATEGY_OPTIONS: { value: RedactionStrategy; label: string }[] = [
	{ value: "replace", label: "Replace" },
	{ value: "mask", label: "Mask (*)" },
	{ value: "hash", label: "Hash (SHA-256)" },
];

const REDACTION_MODE_OPTIONS: { value: RedactionMode; label: string }[] = [
	{ value: "runtime", label: "Permanent (Masked to LLM & Client)" },
	{ value: "runtime_reversible", label: "Reversible (Restored in Tool Calls)" },
];

const PII_TEMPLATE_PATTERNS: GuardrailPattern[] = [
	{
		pattern: "\\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\\.[A-Z]{2,}\\b",
		description: "Email address",
		entity_type: "EMAIL",
		flags: "i",
		action: "block",
		redaction_strategy: "replace",
		redaction_mode: "runtime",
	},
	{
		pattern: "\\b(?:\\+?1[-.\\s]?)?(?:\\(?\\d{3}\\)?[-.\\s]?)\\d{3}[-.\\s]?\\d{4}\\b",
		description: "US phone number",
		entity_type: "PHONE_NUMBER",
		action: "block",
		redaction_strategy: "replace",
		redaction_mode: "runtime",
	},
	{
		pattern: "\\b\\d{3}-\\d{2}-\\d{4}\\b",
		description: "US Social Security Number",
		entity_type: "US_SSN",
		action: "block",
		redaction_strategy: "replace",
		redaction_mode: "runtime",
	},
	{
		pattern: "\\b(?:\\d[ -]?){13,19}\\b",
		description: "Credit card-like number",
		entity_type: "CREDIT_CARD",
		action: "block",
		redaction_strategy: "replace",
		redaction_mode: "runtime",
	},
	{
		pattern: "\\b(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)(?:\\.(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)){3}\\b",
		description: "IPv4 address",
		entity_type: "IP_ADDRESS",
		action: "block",
		redaction_strategy: "replace",
		redaction_mode: "runtime",
	},
];

function parseConfig(raw: unknown): GuardrailsPluginConfig {
	if (!raw || typeof raw !== "object") return defaultGuardrailsConfig();
	const r = raw as Record<string, unknown>;
	return {
		guardrail_providers: Array.isArray(r.guardrail_providers) ? (r.guardrail_providers as GuardrailProvider[]) : [],
		guardrail_rules: Array.isArray(r.guardrail_rules) ? (r.guardrail_rules as GuardrailRule[]) : [],
	};
}

function nextProviderId(providers: GuardrailProvider[]): number {
	const ids = providers.map((p) => p.id);
	return ids.length ? Math.max(...ids) + 1 : 1;
}

function emptyPattern(): GuardrailPattern {
	return {
		pattern: "",
		description: "",
		action: "block",
		redaction_strategy: "replace",
		redaction_mode: "runtime",
	};
}

interface SheetState {
	open: boolean;
	provider: GuardrailProvider | null;
	providerType: GuardrailProviderType;
}

export default function GuardrailsProviderView() {
	const { data: plugin, isLoading } = useGetPluginQuery(GUARDRAILS_PLUGIN_NAME);
	const [updatePlugin, { isLoading: isSaving }] = useUpdatePluginMutation();
	const [providers, setProviders] = useState<GuardrailProvider[]>([]);
	const [pluginEnabled, setPluginEnabled] = useState(false);
	const [activeTab, setActiveTab] = useState<GuardrailProviderType>("regex");
	const [sheet, setSheet] = useState<SheetState>({ open: false, provider: null, providerType: "regex" });

	useEffect(() => {
		const cfg = parseConfig(plugin?.config);
		setProviders(cfg.guardrail_providers);
		setPluginEnabled(Boolean(plugin?.enabled));
	}, [plugin]);

	const handleTogglePlugin = async (newVal: boolean) => {
		setPluginEnabled(newVal);
		try {
			const cfg = parseConfig(plugin?.config);
			await updatePlugin({ name: GUARDRAILS_PLUGIN_NAME, data: { enabled: newVal, config: cfg } }).unwrap();
			toast.success(`Guardrails plugin ${newVal ? "enabled" : "disabled"}`);
		} catch (error) {
			setPluginEnabled(!newVal);
			toast.error(getErrorMessage(error));
		}
	};

	const handleSheetSave = async (saved: GuardrailProvider) => {
		const isNew = sheet.provider === null;
		let next: GuardrailProvider[];
		if (isNew) {
			next = [...providers, saved];
		} else {
			next = providers.map((p) => (p.id === saved.id ? saved : p));
		}
		try {
			const cfg = parseConfig(plugin?.config);
			await updatePlugin({
				name: GUARDRAILS_PLUGIN_NAME,
				data: { enabled: pluginEnabled, config: { ...cfg, guardrail_providers: next } satisfies GuardrailsPluginConfig },
			}).unwrap();
			setProviders(next);
			setSheet({ open: false, provider: null, providerType: activeTab });
			toast.success(`Guardrail configuration ${isNew ? "created" : "updated"}`);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleDelete = async (id: number) => {
		const next = providers.filter((p) => p.id !== id);
		try {
			const cfg = parseConfig(plugin?.config);
			await updatePlugin({
				name: GUARDRAILS_PLUGIN_NAME,
				data: { enabled: pluginEnabled, config: { ...cfg, guardrail_providers: next } satisfies GuardrailsPluginConfig },
			}).unwrap();
			setProviders(next);
			toast.success("Guardrail configuration deleted");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleToggleProvider = async (id: number, newVal: boolean) => {
		const next = providers.map((p) => (p.id === id ? { ...p, enabled: newVal } : p));
		try {
			const cfg = parseConfig(plugin?.config);
			await updatePlugin({
				name: GUARDRAILS_PLUGIN_NAME,
				data: { enabled: pluginEnabled, config: { ...cfg, guardrail_providers: next } satisfies GuardrailsPluginConfig },
			}).unwrap();
			setProviders(next);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleCreate = (type: GuardrailProviderType) => {
		let initialConfig: GuardrailProvider["config"] = {};
		if (type === "regex") {
			initialConfig = { patterns: [emptyPattern()] };
		} else if (type === "secrets") {
			initialConfig = {
				action: "block",
				redaction_strategy: "replace",
				redaction_mode: "runtime",
				ignored_secret_keywords: [],
			};
		} else {
			initialConfig = {
				judge_provider: "",
				judge_model: "",
				rule: "",
				timeout: 30,
				max_output_tokens: 200,
			};
		}

		setSheet({
			open: true,
			providerType: type,
			provider: {
				id: nextProviderId(providers),
				provider_name: type,
				policy_name: "",
				enabled: true,
				config: initialConfig,
			},
		});
	};

	const handleEdit = (provider: GuardrailProvider) => {
		setSheet({
			open: true,
			providerType: provider.provider_name,
			provider: structuredClone(provider),
		});
	};

	const currentProviders = providers.filter((p) => {
		if (activeTab === "prompt-guardrail") {
			return p.provider_name === "prompt-guardrail" || p.provider_name === "prompt_guardrail";
		}
		return p.provider_name === activeTab;
	});

	if (isLoading) {
		return <div className="text-muted-foreground p-8 text-sm">Loading guardrails…</div>;
	}

	return (
		<div className="flex flex-col gap-6 md:flex-row">
			{/* Left Navigation Sidebar: Providers */}
			<div className="w-full shrink-0 space-y-2 md:w-60">
				<div className="text-muted-foreground mb-2 px-2 text-xs font-semibold tracking-wider uppercase">Providers</div>
				<nav className="space-y-1">
					{/* Custom Regex */}
					<button
						type="button"
						onClick={() => setActiveTab("regex")}
						className={cn(
							"w-full flex items-center justify-between px-3 py-2 rounded-md text-sm font-medium transition-colors text-left",
							activeTab === "regex" ? "bg-secondary text-primary" : "hover:bg-muted/50 text-muted-foreground hover:text-foreground",
						)}
					>
						<div className="flex items-center gap-2.5">
							<Code className="h-4 w-4" />
							<span>Custom Regex</span>
						</div>
						<Badge variant="outline" className="px-1 py-0 font-mono text-[10px]">
							RE2
						</Badge>
					</button>

					{/* Secrets Detection (Betterleaks) */}
					<button
						type="button"
						onClick={() => setActiveTab("secrets")}
						className={cn(
							"w-full flex items-center justify-between px-3 py-2 rounded-md text-sm font-medium transition-colors text-left",
							activeTab === "secrets" ? "bg-secondary text-primary" : "hover:bg-muted/50 text-muted-foreground hover:text-foreground",
						)}
					>
						<div className="flex items-center gap-2.5">
							<Key className="h-4 w-4" />
							<span>Secrets Detection</span>
						</div>
						<Badge variant="outline" className="px-1 py-0 text-[10px] text-emerald-600 dark:text-emerald-400">
							Betterleaks
						</Badge>
					</button>

					{/* Prompt Guardrails (LLM Judge) */}
					<button
						type="button"
						onClick={() => setActiveTab("prompt-guardrail")}
						className={cn(
							"w-full flex items-center justify-between px-3 py-2 rounded-md text-sm font-medium transition-colors text-left",
							activeTab === "prompt-guardrail"
								? "bg-secondary text-primary"
								: "hover:bg-muted/50 text-muted-foreground hover:text-foreground",
						)}
					>
						<div className="flex items-center gap-2.5">
							<ShieldCheck className="h-4 w-4" />
							<span>Prompt Guardrails</span>
						</div>
						<Badge variant="outline" className="px-1 py-0 text-[10px] text-purple-600 dark:text-purple-400">
							Judge
						</Badge>
					</button>

					{/* Coming soon providers */}
					<div className="text-muted-foreground flex items-center justify-between rounded-md px-3 py-2 text-sm opacity-50 select-none">
						<div className="flex items-center gap-2.5">
							<Shield className="h-4 w-4" />
							<span>AWS Bedrock</span>
						</div>
						<Badge variant="outline" className="px-1 py-0 text-[10px]">
							Soon
						</Badge>
					</div>
					<div className="text-muted-foreground flex items-center justify-between rounded-md px-3 py-2 text-sm opacity-50 select-none">
						<div className="flex items-center gap-2.5">
							<Shield className="h-4 w-4" />
							<span>Azure Safety</span>
						</div>
						<Badge variant="outline" className="px-1 py-0 text-[10px]">
							Soon
						</Badge>
					</div>
				</nav>
			</div>

			{/* Main Content Area */}
			<div className="min-w-0 flex-1 space-y-4">
				{/* Top Header */}
				<div className="flex flex-wrap items-center justify-between gap-4">
					<div>
						<h1 className="text-xl font-bold tracking-tight">
							{activeTab === "regex" && "Regex Guardrail Configurations"}
							{activeTab === "secrets" && "Secrets Detection Configurations (Betterleaks)"}
							{activeTab === "prompt-guardrail" && "Prompt Guardrail Configurations (LLM Judge)"}
						</h1>
						<p className="text-muted-foreground text-sm">
							{activeTab === "regex" && "Define deterministic regex patterns to evaluate and moderate prompts and completions."}
							{activeTab === "secrets" && "Scans credentials, tokens, API keys, and private keys using the embedded Betterleaks engine."}
							{activeTab === "prompt-guardrail" && "Natural-language policies evaluated by a configured Bifrost LLM judge model."}
						</p>
					</div>
					<div className="flex items-center gap-3">
						<div className="flex items-center gap-2">
							<Switch checked={pluginEnabled} onCheckedChange={handleTogglePlugin} data-testid="guardrails-plugin-enabled-switch" />
							<Label className="text-xs">Plugin enabled</Label>
						</div>
						<Button onClick={() => handleCreate(activeTab)} data-testid="guardrails-provider-add">
							<Plus className="mr-1.5 h-4 w-4" /> Add Configuration
						</Button>
					</div>
				</div>

				{/* Table */}
				<div className="bg-card rounded-md border">
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead className="w-16">ID</TableHead>
								<TableHead>Name</TableHead>
								<TableHead className="w-36">
									{activeTab === "regex" && "Patterns"}
									{activeTab === "secrets" && "Action / Mode"}
									{activeTab === "prompt-guardrail" && "Judge Model"}
								</TableHead>
								<TableHead className="w-28">Is Enabled</TableHead>
								<TableHead className="w-24 text-right">Actions</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{currentProviders.length === 0 ? (
								<TableRow>
									<TableCell colSpan={5} className="text-muted-foreground h-32 text-center text-sm">
										No configurations found for this provider. Click <strong>Add Configuration</strong> to create one.
									</TableCell>
								</TableRow>
							) : (
								currentProviders.map((p) => (
									<TableRow
										key={p.id}
										className="hover:bg-muted/50 cursor-pointer"
										onClick={() => handleEdit(p)}
										data-testid={`guardrails-provider-row-${p.id}`}
									>
										<TableCell className="font-mono text-xs">{p.id}</TableCell>
										<TableCell className="font-medium">
											{p.policy_name || "Untitled"}
											{activeTab === "prompt-guardrail" && p.config.rule && (
												<div className="text-muted-foreground max-w-sm truncate text-[11px] font-normal">{p.config.rule}</div>
											)}
										</TableCell>
										<TableCell className="text-muted-foreground text-xs">
											{activeTab === "regex" && `${p.config.patterns?.length ?? 0} pattern(s)`}
											{activeTab === "secrets" && (
												<div className="space-y-0.5">
													<Badge variant="outline" className="text-[10px] capitalize">
														{p.config.action || "block"}
													</Badge>
													{p.config.redaction_mode === "runtime_reversible" && (
														<Badge variant="secondary" className="ml-1 text-[10px]">
															Reversible
														</Badge>
													)}
												</div>
											)}
											{activeTab === "prompt-guardrail" && (
												<code className="bg-muted/60 rounded px-1 py-0.5 text-xs">{p.config.judge_model || "Not set"}</code>
											)}
										</TableCell>
										<TableCell onClick={(e) => e.stopPropagation()}>
											<Switch
												checked={p.enabled}
												onCheckedChange={(val) => handleToggleProvider(p.id, val)}
												data-testid={`guardrails-provider-enabled-${p.id}`}
											/>
										</TableCell>
										<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
											<DropdownMenu>
												<DropdownMenuTrigger asChild>
													<Button variant="ghost" size="icon" className="h-8 w-8" data-testid={`guardrails-provider-actions-${p.id}`}>
														<MoreHorizontal className="h-4 w-4" />
													</Button>
												</DropdownMenuTrigger>
												<DropdownMenuContent align="end">
													<DropdownMenuItem onClick={() => handleEdit(p)} data-testid={`guardrails-provider-edit-${p.id}`}>
														<Pencil className="mr-2 h-3.5 w-3.5" /> Edit
													</DropdownMenuItem>
													<DropdownMenuItem
														className="text-destructive focus:text-destructive"
														onClick={() => handleDelete(p.id)}
														data-testid={`guardrails-provider-delete-${p.id}`}
													>
														<Trash2 className="mr-2 h-3.5 w-3.5" /> Delete
													</DropdownMenuItem>
												</DropdownMenuContent>
											</DropdownMenu>
										</TableCell>
									</TableRow>
								))
							)}
						</TableBody>
					</Table>
				</div>
			</div>

			{/* Slide-over Sheets */}
			{sheet.open && sheet.providerType === "regex" && (
				<RegexConfigSheet
					open={sheet.open}
					onOpenChange={(open) => !open && setSheet({ open: false, provider: null, providerType: activeTab })}
					provider={sheet.provider}
					onSave={handleSheetSave}
					isSaving={isSaving}
				/>
			)}

			{sheet.open && sheet.providerType === "secrets" && (
				<SecretsConfigSheet
					open={sheet.open}
					onOpenChange={(open) => !open && setSheet({ open: false, provider: null, providerType: activeTab })}
					provider={sheet.provider}
					onSave={handleSheetSave}
					isSaving={isSaving}
				/>
			)}

			{sheet.open && (sheet.providerType === "prompt-guardrail" || sheet.providerType === "prompt_guardrail") && (
				<PromptJudgeConfigSheet
					open={sheet.open}
					onOpenChange={(open) => !open && setSheet({ open: false, provider: null, providerType: activeTab })}
					provider={sheet.provider}
					onSave={handleSheetSave}
					isSaving={isSaving}
				/>
			)}
		</div>
	);
}

// ---------------------------------------------------------------------------
// 1. Regex Configuration Sheet
// ---------------------------------------------------------------------------

interface SheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	provider: GuardrailProvider | null;
	onSave: (provider: GuardrailProvider) => void;
	isSaving: boolean;
}

function RegexConfigSheet({ open, onOpenChange, provider, onSave, isSaving }: SheetProps) {
	const [formState, setFormState] = useState<GuardrailProvider | null>(null);

	useEffect(() => {
		if (provider) {
			setFormState(structuredClone(provider));
		}
	}, [provider]);

	if (!formState) return null;

	const patterns = formState.config.patterns ?? [];

	const handleAddCustom = () => {
		setFormState((prev) => (prev ? { ...prev, config: { ...prev.config, patterns: [...patterns, emptyPattern()] } } : prev));
	};

	const handleAddPIITemplate = () => {
		setFormState((prev) =>
			prev ? { ...prev, config: { ...prev.config, patterns: [...patterns, ...structuredClone(PII_TEMPLATE_PATTERNS)] } } : prev,
		);
	};

	const updatePattern = (idx: number, patch: Partial<GuardrailPattern>) => {
		setFormState((prev) =>
			prev
				? {
						...prev,
						config: {
							...prev.config,
							patterns: patterns.map((pat, i) => (i === idx ? { ...pat, ...patch } : pat)),
						},
					}
				: prev,
		);
	};

	const removePattern = (idx: number) => {
		setFormState((prev) =>
			prev
				? {
						...prev,
						config: {
							...prev.config,
							patterns: patterns.filter((_, i) => i !== idx),
						},
					}
				: prev,
		);
	};

	const handleSubmit = () => {
		if (!formState.policy_name.trim()) {
			toast.error("Configuration name is required");
			return;
		}
		if (patterns.length === 0) {
			toast.error("At least one pattern is required");
			return;
		}
		for (let i = 0; i < patterns.length; i++) {
			if (!patterns[i].pattern.trim()) {
				toast.error(`Pattern #${i + 1}: regex expression is required`);
				return;
			}
		}
		onSave(formState);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col overflow-hidden p-0 sm:max-w-xl md:max-w-2xl">
				<SheetHeader className="border-b p-6">
					<SheetTitle>Regex Guardrail Configuration</SheetTitle>
					<SheetDescription>Define custom regex patterns to block or flag matching content.</SheetDescription>
				</SheetHeader>

				<div className="flex-1 space-y-6 overflow-y-auto p-6">
					<div className="space-y-1.5">
						<Label htmlFor="regex-name">
							Name <span className="text-destructive">*</span>
						</Label>
						<Input
							id="regex-name"
							value={formState.policy_name}
							onChange={(e) => setFormState({ ...formState, policy_name: e.target.value })}
							placeholder="e.g. PII Detection"
							data-testid="guardrails-sheet-name"
						/>
					</div>

					<div className="space-y-3">
						<div className="flex items-center justify-between">
							<div>
								<div className="text-sm font-semibold">Patterns</div>
								<div className="text-muted-foreground text-xs">Define regex patterns to match against request and response text.</div>
							</div>

							<DropdownMenu>
								<DropdownMenuTrigger asChild>
									<Button variant="outline" size="sm" data-testid="guardrails-add-pattern-btn">
										<Plus className="mr-1 h-3.5 w-3.5" /> Add Pattern
										<ChevronDown className="ml-1 h-3.5 w-3.5 opacity-70" />
									</Button>
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end">
									<DropdownMenuItem onClick={handleAddCustom} data-testid="guardrails-add-custom-pattern">
										Custom regexp
									</DropdownMenuItem>
									<DropdownMenuItem onClick={handleAddPIITemplate} data-testid="guardrails-add-pii-template">
										PII Detection
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						</div>

						<div className="space-y-3">
							{patterns.map((pat, idx) => (
								<div key={idx} className="bg-card space-y-3 rounded-lg border p-4 shadow-xs" data-testid={`guardrails-pattern-card-${idx}`}>
									<div className="flex items-start gap-3">
										<div className="flex-1 space-y-1">
											<Label className="text-xs">
												Pattern <span className="text-destructive">*</span>
											</Label>
											<Input
												className="font-mono text-xs"
												placeholder="\\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\\.[A-Z]{2,}\\b"
												value={pat.pattern}
												onChange={(e) => updatePattern(idx, { pattern: e.target.value })}
												data-testid={`guardrails-pattern-input-${idx}`}
											/>
										</div>

										<div className="w-36 space-y-1">
											<Label className="text-xs">Flags</Label>
											<Select
												value={pat.flags || "none"}
												onValueChange={(val) => updatePattern(idx, { flags: val === "none" ? undefined : val })}
											>
												<SelectTrigger className="h-9 text-xs">
													<SelectValue />
												</SelectTrigger>
												<SelectContent>
													{FLAG_OPTIONS.map((opt) => (
														<SelectItem key={opt.value} value={opt.value} className="text-xs">
															{opt.label}
														</SelectItem>
													))}
												</SelectContent>
											</Select>
										</div>

										<Button
											variant="ghost"
											size="icon"
											className="text-muted-foreground hover:text-destructive mt-5 h-9 w-9 shrink-0"
											onClick={() => removePattern(idx)}
											data-testid={`guardrails-pattern-delete-${idx}`}
										>
											<Trash2 className="h-4 w-4" />
										</Button>
									</div>

									<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
										<div className="space-y-1">
											<Label className="text-muted-foreground text-xs">Description (optional)</Label>
											<Input
												className="h-8 text-xs"
												placeholder="e.g. Email address"
												value={pat.description ?? ""}
												onChange={(e) => updatePattern(idx, { description: e.target.value })}
											/>
										</div>

										<div className="flex gap-2">
											<div className="flex-1 space-y-1">
												<Label className="text-muted-foreground text-xs">Action</Label>
												<Select value={pat.action ?? "block"} onValueChange={(v) => updatePattern(idx, { action: v as PatternAction })}>
													<SelectTrigger className="h-8 text-xs">
														<SelectValue />
													</SelectTrigger>
													<SelectContent>
														{ACTION_OPTIONS.map((a) => (
															<SelectItem key={a.value} value={a.value} className="text-xs">
																{a.label}
															</SelectItem>
														))}
													</SelectContent>
												</Select>
											</div>

											{pat.action === "redact" && (
												<div className="flex-1 space-y-1">
													<Label className="text-muted-foreground text-xs">Mode</Label>
													<Select
														value={pat.redaction_mode || "runtime"}
														onValueChange={(v) => updatePattern(idx, { redaction_mode: v as RedactionMode })}
													>
														<SelectTrigger className="h-8 text-xs">
															<SelectValue />
														</SelectTrigger>
														<SelectContent>
															{REDACTION_MODE_OPTIONS.map((m) => (
																<SelectItem key={m.value} value={m.value} className="text-xs">
																	{m.label}
																</SelectItem>
															))}
														</SelectContent>
													</Select>
												</div>
											)}
										</div>
									</div>
								</div>
							))}
						</div>
					</div>
				</div>

				<div className="bg-background flex items-center justify-between border-t p-4">
					<div className="flex items-center gap-2">
						<Switch
							id="regex-sheet-enabled"
							checked={formState.enabled}
							onCheckedChange={(v) => setFormState({ ...formState, enabled: v })}
						/>
						<Label htmlFor="regex-sheet-enabled" className="text-xs">
							Enabled
						</Label>
					</div>

					<div className="flex items-center gap-2">
						<Button variant="ghost" size="sm" onClick={() => provider && setFormState(structuredClone(provider))}>
							Reset
						</Button>
						<Button size="sm" onClick={handleSubmit} disabled={isSaving}>
							Save Configuration
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}

// ---------------------------------------------------------------------------
// 2. Secrets Detection (Betterleaks) Sheet
// ---------------------------------------------------------------------------

function SecretsConfigSheet({ open, onOpenChange, provider, onSave, isSaving }: SheetProps) {
	const [formState, setFormState] = useState<GuardrailProvider | null>(null);
	const [keywordsStr, setKeywordsStr] = useState("");

	useEffect(() => {
		if (provider) {
			setFormState(structuredClone(provider));
			setKeywordsStr((provider.config.ignored_secret_keywords ?? []).join(", "));
		}
	}, [provider]);

	if (!formState) return null;

	const handleSubmit = () => {
		if (!formState.policy_name.trim()) {
			toast.error("Configuration name is required");
			return;
		}
		const kws = keywordsStr
			.split(",")
			.map((k) => k.trim())
			.filter(Boolean);
		onSave({
			...formState,
			config: {
				...formState.config,
				ignored_secret_keywords: kws,
			},
		});
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col overflow-hidden p-0 sm:max-w-xl md:max-w-2xl">
				<SheetHeader className="border-b p-6">
					<SheetTitle>Secrets Detection Configuration</SheetTitle>
					<SheetDescription>Configure the embedded Betterleaks engine to scan for API keys, tokens, and credentials.</SheetDescription>
				</SheetHeader>

				<div className="flex-1 space-y-6 overflow-y-auto p-6">
					<div className="space-y-1.5">
						<Label htmlFor="secrets-name">
							Name <span className="text-destructive">*</span>
						</Label>
						<Input
							id="secrets-name"
							value={formState.policy_name}
							onChange={(e) => setFormState({ ...formState, policy_name: e.target.value })}
							placeholder="e.g. betterleaks-scanner"
						/>
					</div>

					<div className="space-y-1.5">
						<Label>Action</Label>
						<Select
							value={formState.config.action || "block"}
							onValueChange={(v) =>
								setFormState({
									...formState,
									config: { ...formState.config, action: v as PatternAction },
								})
							}
						>
							<SelectTrigger>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{ACTION_OPTIONS.map((a) => (
									<SelectItem key={a.value} value={a.value}>
										{a.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<p className="text-muted-foreground text-xs">
							Choose whether detected secrets should immediately block the request or be redacted.
						</p>
					</div>

					{formState.config.action === "redact" && (
						<>
							<div className="space-y-1.5">
								<Label>Redaction Strategy</Label>
								<Select
									value={formState.config.redaction_strategy || "replace"}
									onValueChange={(v) =>
										setFormState({
											...formState,
											config: { ...formState.config, redaction_strategy: v as RedactionStrategy },
										})
									}
								>
									<SelectTrigger>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{STRATEGY_OPTIONS.map((s) => (
											<SelectItem key={s.value} value={s.value}>
												{s.label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>

							<div className="space-y-1.5">
								<Label>Redaction Mode</Label>
								<Select
									value={formState.config.redaction_mode || "runtime"}
									onValueChange={(v) =>
										setFormState({
											...formState,
											config: { ...formState.config, redaction_mode: v as RedactionMode },
										})
									}
								>
									<SelectTrigger>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{REDACTION_MODE_OPTIONS.map((m) => (
											<SelectItem key={m.value} value={m.value}>
												{m.label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-xs">
									Reversible mode restores secrets inside <code>tool_calls</code> arguments (e.g. bash scripts) on response.
								</p>
							</div>
						</>
					)}

					<div className="space-y-1.5">
						<Label htmlFor="secrets-ignore">Ignored Secret Keywords</Label>
						<Textarea
							id="secrets-ignore"
							rows={3}
							value={keywordsStr}
							onChange={(e) => setKeywordsStr(e.target.value)}
							placeholder="example, dummy_key, test_token"
							className="font-mono text-xs"
						/>
						<p className="text-muted-foreground text-xs">
							Comma-separated substrings. Any secret containing these keywords will be ignored (reduces false positives).
						</p>
					</div>
				</div>

				<div className="bg-background flex items-center justify-between border-t p-4">
					<div className="flex items-center gap-2">
						<Switch
							id="secrets-sheet-enabled"
							checked={formState.enabled}
							onCheckedChange={(v) => setFormState({ ...formState, enabled: v })}
						/>
						<Label htmlFor="secrets-sheet-enabled" className="text-xs">
							Enabled
						</Label>
					</div>

					<div className="flex items-center gap-2">
						<Button variant="ghost" size="sm" onClick={() => provider && setFormState(structuredClone(provider))}>
							Reset
						</Button>
						<Button size="sm" onClick={handleSubmit} disabled={isSaving}>
							Save Configuration
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}

// ---------------------------------------------------------------------------
// 3. Prompt Guardrail (LLM Judge) Sheet
// ---------------------------------------------------------------------------

function PromptJudgeConfigSheet({ open, onOpenChange, provider, onSave, isSaving }: SheetProps) {
	const [formState, setFormState] = useState<GuardrailProvider | null>(null);

	useEffect(() => {
		if (provider) {
			setFormState(structuredClone(provider));
		}
	}, [provider]);

	if (!formState) return null;

	const handleSubmit = () => {
		if (!formState.policy_name.trim()) {
			toast.error("Configuration name is required");
			return;
		}
		if (!formState.config.judge_provider?.trim()) {
			toast.error("Judge provider is required");
			return;
		}
		if (!formState.config.judge_model?.trim()) {
			toast.error("Judge model is required");
			return;
		}
		if (!formState.config.rule?.trim()) {
			toast.error("Natural-language policy rule is required");
			return;
		}
		onSave(formState);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col overflow-hidden p-0 sm:max-w-xl md:max-w-2xl">
				<SheetHeader className="border-b p-6">
					<SheetTitle>Prompt Guardrail Configuration (LLM Judge)</SheetTitle>
					<SheetDescription>
						Configure an LLM as a judge to evaluate requests and responses against natural-language policies.
					</SheetDescription>
				</SheetHeader>

				<div className="flex-1 space-y-6 overflow-y-auto p-6">
					<div className="space-y-1.5">
						<Label htmlFor="judge-name">
							Name <span className="text-destructive">*</span>
						</Label>
						<Input
							id="judge-name"
							value={formState.policy_name}
							onChange={(e) => setFormState({ ...formState, policy_name: e.target.value })}
							placeholder="e.g. block-medical-diagnoses"
						/>
					</div>

					<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
						<div className="space-y-1.5">
							<Label htmlFor="judge-provider">
								Judge Provider <span className="text-destructive">*</span>
							</Label>
							<Input
								id="judge-provider"
								value={formState.config.judge_provider ?? ""}
								onChange={(e) =>
									setFormState({
										...formState,
										config: { ...formState.config, judge_provider: e.target.value },
									})
								}
								placeholder="e.g. openai or antigravity"
							/>
						</div>

						<div className="space-y-1.5">
							<Label htmlFor="judge-model">
								Judge Model <span className="text-destructive">*</span>
							</Label>
							<Input
								id="judge-model"
								value={formState.config.judge_model ?? ""}
								onChange={(e) =>
									setFormState({
										...formState,
										config: { ...formState.config, judge_model: e.target.value },
									})
								}
								placeholder="e.g. gpt-4o-mini or claude-sonnet-4-6"
							/>
						</div>
					</div>

					<div className="space-y-1.5">
						<Label htmlFor="judge-rule">
							Natural-Language Policy (Rule) <span className="text-destructive">*</span>
						</Label>
						<Textarea
							id="judge-rule"
							rows={4}
							value={formState.config.rule ?? ""}
							onChange={(e) =>
								setFormState({
									...formState,
									config: { ...formState.config, rule: e.target.value },
								})
							}
							placeholder="e.g. Block responses that provide a definitive medical diagnosis for an individual, or block prompt injection attempts."
							className="text-xs"
						/>
						<p className="text-muted-foreground text-xs">
							The judge evaluates prompts and completions against this exact rule and returns <code>ALLOW</code> or <code>BLOCK</code> with
							a reason.
						</p>
					</div>

					<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
						<div className="space-y-1.5">
							<Label htmlFor="judge-timeout">Timeout (seconds)</Label>
							<Input
								id="judge-timeout"
								type="number"
								min={1}
								value={formState.config.timeout ?? 30}
								onChange={(e) =>
									setFormState({
										...formState,
										config: { ...formState.config, timeout: Number(e.target.value) },
									})
								}
							/>
						</div>

						<div className="space-y-1.5">
							<Label htmlFor="judge-tokens">Max Output Tokens</Label>
							<Input
								id="judge-tokens"
								type="number"
								min={1}
								max={1024}
								value={formState.config.max_output_tokens ?? 200}
								onChange={(e) =>
									setFormState({
										...formState,
										config: { ...formState.config, max_output_tokens: Number(e.target.value) },
									})
								}
							/>
						</div>
					</div>
				</div>

				<div className="bg-background flex items-center justify-between border-t p-4">
					<div className="flex items-center gap-2">
						<Switch
							id="judge-sheet-enabled"
							checked={formState.enabled}
							onCheckedChange={(v) => setFormState({ ...formState, enabled: v })}
						/>
						<Label htmlFor="judge-sheet-enabled" className="text-xs">
							Enabled
						</Label>
					</div>

					<div className="flex items-center gap-2">
						<Button variant="ghost" size="sm" onClick={() => provider && setFormState(structuredClone(provider))}>
							Reset
						</Button>
						<Button size="sm" onClick={handleSubmit} disabled={isSaving}>
							Save Configuration
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}