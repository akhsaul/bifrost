import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TagInput } from "@/components/ui/tagInput";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetPluginQuery, useUpdatePluginMutation } from "@/lib/store";
import {
	EXTRA_DETECTION_ENDPOINTS,
	EXTRA_DETECTION_MAX_MESSAGES_CAP,
	EXTRA_DETECTION_MODEL_PATTERN_TYPES,
	EXTRA_DETECTION_PLUGIN_NAME,
	EXTRA_DETECTION_PROVIDER_WILDCARD,
	blankExtraDetectionCountingRule,
	defaultExtraDetectionConfig,
	endpointLabel,
	extraDetectionCelPreview,
	extraDetectionCountingRuleSummary,
	extraDetectionMatchSummary,
	normalizeExtraDetectionTokenCounting,
	type ExtraDetectionCountingRule,
	type ExtraDetectionEndpoint,
	type ExtraDetectionModelPatternType,
	type ExtraDetectionPluginConfig,
	type ExtraDetectionRule,
	type ExtraDetectionTokenCounting,
} from "@/lib/types/extraDetection";
import { Info, MoreHorizontal, Pencil, Plus, Search, Tags, Trash2, Zap } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

function parseConfig(raw: unknown): ExtraDetectionPluginConfig {
	if (!raw || typeof raw !== "object") return defaultExtraDetectionConfig();
	const r = raw as Record<string, unknown>;
	const base = defaultExtraDetectionConfig();
	return {
		enabled: typeof r.enabled === "boolean" ? r.enabled : base.enabled,
		estimate_tokens: typeof r.estimate_tokens === "boolean" ? r.estimate_tokens : true,
		token_header: typeof r.token_header === "string" && r.token_header ? r.token_header : base.token_header,
		token_counting: (r.token_counting ?? {}) as ExtraDetectionTokenCounting,
		rules: Array.isArray(r.rules) ? (r.rules as ExtraDetectionRule[]) : [],
	};
}

function nextRuleId(rules: ExtraDetectionRule[]): number {
	const ids = rules.map((r) => r.id);
	return ids.length ? Math.max(...ids) + 1 : 1;
}

function blankRule(id: number): ExtraDetectionRule {
	return {
		id,
		name: "",
		description: "",
		enabled: true,
		header: "complexity_tier",
		value: "simple",
		max_messages_to_scan: 1,
		match: { scope: "last_user_messages", all_of: [] },
	};
}

interface RuleSheetState {
	open: boolean;
	rule: ExtraDetectionRule | null; // null = create mode
}

interface CountingSheetState {
	open: boolean;
	rule: ExtraDetectionCountingRule | null; // null = create mode
}

// readSecretValue pulls the current value out of a stored secret, which may be
// a bare string, an "env.NAME"/"vault.path" reference, or the {value, ref,
// type} object the Go SecretVar marshals to.
function readSecretValue(raw: unknown): string {
	if (typeof raw === "string") return raw;
	if (raw && typeof raw === "object") {
		const o = raw as { value?: string; ref?: string };
		return o.value?.trim() || o.ref?.trim() || "";
	}
	return "";
}

// writeSecretValue stores a plain key as a bare string, leaving an env/vault
// reference untouched so the operator's indirection survives an unrelated edit.
function writeSecretValue(current: unknown, next: string): unknown {
	const existingRef = current && typeof current === "object" ? (current as { ref?: string }).ref : undefined;
	if (existingRef?.trim() && !next.trim()) return current;
	return next;
}

export default function ExtraDetectionView() {
	const { data: plugin, isLoading } = useGetPluginQuery(EXTRA_DETECTION_PLUGIN_NAME);
	const [updatePlugin, { isLoading: isSaving }] = useUpdatePluginMutation();
	const [rules, setRules] = useState<ExtraDetectionRule[]>([]);
	const [pluginEnabled, setPluginEnabled] = useState(false);
	const [estimateTokens, setEstimateTokens] = useState(true);
	const [tokenCounting, setTokenCounting] = useState(() => normalizeExtraDetectionTokenCounting(undefined));
	const [sheet, setSheet] = useState<RuleSheetState>({ open: false, rule: null });
	const [countingSheet, setCountingSheet] = useState<CountingSheetState>({ open: false, rule: null });
	const [search, setSearch] = useState("");

	useEffect(() => {
		const cfg = parseConfig(plugin?.config);
		setRules(cfg.rules);
		setEstimateTokens(cfg.estimate_tokens);
		setTokenCounting(normalizeExtraDetectionTokenCounting(cfg.token_counting));
		setPluginEnabled(Boolean(plugin?.enabled));
	}, [plugin]);

	const persist = async (next: {
		rules?: ExtraDetectionRule[];
		enabled?: boolean;
		estimate_tokens?: boolean;
		token_counting?: ExtraDetectionTokenCounting;
	}) => {
		const cfg = parseConfig(plugin?.config);
		await updatePlugin({
			name: EXTRA_DETECTION_PLUGIN_NAME,
			data: {
				enabled: next.enabled ?? pluginEnabled,
				config: {
					...cfg,
					enabled: next.enabled ?? pluginEnabled,
					estimate_tokens: next.estimate_tokens ?? estimateTokens,
					token_header: cfg.token_header,
					token_counting: next.token_counting ?? tokenCounting,
					rules: next.rules ?? rules,
				} satisfies ExtraDetectionPluginConfig,
			},
		}).unwrap();
	};

	const handleTogglePlugin = async (newVal: boolean) => {
		setPluginEnabled(newVal);
		try {
			await persist({ enabled: newVal });
			toast.success(`Extra detection plugin ${newVal ? "enabled" : "disabled"}`);
		} catch (error) {
			setPluginEnabled(!newVal);
			toast.error(getErrorMessage(error));
		}
	};

	const handleToggleEstimate = async (newVal: boolean) => {
		setEstimateTokens(newVal);
		try {
			await persist({ estimate_tokens: newVal });
			toast.success(`Token counting ${newVal ? "enabled" : "disabled"}`);
		} catch (error) {
			setEstimateTokens(!newVal);
			toast.error(getErrorMessage(error));
		}
	};

	const persistCounting = async (next: ExtraDetectionTokenCounting) => {
		const merged = normalizeExtraDetectionTokenCounting(next);
		await persist({ token_counting: merged });
		setTokenCounting(merged);
	};

	const handleCountingFieldChange = async (patch: Partial<ExtraDetectionTokenCounting>) => {
		const previous = tokenCounting;
		const next = normalizeExtraDetectionTokenCounting({ ...tokenCounting, ...patch });
		setTokenCounting(next);
		try {
			await persistCounting(next);
		} catch (error) {
			setTokenCounting(previous);
			toast.error(getErrorMessage(error));
		}
	};

	const handleCountingKeyChange = async (endpoint: ExtraDetectionEndpoint, value: string) => {
		await handleCountingFieldChange({
			api_keys: { ...tokenCounting.api_keys, [endpoint]: writeSecretValue(tokenCounting.api_keys?.[endpoint], value) },
		});
	};

	const handleCountingBaseUrlChange = async (endpoint: ExtraDetectionEndpoint, value: string) => {
		const base_urls = { ...tokenCounting.base_urls };
		if (value.trim()) base_urls[endpoint] = value.trim();
		else delete base_urls[endpoint];
		await handleCountingFieldChange({ base_urls });
	};

	const handleCountingRuleSave = async (saved: ExtraDetectionCountingRule) => {
		const isNew = !tokenCounting.rules?.some((r) => r.id === saved.id);
		const next = isNew ? [...(tokenCounting.rules ?? []), saved] : (tokenCounting.rules ?? []).map((r) => (r.id === saved.id ? saved : r));
		try {
			await persistCounting({ ...tokenCounting, rules: next });
			setCountingSheet({ open: false, rule: null });
			toast.success(`Counting rule ${isNew ? "created" : "updated"}`);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleCountingRuleDelete = async (id: number) => {
		try {
			await persistCounting({ ...tokenCounting, rules: (tokenCounting.rules ?? []).filter((r) => r.id !== id) });
			toast.success("Counting rule deleted");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleToggleCountingRule = async (id: number, newVal: boolean) => {
		const next = (tokenCounting.rules ?? []).map((r) => (r.id === id ? { ...r, enabled: newVal } : r));
		try {
			await persistCounting({ ...tokenCounting, rules: next });
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleSheetSave = async (saved: ExtraDetectionRule) => {
		const isNew = !rules.some((r) => r.id === saved.id);
		const next = isNew ? [...rules, saved] : rules.map((r) => (r.id === saved.id ? saved : r));
		try {
			await persist({ rules: next });
			setRules(next);
			setSheet({ open: false, rule: null });
			toast.success(`Detection rule ${isNew ? "created" : "updated"}`);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleDelete = async (id: number) => {
		const next = rules.filter((r) => r.id !== id);
		try {
			await persist({ rules: next });
			setRules(next);
			toast.success("Detection rule deleted");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleToggleRule = async (id: number, newVal: boolean) => {
		const next = rules.map((r) => (r.id === id ? { ...r, enabled: newVal } : r));
		try {
			await persist({ rules: next });
			setRules(next);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleCreate = () => {
		setSheet({ open: true, rule: blankRule(nextRuleId(rules)) });
	};

	const handleEdit = (rule: ExtraDetectionRule) => {
		setSheet({ open: true, rule: structuredClone(rule) });
	};

	const handleCreateCountingRule = () => {
		const ids = (tokenCounting.rules ?? []).map((r) => r.id);
		setCountingSheet({ open: true, rule: blankExtraDetectionCountingRule(ids.length ? Math.max(...ids) + 1 : 1) });
	};

	const handleEditCountingRule = (rule: ExtraDetectionCountingRule) => {
		setCountingSheet({ open: true, rule: structuredClone(rule) });
	};

	const filteredRules = rules.filter((r) => (search.trim() ? r.name.toLowerCase().includes(search.toLowerCase()) : true));

	if (isLoading) {
		return <div className="text-muted-foreground p-8 text-sm">Loading extra detection…</div>;
	}

	return (
		<div className="space-y-4">
			{/* Page Header */}
			<div className="flex flex-wrap items-center justify-between gap-4">
				<div>
					<h1 className="text-xl font-bold tracking-tight">Extra Detection</h1>
					<p className="text-muted-foreground text-sm">
						Stamp headers from message patterns and token estimates — before routing runs, without embedding.
					</p>
				</div>
				<div className="flex items-center gap-3">
					<div className="flex items-center gap-2">
						<Switch checked={pluginEnabled} onCheckedChange={handleTogglePlugin} data-testid="extra-detection-plugin-enabled-switch" />
						<Label className="text-xs">Plugin enabled</Label>
					</div>
					<Button onClick={handleCreate} data-testid="extra-detection-rule-add">
						<Plus className="mr-1.5 h-4 w-4" /> Add Rule
					</Button>
				</div>
			</div>

			{/* Token counting card */}
			<div className="bg-card rounded-sm border">
				<div className="flex items-center justify-between gap-6 p-4">
					<div className="space-y-1">
						<div className="flex items-center gap-2">
							<Zap className="h-3.5 w-3.5" />
							<Label htmlFor="extra-detection-estimate-switch" className="text-sm font-medium">
								Token counting
							</Label>
						</div>
						<p className="text-muted-foreground max-w-3xl text-xs leading-relaxed">
							Asks a counting endpoint for the exact input token count and writes{" "}
							<code className="bg-muted rounded-sm px-1 py-0.5 font-mono text-[11px]">headers["estimated_tokens"]</code>, so routing rules
							can target it with e.g.{" "}
							<code className="bg-muted rounded-sm px-1 py-0.5 font-mono text-[11px]">int(headers["estimated_tokens"]) &gt; 4000</code>.
							Counting is opt-in per model: a request matching no rule below gets no estimate header and continues to routing untouched.
						</p>
					</div>
					<Switch
						id="extra-detection-estimate-switch"
						data-testid="extra-detection-estimate-switch"
						checked={estimateTokens}
						onCheckedChange={handleToggleEstimate}
					/>
				</div>

				{/* Endpoint credentials + tuning */}
				<div className="space-y-3 border-t p-4">
					<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
						{EXTRA_DETECTION_ENDPOINTS.map((endpoint) => (
							<div key={endpoint} className="space-y-2">
								<Label htmlFor={`extra-detection-counting-key-${endpoint}`} className="text-xs font-medium">
									{endpointLabel(endpoint)} API key
								</Label>
								<Input
									id={`extra-detection-counting-key-${endpoint}`}
									type="password"
									autoComplete="off"
									placeholder="sk-… or env.OPENAI_API_KEY"
									className="font-mono text-xs"
									value={readSecretValue(tokenCounting.api_keys?.[endpoint])}
									onChange={(e) => handleCountingKeyChange(endpoint, e.target.value)}
									data-testid={`extra-detection-counting-key-${endpoint}`}
								/>
								<Input
									placeholder="Base URL override (optional)"
									className="font-mono text-xs"
									value={tokenCounting.base_urls?.[endpoint] ?? ""}
									onChange={(e) => handleCountingBaseUrlChange(endpoint, e.target.value)}
									data-testid={`extra-detection-counting-base-url-${endpoint}`}
								/>
							</div>
						))}
					</div>
					<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
						<div className="space-y-2">
							<Label htmlFor="extra-detection-counting-timeout" className="text-xs font-medium">
								Timeout (ms)
							</Label>
							<Input
								id="extra-detection-counting-timeout"
								type="number"
								min={1}
								value={tokenCounting.timeout_ms}
								onChange={(e) => handleCountingFieldChange({ timeout_ms: Math.max(1, Number(e.target.value) || 1) })}
								data-testid="extra-detection-counting-timeout"
							/>
						</div>
						<div className="space-y-2">
							<Label htmlFor="extra-detection-counting-padding" className="text-xs font-medium">
								Padding ratio
							</Label>
							<Input
								id="extra-detection-counting-padding"
								type="number"
								min={0}
								step={0.01}
								value={tokenCounting.padding_ratio}
								onChange={(e) => handleCountingFieldChange({ padding_ratio: Math.max(0, Number(e.target.value) || 0) })}
								data-testid="extra-detection-counting-padding"
							/>
							<p className="text-muted-foreground text-[11px]">
								0 is honest — these endpoints count exactly. Raise it when count_model is a proxy for the request's real model.
							</p>
						</div>
					</div>
				</div>

				{/* Counting rules */}
				<div className="space-y-3 border-t p-4">
					<div className="flex flex-wrap items-center justify-between gap-2">
						<div>
							<p className="text-sm font-medium">Counting rules</p>
							<p className="text-muted-foreground text-xs">
								First enabled rule whose provider and model pattern match wins. A request matching no rule is simply not counted.
							</p>
						</div>
						<Button variant="outline" size="sm" onClick={handleCreateCountingRule} data-testid="extra-detection-counting-rule-add">
							<Plus className="mr-1.5 h-4 w-4" /> Add Counting Rule
						</Button>
					</div>
					<div className="bg-card rounded-md border">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead className="w-16">ID</TableHead>
									<TableHead>Name</TableHead>
									<TableHead className="w-[45%]">Matches / Counts As</TableHead>
									<TableHead className="w-24">Enabled</TableHead>
									<TableHead className="w-24 text-right">Actions</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{(tokenCounting.rules ?? []).length === 0 ? (
									<TableRow>
										<TableCell colSpan={5} className="text-muted-foreground h-24 text-center text-sm">
											No counting rules configured. Requests are not counted until you add one.
										</TableCell>
									</TableRow>
								) : (
									(tokenCounting.rules ?? []).map((rule) => (
										<TableRow
											key={rule.id}
											className="hover:bg-muted/50 cursor-pointer"
											onClick={() => handleEditCountingRule(rule)}
											data-testid={`extra-detection-counting-rule-row-${rule.id}`}
										>
											<TableCell className="font-mono text-xs">{rule.id}</TableCell>
											<TableCell>
												<div className="font-medium">{rule.name || "Untitled"}</div>
												{rule.description && <div className="text-muted-foreground max-w-xs truncate text-[11px]">{rule.description}</div>}
											</TableCell>
											<TableCell>
												<Badge variant="secondary" className="font-mono text-[11px] font-normal">
													{extraDetectionCountingRuleSummary(rule)}
												</Badge>
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<Switch
													checked={rule.enabled}
													onCheckedChange={(val) => handleToggleCountingRule(rule.id, val)}
													data-testid={`extra-detection-counting-rule-enabled-${rule.id}`}
												/>
											</TableCell>
											<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
												<DropdownMenu>
													<DropdownMenuTrigger asChild>
														<Button
															variant="ghost"
															size="icon"
															className="h-8 w-8"
															data-testid={`extra-detection-counting-rule-actions-${rule.id}`}
														>
															<MoreHorizontal className="h-4 w-4" />
														</Button>
													</DropdownMenuTrigger>
													<DropdownMenuContent align="end">
														<DropdownMenuItem
															onClick={() => handleEditCountingRule(rule)}
															data-testid={`extra-detection-counting-rule-edit-${rule.id}`}
														>
															<Pencil className="mr-2 h-3.5 w-3.5" /> Edit
														</DropdownMenuItem>
														<DropdownMenuItem
															className="text-destructive focus:text-destructive"
															onClick={() => handleCountingRuleDelete(rule.id)}
															data-testid={`extra-detection-counting-rule-delete-${rule.id}`}
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
			</div>

			<div className="bg-muted/50 flex items-start gap-2 rounded-md border p-3">
				<Info className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0" />
				<p className="text-muted-foreground text-xs leading-relaxed">
					Rules scan <strong>user messages only</strong> — assistant replies, tool calls and tool results are skipped and never counted.
					Matching rules write their header in order (later rules overwrite earlier ones for the same header). Header-based routing rules
					never trigger the complexity-router embedding classifier.
				</p>
			</div>

			{/* Search Filter */}
			<div className="relative max-w-sm">
				<Search className="text-muted-foreground absolute top-2.5 left-3 h-4 w-4" />
				<Input
					placeholder="Search by name..."
					value={search}
					onChange={(e) => setSearch(e.target.value)}
					className="h-9 pl-9 text-xs"
					data-testid="extra-detection-rules-search"
				/>
			</div>

			{/* Rules Table */}
			<div className="bg-card rounded-md border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead className="w-16">ID</TableHead>
							<TableHead>Rule Name</TableHead>
							<TableHead className="w-[30%]">Writes Header</TableHead>
							<TableHead>Match</TableHead>
							<TableHead className="w-28">Is Enabled</TableHead>
							<TableHead className="w-24 text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{filteredRules.length === 0 ? (
							<TableRow>
								<TableCell colSpan={6} className="text-muted-foreground h-32 text-center text-sm">
									{rules.length === 0 ? "No rules configured yet. Click Add Rule to create one." : "No matching rules found."}
								</TableCell>
							</TableRow>
						) : (
							filteredRules.map((rule) => (
								<TableRow
									key={rule.id}
									className="hover:bg-muted/50 cursor-pointer"
									onClick={() => handleEdit(rule)}
									data-testid={`extra-detection-rule-row-${rule.id}`}
								>
									<TableCell className="font-mono text-xs">{rule.id}</TableCell>
									<TableCell>
										<div className="font-medium">{rule.name || "Untitled"}</div>
										{rule.description && <div className="text-muted-foreground max-w-xs truncate text-[11px]">{rule.description}</div>}
									</TableCell>
									<TableCell>
										<code className="bg-muted/60 rounded px-1.5 py-0.5 font-mono text-xs">{extraDetectionCelPreview(rule)}</code>
									</TableCell>
									<TableCell>
										<Badge variant="secondary" className="text-[11px] font-normal">
											{extraDetectionMatchSummary(rule)}
										</Badge>
									</TableCell>
									<TableCell onClick={(e) => e.stopPropagation()}>
										<Switch
											checked={rule.enabled}
											onCheckedChange={(val) => handleToggleRule(rule.id, val)}
											data-testid={`extra-detection-rule-enabled-${rule.id}`}
										/>
									</TableCell>
									<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
										<DropdownMenu>
											<DropdownMenuTrigger asChild>
												<Button variant="ghost" size="icon" className="h-8 w-8" data-testid={`extra-detection-rule-actions-${rule.id}`}>
													<MoreHorizontal className="h-4 w-4" />
												</Button>
											</DropdownMenuTrigger>
											<DropdownMenuContent align="end">
												<DropdownMenuItem onClick={() => handleEdit(rule)} data-testid={`extra-detection-rule-edit-${rule.id}`}>
													<Pencil className="mr-2 h-3.5 w-3.5" /> Edit
												</DropdownMenuItem>
												<DropdownMenuItem
													className="text-destructive focus:text-destructive"
													onClick={() => handleDelete(rule.id)}
													data-testid={`extra-detection-rule-delete-${rule.id}`}
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

			<div className="text-muted-foreground text-xs">
				Showing {filteredRules.length} of {rules.length} rule(s)
			</div>

			{/* Slide-over Sheet: Rule Editor */}
			<Sheet open={sheet.open} onOpenChange={(open) => setSheet((s) => ({ ...s, open }))}>
				<SheetContent className="w-full overflow-y-auto sm:max-w-xl">
					<SheetHeader>
						<SheetTitle className="flex items-center gap-2">
							<Tags className="h-4 w-4" />
							{sheet.rule && rules.some((r) => r.id === sheet.rule!.id) ? "Edit detection rule" : "New detection rule"}
						</SheetTitle>
						<SheetDescription>Match user messages and write a header that routing rules can target.</SheetDescription>
					</SheetHeader>
					{sheet.rule && (
						<RuleForm
							key={sheet.rule.id}
							initial={sheet.rule}
							isSaving={isSaving}
							onCancel={() => setSheet({ open: false, rule: null })}
							onSave={handleSheetSave}
						/>
					)}
				</SheetContent>
			</Sheet>

			{/* Slide-over Sheet: Counting Rule Editor */}
			<Sheet open={countingSheet.open} onOpenChange={(open) => setCountingSheet((s) => ({ ...s, open }))}>
				<SheetContent className="w-full overflow-y-auto sm:max-w-xl">
					<SheetHeader>
						<SheetTitle className="flex items-center gap-2">
							<Zap className="h-4 w-4" />
							{countingSheet.rule && (tokenCounting.rules ?? []).some((r) => r.id === countingSheet.rule!.id)
								? "Edit counting rule"
								: "New counting rule"}
						</SheetTitle>
						<SheetDescription>
							Send matching requests to a counting endpoint and stamp the exact input token count for routing rules to target.
						</SheetDescription>
					</SheetHeader>
					{countingSheet.rule && (
						<CountingRuleForm
							key={countingSheet.rule.id}
							initial={countingSheet.rule}
							isSaving={isSaving}
							onCancel={() => setCountingSheet({ open: false, rule: null })}
							onSave={handleCountingRuleSave}
						/>
					)}
				</SheetContent>
			</Sheet>
		</div>
	);
}

function CountingRuleForm({
	initial,
	isSaving,
	onCancel,
	onSave,
}: {
	initial: ExtraDetectionCountingRule;
	isSaving: boolean;
	onCancel: () => void;
	onSave: (rule: ExtraDetectionCountingRule) => void;
}) {
	const [draft, setDraft] = useState<ExtraDetectionCountingRule>(structuredClone(initial));
	const [error, setError] = useState<string | null>(null);

	const set = <K extends keyof ExtraDetectionCountingRule>(key: K, value: ExtraDetectionCountingRule[K]) =>
		setDraft((d) => ({ ...d, [key]: value }));

	const validate = (): string | null => {
		if (!draft.name.trim()) return "Rule name is required";
		if (!draft.model_pattern.trim()) return "Model pattern is required";
		if (!draft.count_model.trim()) return "Count model is required — it is the model name sent to the counting endpoint";
		if (draft.model_pattern_type === "regex") {
			try {
				new RegExp(draft.model_pattern.trim());
			} catch {
				return "Model pattern is not a valid regular expression";
			}
		}
		return null;
	};

	return (
		<div className="space-y-4 py-4">
			<div className="space-y-2">
				<Label>Rule name *</Label>
				<Input
					value={draft.name}
					onChange={(e) => set("name", e.target.value)}
					placeholder="e.g. openrouter gpt-5 → OpenAI"
					data-testid="extra-detection-counting-rule-sheet-name"
				/>
			</div>
			<div className="space-y-2">
				<Label>Description</Label>
				<Textarea
					value={draft.description ?? ""}
					onChange={(e) => set("description", e.target.value)}
					placeholder="What this rule counts"
					rows={2}
					data-testid="extra-detection-counting-rule-sheet-desc"
				/>
			</div>
			<div className="flex items-center justify-between gap-4">
				<Label>Enabled</Label>
				<Switch
					checked={draft.enabled}
					onCheckedChange={(v) => set("enabled", v)}
					data-testid="extra-detection-counting-rule-sheet-enabled"
				/>
			</div>
			<div className="grid grid-cols-2 gap-3">
				<div className="space-y-2">
					<Label>Provider *</Label>
					<Input
						value={draft.provider}
						onChange={(e) => set("provider", e.target.value)}
						placeholder="* or openrouter"
						className="font-mono text-xs"
						data-testid="extra-detection-counting-rule-sheet-provider"
					/>
					<p className="text-muted-foreground text-[11px] leading-relaxed">
						Matched against the request&apos;s provider, or <code className="font-mono">*</code> for any. When the request carries no
						provider, the prefix on the model string is used.
					</p>
				</div>
				<div className="space-y-2">
					<Label>Endpoint *</Label>
					<Select value={draft.endpoint} onValueChange={(v) => set("endpoint", v as ExtraDetectionEndpoint)}>
						<SelectTrigger data-testid="extra-detection-counting-rule-sheet-endpoint">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{EXTRA_DETECTION_ENDPOINTS.map((e) => (
								<SelectItem key={e} value={e}>
									{endpointLabel(e)}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<p className="text-muted-foreground text-[11px] leading-relaxed">
						{draft.endpoint === "openai" ? "POST /v1/responses/input_tokens" : "POST /v1beta/models/{count_model}:countTokens"}
					</p>
				</div>
			</div>
			<div className="grid grid-cols-3 gap-3">
				<div className="col-span-2 space-y-2">
					<Label>Model pattern *</Label>
					<Input
						value={draft.model_pattern}
						onChange={(e) => set("model_pattern", e.target.value)}
						placeholder="gemini-* or openrouter/*"
						className="font-mono text-xs"
						data-testid="extra-detection-counting-rule-sheet-model-pattern"
					/>
				</div>
				<div className="space-y-2">
					<Label>Pattern type</Label>
					<Select
						value={draft.model_pattern_type ?? "glob"}
						onValueChange={(v) => set("model_pattern_type", v as ExtraDetectionModelPatternType)}
					>
						<SelectTrigger data-testid="extra-detection-counting-rule-sheet-pattern-type">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{EXTRA_DETECTION_MODEL_PATTERN_TYPES.map((t) => (
								<SelectItem key={t} value={t}>
									{t}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				</div>
			</div>
			<div className="space-y-2">
				<Label>Count model *</Label>
				<Input
					value={draft.count_model}
					onChange={(e) => set("count_model", e.target.value)}
					placeholder="gemini-3-flash"
					className="font-mono text-xs"
					data-testid="extra-detection-counting-rule-sheet-count-model"
				/>
				<p className="text-muted-foreground text-[11px] leading-relaxed">
					The model name sent to the counting endpoint, verbatim. Set it per model: aggregators rename and suffix models (
					&quot;gemini-3.8-flash-free&quot;, &quot;gpt-5-preview&quot;), and only you know which upstream model tokenizes acceptably for a
					given request shape.
				</p>
			</div>
			<div className="bg-muted/60 rounded-md border p-3">
				<p className="text-muted-foreground text-[11px]">Summary</p>
				<code className="font-mono text-xs">
					{extraDetectionCountingRuleSummary({ ...draft, provider: draft.provider || EXTRA_DETECTION_PROVIDER_WILDCARD })}
				</code>
			</div>
			{error && (
				<p role="alert" className="text-destructive text-xs" data-testid="extra-detection-counting-rule-sheet-error">
					{error}
				</p>
			)}
			<div className="flex justify-end gap-2">
				<Button variant="outline" size="sm" onClick={onCancel} data-testid="extra-detection-counting-rule-sheet-cancel">
					Cancel
				</Button>
				<Button
					size="sm"
					disabled={isSaving}
					data-testid="extra-detection-counting-rule-sheet-save"
					onClick={() => {
						const err = validate();
						if (err) {
							setError(err);
							return;
						}
						setError(null);
						onSave({
							...draft,
							name: draft.name.trim(),
							description: draft.description?.trim() || undefined,
							provider: draft.provider.trim().toLowerCase() || EXTRA_DETECTION_PROVIDER_WILDCARD,
							model_pattern: draft.model_pattern.trim(),
							model_pattern_type: draft.model_pattern_type ?? "glob",
							count_model: draft.count_model.trim(),
						});
					}}
				>
					Save rule
				</Button>
			</div>
		</div>
	);
}

function RuleForm({
	initial,
	isSaving,
	onCancel,
	onSave,
}: {
	initial: ExtraDetectionRule;
	isSaving: boolean;
	onCancel: () => void;
	onSave: (rule: ExtraDetectionRule) => void;
}) {
	const [draft, setDraft] = useState<ExtraDetectionRule>(structuredClone(initial));
	const [error, setError] = useState<string | null>(null);

	const set = <K extends keyof ExtraDetectionRule>(key: K, value: ExtraDetectionRule[K]) => setDraft((d) => ({ ...d, [key]: value }));
	const setMatch = (patch: Partial<ExtraDetectionRule["match"]>) => setDraft((d) => ({ ...d, match: { ...d.match, ...patch } }));

	const validate = (): string | null => {
		if (!draft.name.trim()) return "Rule name is required";
		if (!draft.header.trim()) return "Header is required";
		if (!/^[a-z0-9][a-z0-9_-]*$/i.test(draft.header.trim())) return "Header may only contain letters, numbers, - and _";
		if (!draft.value.trim()) return "Value is required";
		if (draft.value.trim().length > 64) return "Value must be 64 characters or less";
		const n = draft.max_messages_to_scan ?? 1;
		if (!Number.isInteger(n) || n < 1 || n > EXTRA_DETECTION_MAX_MESSAGES_CAP)
			return `Max messages to scan must be between 1 and ${EXTRA_DETECTION_MAX_MESSAGES_CAP}`;
		const m = draft.match;
		if (!(m.all_of?.length || m.any_of?.length || m.starts_with?.length))
			return "Add at least one match condition (contains all / contains any / starts with)";
		return null;
	};

	const preview: ExtraDetectionRule = {
		...draft,
		header: draft.header.trim().toLowerCase() || "header",
		value: draft.value.trim() || "value",
	};

	return (
		<div className="space-y-4 py-4">
			<div className="space-y-2">
				<Label>Rule name *</Label>
				<Input
					value={draft.name}
					onChange={(e) => set("name", e.target.value)}
					placeholder="e.g. git commit → simple"
					data-testid="extra-detection-rule-sheet-name"
				/>
			</div>
			<div className="space-y-2">
				<Label>Description</Label>
				<Textarea
					value={draft.description ?? ""}
					onChange={(e) => set("description", e.target.value)}
					placeholder="What this rule detects"
					rows={2}
					data-testid="extra-detection-rule-sheet-desc"
				/>
			</div>
			<div className="flex items-center justify-between gap-4">
				<Label>Enabled</Label>
				<Switch checked={draft.enabled} onCheckedChange={(v) => set("enabled", v)} data-testid="extra-detection-rule-sheet-enabled" />
			</div>
			<div className="grid grid-cols-2 gap-3">
				<div className="space-y-2">
					<Label>Header *</Label>
					<Input
						value={draft.header}
						onChange={(e) => set("header", e.target.value)}
						placeholder="complexity_tier"
						className="font-mono text-xs"
						data-testid="extra-detection-rule-sheet-header"
					/>
				</div>
				<div className="space-y-2">
					<Label>Value *</Label>
					<Input
						value={draft.value}
						onChange={(e) => set("value", e.target.value)}
						placeholder="simple"
						className="font-mono text-xs"
						data-testid="extra-detection-rule-sheet-value"
					/>
				</div>
			</div>
			<div className="grid grid-cols-2 gap-3">
				<div className="space-y-2">
					<Label>Max messages to scan</Label>
					<Input
						type="number"
						min={1}
						max={EXTRA_DETECTION_MAX_MESSAGES_CAP}
						value={draft.max_messages_to_scan ?? 1}
						onChange={(e) => set("max_messages_to_scan", Math.max(1, Number(e.target.value) || 1))}
						data-testid="extra-detection-rule-sheet-max-messages"
					/>
					<p className="text-muted-foreground text-[11px] leading-relaxed">
						1 = last user message only. 2+ = also check previous user messages (assistant replies and tool results are skipped, never
						counted).
					</p>
				</div>
				<div className="space-y-2">
					<Label>Scope</Label>
					<Select
						value={draft.match.scope ?? "last_user_messages"}
						onValueChange={(v) => setMatch({ scope: v as "last_user_messages" | "full_input" })}
					>
						<SelectTrigger data-testid="extra-detection-rule-sheet-scope">
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="last_user_messages">Last user messages</SelectItem>
							<SelectItem value="full_input">Full input</SelectItem>
						</SelectContent>
					</Select>
					<p className="text-muted-foreground text-[11px] leading-relaxed">
						Full input also scans system prompts and every user message in the conversation.
					</p>
				</div>
			</div>
			<div className="space-y-2">
				<Label>Contains all of</Label>
				<TagInput
					value={draft.match.all_of ?? []}
					onValueChange={(v) => setMatch({ all_of: v })}
					placeholder="Type a keyword and press Enter"
					data-testid="extra-detection-rule-sheet-all-of"
				/>
				<p className="text-muted-foreground text-[11px]">Every keyword must appear (e.g. git + commit).</p>
			</div>
			<div className="space-y-2">
				<Label>Contains any of</Label>
				<TagInput
					value={draft.match.any_of ?? []}
					onValueChange={(v) => setMatch({ any_of: v })}
					placeholder="Type a keyword and press Enter"
					data-testid="extra-detection-rule-sheet-any-of"
				/>
			</div>
			<div className="space-y-2">
				<Label>Starts with</Label>
				<TagInput
					value={draft.match.starts_with ?? []}
					onValueChange={(v) => setMatch({ starts_with: v })}
					placeholder="Type a prefix and press Enter"
					data-testid="extra-detection-rule-sheet-starts-with"
				/>
				<p className="text-muted-foreground text-[11px]">Message starts with any prefix (e.g. question).</p>
			</div>
			<div className="bg-muted/60 rounded-md border p-3">
				<p className="text-muted-foreground text-[11px]">Routing rule target</p>
				<code className="font-mono text-xs">{extraDetectionCelPreview(preview)}</code>
				<p className="text-muted-foreground mt-1 text-[11px]">
					{extraDetectionMatchSummary({ ...preview, max_messages_to_scan: draft.max_messages_to_scan })}
				</p>
			</div>
			{error && (
				<p role="alert" className="text-destructive text-xs" data-testid="extra-detection-rule-sheet-error">
					{error}
				</p>
			)}
			<div className="flex justify-end gap-2">
				<Button variant="outline" size="sm" onClick={onCancel} data-testid="extra-detection-rule-sheet-cancel">
					Cancel
				</Button>
				<Button
					size="sm"
					disabled={isSaving}
					data-testid="extra-detection-rule-sheet-save"
					onClick={() => {
						const err = validate();
						if (err) {
							setError(err);
							return;
						}
						setError(null);
						onSave({
							...draft,
							name: draft.name.trim(),
							description: draft.description?.trim() || undefined,
							header: draft.header.trim().toLowerCase(),
							value: draft.value.trim(),
							match: {
								scope: draft.match.scope ?? "last_user_messages",
								all_of: (draft.match.all_of ?? []).map((s) => s.trim()).filter(Boolean),
								any_of: (draft.match.any_of ?? []).map((s) => s.trim()).filter(Boolean),
								starts_with: (draft.match.starts_with ?? []).map((s) => s.trim()).filter(Boolean),
							},
						});
					}}
				>
					Save rule
				</Button>
			</div>
		</div>
	);
}