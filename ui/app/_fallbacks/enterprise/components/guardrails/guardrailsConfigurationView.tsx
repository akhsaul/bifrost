import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetPluginQuery, useUpdatePluginMutation } from "@/lib/store";
import {
	GUARDRAILS_PLUGIN_NAME,
	defaultGuardrailsConfig,
	type GuardrailProvider,
	type GuardrailRule,
	type GuardrailsPluginConfig,
} from "@/lib/types/guardrails";
import { cn } from "@/lib/utils";
import {
	Boxes,
	ChevronDown,
	Info,
	Key,
	MoreHorizontal,
	Pencil,
	Plus,
	Search,
	ShieldCheck,
	Sparkles,
	Trash2,
	Wrench,
	X,
} from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

function parseConfig(raw: unknown): GuardrailsPluginConfig {
	if (!raw || typeof raw !== "object") return defaultGuardrailsConfig();
	const r = raw as Record<string, unknown>;
	return {
		guardrail_providers: Array.isArray(r.guardrail_providers) ? (r.guardrail_providers as GuardrailProvider[]) : [],
		guardrail_rules: Array.isArray(r.guardrail_rules) ? (r.guardrail_rules as GuardrailRule[]) : [],
	};
}

function nextRuleId(rules: GuardrailRule[]): number {
	const ids = rules.map((r) => r.id);
	return ids.length ? Math.max(...ids) + 1 : 1;
}

interface RuleSheetState {
	open: boolean;
	rule: GuardrailRule | null; // null = create mode
}

export default function GuardrailsConfigurationView() {
	const { data: plugin, isLoading } = useGetPluginQuery(GUARDRAILS_PLUGIN_NAME);
	const [updatePlugin, { isLoading: isSaving }] = useUpdatePluginMutation();
	const [rules, setRules] = useState<GuardrailRule[]>([]);
	const [pluginEnabled, setPluginEnabled] = useState(false);
	const [sheet, setSheet] = useState<RuleSheetState>({ open: false, rule: null });
	const [search, setSearch] = useState("");

	useEffect(() => {
		const cfg = parseConfig(plugin?.config);
		setRules(cfg.guardrail_rules);
		setPluginEnabled(Boolean(plugin?.enabled));
	}, [plugin]);

	const providers = parseConfig(plugin?.config).guardrail_providers;

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

	const handleSheetSave = async (saved: GuardrailRule) => {
		const isNew = sheet.rule === null;
		let next: GuardrailRule[];
		if (isNew) {
			next = [...rules, saved];
		} else {
			next = rules.map((r) => (r.id === saved.id ? saved : r));
		}
		try {
			const cfg = parseConfig(plugin?.config);
			await updatePlugin({
				name: GUARDRAILS_PLUGIN_NAME,
				data: { enabled: pluginEnabled, config: { ...cfg, guardrail_rules: next } satisfies GuardrailsPluginConfig },
			}).unwrap();
			setRules(next);
			setSheet({ open: false, rule: null });
			toast.success(`Guardrail rule ${isNew ? "created" : "updated"}`);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleDelete = async (id: number) => {
		const next = rules.filter((r) => r.id !== id);
		try {
			const cfg = parseConfig(plugin?.config);
			await updatePlugin({
				name: GUARDRAILS_PLUGIN_NAME,
				data: { enabled: pluginEnabled, config: { ...cfg, guardrail_rules: next } satisfies GuardrailsPluginConfig },
			}).unwrap();
			setRules(next);
			toast.success("Guardrail rule deleted");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleToggleRule = async (id: number, newVal: boolean) => {
		const next = rules.map((r) => (r.id === id ? { ...r, enabled: newVal } : r));
		try {
			const cfg = parseConfig(plugin?.config);
			await updatePlugin({
				name: GUARDRAILS_PLUGIN_NAME,
				data: { enabled: pluginEnabled, config: { ...cfg, guardrail_rules: next } satisfies GuardrailsPluginConfig },
			}).unwrap();
			setRules(next);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleCreate = () => {
		setSheet({
			open: true,
			rule: {
				id: nextRuleId(rules),
				name: "",
				description: "",
				enabled: true,
				target: "llm",
				cel_expression: "true",
				apply_to: "both",
				sampling_rate: 100,
				timeout: 60,
				max_turns_to_send: 0,
				provider_config_ids: providers.length > 0 ? [providers[0].id] : [],
			},
		});
	};

	const handleEdit = (rule: GuardrailRule) => {
		setSheet({ open: true, rule: structuredClone(rule) });
	};

	const filteredRules = rules.filter((r) => (search.trim() ? r.name.toLowerCase().includes(search.toLowerCase()) : true));

	if (isLoading) {
		return <div className="text-muted-foreground p-8 text-sm">Loading guardrails…</div>;
	}

	return (
		<div className="space-y-4">
			{/* Page Header */}
			<div className="flex flex-wrap items-center justify-between gap-4">
				<div>
					<h1 className="text-xl font-bold tracking-tight">Guardrail Rules</h1>
					<p className="text-muted-foreground text-sm">Configure guardrail rules to control when to execute guardrails.</p>
				</div>
				<div className="flex items-center gap-3">
					<div className="flex items-center gap-2">
						<Switch checked={pluginEnabled} onCheckedChange={handleTogglePlugin} data-testid="guardrails-rules-plugin-enabled-switch" />
						<Label className="text-xs">Plugin enabled</Label>
					</div>
					<Button onClick={handleCreate} data-testid="guardrails-rule-add">
						<Plus className="mr-1.5 h-4 w-4" /> Add Rule
					</Button>
				</div>
			</div>

			{/* Search Filter */}
			<div className="relative max-w-sm">
				<Search className="text-muted-foreground absolute top-2.5 left-3 h-4 w-4" />
				<Input
					placeholder="Search by name..."
					value={search}
					onChange={(e) => setSearch(e.target.value)}
					className="h-9 pl-9 text-xs"
					data-testid="guardrails-rules-search"
				/>
			</div>

			{/* Rules Table */}
			<div className="bg-card rounded-md border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead className="w-16">ID</TableHead>
							<TableHead>Rule Name</TableHead>
							<TableHead className="w-24">Target</TableHead>
							<TableHead className="w-24">Apply on</TableHead>
							<TableHead className="w-[30%]">Expression</TableHead>
							<TableHead>Guardrail Profiles</TableHead>
							<TableHead className="w-28">Is Enabled</TableHead>
							<TableHead className="w-24 text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{filteredRules.length === 0 ? (
							<TableRow>
								<TableCell colSpan={8} className="text-muted-foreground h-32 text-center text-sm">
									{rules.length === 0 ? "No rules configured yet. Click Add Rule to create one." : "No matching rules found."}
								</TableCell>
							</TableRow>
						) : (
							filteredRules.map((rule) => (
								<TableRow
									key={rule.id}
									className="hover:bg-muted/50 cursor-pointer"
									onClick={() => handleEdit(rule)}
									data-testid={`guardrails-rule-row-${rule.id}`}
								>
									<TableCell className="font-mono text-xs">{rule.id}</TableCell>
									<TableCell>
										<div className="font-medium">{rule.name || "Untitled"}</div>
										{rule.description && <div className="text-muted-foreground max-w-xs truncate text-[11px]">{rule.description}</div>}
									</TableCell>
									<TableCell>
										<Badge variant="outline" className="font-mono text-[11px] uppercase">
											{rule.target || "llm"}
										</Badge>
									</TableCell>
									<TableCell>
										<Badge variant="secondary" className="text-[11px] capitalize">
											{rule.apply_to}
										</Badge>
									</TableCell>
									<TableCell>
										<code className="bg-muted/60 rounded px-1.5 py-0.5 font-mono text-xs">{rule.cel_expression || "true"}</code>
									</TableCell>
									<TableCell>
										<div className="flex flex-wrap gap-1">
											{rule.provider_config_ids.map((pid) => {
												const p = providers.find((prov) => prov.id === pid);
												return (
													<Badge key={pid} variant="outline" className="text-[11px] font-normal">
														{p ? p.policy_name : `#${pid}`}
													</Badge>
												);
											})}
										</div>
									</TableCell>
									<TableCell onClick={(e) => e.stopPropagation()}>
										<Switch
											checked={rule.enabled}
											onCheckedChange={(val) => handleToggleRule(rule.id, val)}
											data-testid={`guardrails-rule-enabled-${rule.id}`}
										/>
									</TableCell>
									<TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
										<DropdownMenu>
											<DropdownMenuTrigger asChild>
												<Button variant="ghost" size="icon" className="h-8 w-8" data-testid={`guardrails-rule-actions-${rule.id}`}>
													<MoreHorizontal className="h-4 w-4" />
												</Button>
											</DropdownMenuTrigger>
											<DropdownMenuContent align="end">
												<DropdownMenuItem onClick={() => handleEdit(rule)} data-testid={`guardrails-rule-edit-${rule.id}`}>
													<Pencil className="mr-2 h-3.5 w-3.5" /> Edit
												</DropdownMenuItem>
												<DropdownMenuItem
													className="text-destructive focus:text-destructive"
													onClick={() => handleDelete(rule.id)}
													data-testid={`guardrails-rule-delete-${rule.id}`}
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
			<RuleSheet
				open={sheet.open}
				onOpenChange={(open) => !open && setSheet({ open: false, rule: null })}
				rule={sheet.rule}
				providers={providers}
				onSave={handleSheetSave}
				isSaving={isSaving}
			/>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Slide-over Sheet Component matching ui-guardrail-rule-target-selection.png
// ---------------------------------------------------------------------------

interface RuleSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	rule: GuardrailRule | null;
	providers: GuardrailProvider[];
	onSave: (rule: GuardrailRule) => void;
	isSaving: boolean;
}

function RuleSheet({ open, onOpenChange, rule, providers, onSave, isSaving }: RuleSheetProps) {
	const [formState, setFormState] = useState<GuardrailRule | null>(null);

	useEffect(() => {
		if (rule) {
			setFormState(structuredClone(rule));
		}
	}, [rule]);

	if (!formState) return null;

	const isEdit = rule && rule.name !== "";

	const toggleProvider = (id: number) => {
		const has = formState.provider_config_ids.includes(id);
		const ids = has ? formState.provider_config_ids.filter((pid) => pid !== id) : [...formState.provider_config_ids, id];
		setFormState({ ...formState, provider_config_ids: ids });
	};

	const removeProvider = (id: number) => {
		setFormState({
			...formState,
			provider_config_ids: formState.provider_config_ids.filter((pid) => pid !== id),
		});
	};

	const handleSubmit = () => {
		if (!formState.name.trim()) {
			toast.error("Rule Name is required");
			return;
		}
		if (formState.provider_config_ids.length === 0) {
			toast.error("Please select at least one Guardrail Profile");
			return;
		}
		onSave(formState);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col overflow-hidden p-0 sm:max-w-xl md:max-w-2xl">
				{/* Sheet Header */}
				<SheetHeader className="border-b p-6">
					<SheetTitle>{isEdit ? "Edit Guardrail Rule" : "Add Guardrail Rule"}</SheetTitle>
					<SheetDescription>
						Create custom filtering rules using Common Expression Language (CEL) expressions to control when to execute guardrails.
					</SheetDescription>
				</SheetHeader>

				{/* Sheet Scrollable Body */}
				<div className="flex-1 space-y-6 overflow-y-auto p-6">
					{/* Rule Name Field */}
					<div className="space-y-1.5">
						<Label htmlFor="rule-name" className="text-xs font-medium">
							Rule Name <span className="text-destructive">*</span>
						</Label>
						<Input
							id="rule-name"
							value={formState.name}
							onChange={(e) => setFormState({ ...formState, name: e.target.value })}
							placeholder="e.g. gpt-5.4-testinggg"
							data-testid="guardrails-rule-sheet-name"
						/>
					</div>

					{/* Description Field */}
					<div className="space-y-1.5">
						<Label htmlFor="rule-desc" className="text-muted-foreground text-xs font-medium">
							Description
						</Label>
						<Textarea
							id="rule-desc"
							rows={2}
							value={formState.description ?? ""}
							onChange={(e) => setFormState({ ...formState, description: e.target.value })}
							placeholder="Describe what this rule does..."
							className="text-xs"
							data-testid="guardrails-rule-sheet-desc"
						/>
					</div>

					{/* Enable Rule Toggle Card */}
					<div className="bg-card flex items-center justify-between rounded-lg border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="rule-sheet-enabled" className="text-sm font-medium">
								Enable Rule
							</Label>
							<p className="text-muted-foreground text-xs">Rule will be active and applied to matching requests</p>
						</div>
						<Switch
							id="rule-sheet-enabled"
							checked={formState.enabled}
							onCheckedChange={(v) => setFormState({ ...formState, enabled: v })}
							data-testid="guardrails-rule-sheet-enabled"
						/>
					</div>

					{/* Target Selector: "Guardrail applies to" (Radio Cards) */}
					<div className="space-y-2">
						<Label className="text-muted-foreground text-xs font-semibold tracking-wider uppercase">Guardrail applies to</Label>
						<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
							{/* Card 1: LLM */}
							<div
								onClick={() => setFormState({ ...formState, target: "llm" })}
								className={cn(
									"cursor-pointer rounded-lg border p-4 transition-all flex items-start gap-3 select-none",
									(formState.target ?? "llm") === "llm" ? "border-primary bg-primary/5 ring-1 ring-primary" : "hover:bg-muted/40",
								)}
								data-testid="guardrails-target-llm"
							>
								<div
									className={cn(
										"h-4 w-4 rounded-full border flex items-center justify-center mt-0.5 shrink-0",
										(formState.target ?? "llm") === "llm" ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground",
									)}
								>
									{(formState.target ?? "llm") === "llm" && <div className="bg-background h-1.5 w-1.5 rounded-full" />}
								</div>
								<div className="space-y-1">
									<div className="flex items-center gap-1.5 text-sm font-medium">
										<Sparkles className="text-primary h-3.5 w-3.5" />
										LLM requests and responses
									</div>
									<p className="text-muted-foreground text-xs">Inspect prompts, conversation messages, and model outputs</p>
								</div>
							</div>

							{/* Card 2: MCP */}
							<div
								onClick={() => setFormState({ ...formState, target: "mcp" })}
								className={cn(
									"cursor-pointer rounded-lg border p-4 transition-all flex items-start gap-3 select-none",
									formState.target === "mcp" ? "border-primary bg-primary/5 ring-1 ring-primary" : "hover:bg-muted/40",
								)}
								data-testid="guardrails-target-mcp"
							>
								<div
									className={cn(
										"h-4 w-4 rounded-full border flex items-center justify-center mt-0.5 shrink-0",
										formState.target === "mcp" ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground",
									)}
								>
									{formState.target === "mcp" && <div className="bg-background h-1.5 w-1.5 rounded-full" />}
								</div>
								<div className="space-y-1">
									<div className="flex items-center gap-1.5 text-sm font-medium">
										<Wrench className="text-primary h-3.5 w-3.5" />
										MCP tool calls and results
									</div>
									<p className="text-muted-foreground text-xs">Inspect tool arguments before execution and returned results</p>
								</div>
							</div>
						</div>
					</div>

					{/* Execution Phase Selector: "Apply on" (3 Radio Cards) */}
					<div className="space-y-2">
						<Label className="text-muted-foreground text-xs font-semibold tracking-wider uppercase">Apply on</Label>
						<div className="grid grid-cols-1 gap-3 md:grid-cols-3">
							{/* Input Only */}
							<div
								onClick={() => setFormState({ ...formState, apply_to: "input" })}
								className={cn(
									"cursor-pointer rounded-lg border p-3 transition-all flex items-start gap-2.5 select-none",
									formState.apply_to === "input" ? "border-primary bg-primary/5 ring-1 ring-primary" : "hover:bg-muted/40",
								)}
								data-testid="guardrails-apply-input"
							>
								<div
									className={cn(
										"h-4 w-4 rounded-full border flex items-center justify-center mt-0.5 shrink-0",
										formState.apply_to === "input" ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground",
									)}
								>
									{formState.apply_to === "input" && <div className="bg-background h-1.5 w-1.5 rounded-full" />}
								</div>
								<div className="space-y-0.5">
									<div className="text-xs font-medium">Input Only</div>
									<p className="text-muted-foreground text-[11px]">Evaluate on incoming requests</p>
								</div>
							</div>

							{/* Output Only */}
							<div
								onClick={() => setFormState({ ...formState, apply_to: "output" })}
								className={cn(
									"cursor-pointer rounded-lg border p-3 transition-all flex items-start gap-2.5 select-none",
									formState.apply_to === "output" ? "border-primary bg-primary/5 ring-1 ring-primary" : "hover:bg-muted/40",
								)}
								data-testid="guardrails-apply-output"
							>
								<div
									className={cn(
										"h-4 w-4 rounded-full border flex items-center justify-center mt-0.5 shrink-0",
										formState.apply_to === "output" ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground",
									)}
								>
									{formState.apply_to === "output" && <div className="bg-background h-1.5 w-1.5 rounded-full" />}
								</div>
								<div className="space-y-0.5">
									<div className="text-xs font-medium">Output Only</div>
									<p className="text-muted-foreground text-[11px]">Evaluate on outgoing responses</p>
								</div>
							</div>

							{/* Both */}
							<div
								onClick={() => setFormState({ ...formState, apply_to: "both" })}
								className={cn(
									"cursor-pointer rounded-lg border p-3 transition-all flex items-start gap-2.5 select-none",
									formState.apply_to === "both" ? "border-primary bg-primary/5 ring-1 ring-primary" : "hover:bg-muted/40",
								)}
								data-testid="guardrails-apply-both"
							>
								<div
									className={cn(
										"h-4 w-4 rounded-full border flex items-center justify-center mt-0.5 shrink-0",
										formState.apply_to === "both" ? "border-primary bg-primary text-primary-foreground" : "border-muted-foreground",
									)}
								>
									{formState.apply_to === "both" && <div className="bg-background h-1.5 w-1.5 rounded-full" />}
								</div>
								<div className="space-y-0.5">
									<div className="text-xs font-medium">Both</div>
									<p className="text-muted-foreground text-[11px]">Evaluate on both input and output</p>
								</div>
							</div>
						</div>
					</div>

					{/* Guardrail Profiles Selector */}
					<div className="space-y-2">
						<div className="flex items-center gap-1.5">
							<Label className="text-xs font-medium">
								Guardrail Profiles <span className="text-destructive">*</span>
							</Label>
							<Info className="text-muted-foreground h-3.5 w-3.5" />
						</div>

						{/* Chips container */}
						<div className="bg-background flex min-h-[42px] flex-wrap items-center gap-1.5 rounded-lg border p-2">
							{formState.provider_config_ids.length === 0 && (
								<span className="text-muted-foreground px-1 text-xs">No profiles selected. Choose from dropdown below.</span>
							)}
							{formState.provider_config_ids.map((pid) => {
								const prov = providers.find((p) => p.id === pid);
								const ProviderIcon =
									prov?.provider_name === "secrets"
										? Key
										: prov?.provider_name === "prompt-guardrail" || prov?.provider_name === "prompt_guardrail"
											? ShieldCheck
											: Boxes;
								const providerPrefix =
									prov?.provider_name === "secrets"
										? "Secrets"
										: prov?.provider_name === "prompt-guardrail" || prov?.provider_name === "prompt_guardrail"
											? "Prompt Guardrail"
											: "Custom Regex";
								return (
									<Badge
										key={pid}
										variant="secondary"
										className="gap-1.5 px-2.5 py-1 text-xs font-medium"
										data-testid={`guardrails-profile-chip-${pid}`}
									>
										<ProviderIcon className="text-primary h-3.5 w-3.5" />
										<span>
											{providerPrefix}: {prov ? prov.policy_name : `#${pid}`}
										</span>
										<button
											type="button"
											onClick={(e) => {
												e.stopPropagation();
												removeProvider(pid);
											}}
											className="hover:text-destructive text-muted-foreground"
										>
											<X className="h-3 w-3" />
										</button>
									</Badge>
								);
							})}

							{/* Dropdown to add more */}
							<DropdownMenu>
								<DropdownMenuTrigger asChild>
									<Button variant="ghost" size="sm" className="ml-auto h-7 px-2 text-xs">
										Add Profile <ChevronDown className="ml-1 h-3.5 w-3.5" />
									</Button>
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end" className="w-56">
									{providers.length === 0 ? (
										<div className="text-muted-foreground p-2 text-xs">No providers found. Add one in Providers first.</div>
									) : (
										providers.map((p) => {
											const selected = formState.provider_config_ids.includes(p.id);
											const dropdownPrefix =
												p.provider_name === "secrets"
													? "Secrets"
													: p.provider_name === "prompt-guardrail" || p.provider_name === "prompt_guardrail"
														? "Prompt Guardrail"
														: "Custom Regex";
											return (
												<DropdownMenuItem key={p.id} onClick={() => toggleProvider(p.id)} className="justify-between">
													<span className="truncate">
														{dropdownPrefix}: {p.policy_name || `#${p.id}`}
													</span>
													{selected && (
														<Badge variant="secondary" className="text-[10px]">
															Selected
														</Badge>
													)}
												</DropdownMenuItem>
											);
										})
									)}
								</DropdownMenuContent>
							</DropdownMenu>
						</div>
					</div>

					{/* CEL Expression */}
					<div className="space-y-1.5">
						<div className="flex items-center justify-between">
							<Label htmlFor="rule-cel" className="text-xs font-medium">
								CEL Expression
							</Label>
							<span className="text-muted-foreground text-[11px]">e.g. true</span>
						</div>
						<Textarea
							id="rule-cel"
							className="font-mono text-xs"
							rows={3}
							value={formState.cel_expression ?? ""}
							onChange={(e) => setFormState({ ...formState, cel_expression: e.target.value })}
							placeholder={`provider in ["openai", "anthropic"]`}
							data-testid="guardrails-rule-sheet-cel"
						/>
						<p className="text-muted-foreground text-[11px]">
							Variables: <code>model</code>, <code>provider</code>, <code>headers</code>, <code>query</code>, <code>virtual_key_id</code>,{" "}
							<code>virtual_key_name</code>.
						</p>
					</div>

					{/* Numerical Parameters: Sampling Rate & Timeout */}
					<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
						<div className="space-y-1.5">
							<Label htmlFor="rule-sampling" className="text-xs font-medium">
								Sampling Rate (%) <span className="text-destructive">*</span>
							</Label>
							<Input
								id="rule-sampling"
								type="number"
								min={0}
								max={100}
								value={formState.sampling_rate ?? 100}
								onChange={(e) => setFormState({ ...formState, sampling_rate: Number(e.target.value) })}
								data-testid="guardrails-rule-sheet-sampling"
							/>
							<p className="text-muted-foreground text-[11px]">Percentage of matching requests to process</p>
						</div>

						<div className="space-y-1.5">
							<Label htmlFor="rule-timeout" className="text-xs font-medium">
								Timeout (seconds) <span className="text-destructive">*</span>
							</Label>
							<Input
								id="rule-timeout"
								type="number"
								min={1}
								value={formState.timeout ?? 60}
								onChange={(e) => setFormState({ ...formState, timeout: Number(e.target.value) })}
								data-testid="guardrails-rule-sheet-timeout"
							/>
							<p className="text-muted-foreground text-[11px]">Max wait time for guardrail execution</p>
						</div>
					</div>

					{/* Max Turns to Send */}
					<div className="space-y-1.5">
						<Label htmlFor="rule-max-turns" className="text-xs font-medium">
							Max Turns to Send
						</Label>
						<Input
							id="rule-max-turns"
							type="number"
							min={0}
							value={formState.max_turns_to_send ?? 0}
							onChange={(e) => setFormState({ ...formState, max_turns_to_send: Number(e.target.value) })}
							data-testid="guardrails-rule-sheet-max-turns"
						/>
						<p className="text-muted-foreground text-[11px]">
							Number of historical conversation turns to send to the guardrail provider; the latest message is always included on top. Set
							to 0 to send all turns.
						</p>
					</div>
				</div>

				{/* Sticky Footer */}
				<div className="bg-background flex items-center justify-end gap-2.5 border-t p-4">
					<Button variant="outline" size="sm" onClick={() => onOpenChange(false)} data-testid="guardrails-rule-sheet-cancel">
						Cancel
					</Button>
					<Button size="sm" onClick={handleSubmit} disabled={isSaving} data-testid="guardrails-rule-sheet-save">
						{isEdit ? "Update Rule" : "Save Rule"}
					</Button>
				</div>
			</SheetContent>
		</Sheet>
	);
}