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
	type GuardrailRule,
	type GuardrailsPluginConfig,
	type RuleApplyTo,
} from "@/lib/types/guardrails";
import { MoreHorizontal, Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

const APPLY_TO_OPTIONS: { value: RuleApplyTo; label: string }[] = [
	{ value: "input", label: "Input (Prompt)" },
	{ value: "output", label: "Output (Completion)" },
	{ value: "both", label: "Both (Input & Output)" },
];

function parseConfig(raw: unknown): GuardrailsPluginConfig {
	if (!raw || typeof raw !== "object") return defaultGuardrailsConfig();
	const r = raw as Record<string, unknown>;
	return {
		guardrail_providers: Array.isArray(r.guardrail_providers) ? r.guardrail_providers : [],
		guardrail_rules: Array.isArray(r.guardrail_rules) ? r.guardrail_rules : [],
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
				enabled: true,
				cel_expression: "true",
				apply_to: "input",
				sampling_rate: 100,
				provider_config_ids: providers.map((p) => p.id),
			},
		});
	};

	const handleEdit = (rule: GuardrailRule) => {
		setSheet({ open: true, rule: structuredClone(rule) });
	};

	if (isLoading) {
		return <div className="text-muted-foreground p-8 text-sm">Loading guardrails…</div>;
	}

	return (
		<div className="space-y-4">
			<div className="flex flex-wrap items-center justify-between gap-4">
				<div>
					<h1 className="text-xl font-bold tracking-tight">Guardrail Rules</h1>
					<p className="text-muted-foreground text-sm">
						Rules evaluate conditions via CEL expressions and enforce regex providers on input and output.
					</p>
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

			<div className="bg-card rounded-md border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead className="w-16">ID</TableHead>
							<TableHead>Name</TableHead>
							<TableHead className="w-32">Apply To</TableHead>
							<TableHead className="w-24">Sampling</TableHead>
							<TableHead>Providers</TableHead>
							<TableHead className="w-28">Is Enabled</TableHead>
							<TableHead className="w-24 text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{rules.length === 0 ? (
							<TableRow>
								<TableCell colSpan={7} className="text-muted-foreground h-32 text-center text-sm">
									No rules configured yet. Click <strong>Add Rule</strong> to create one.
								</TableCell>
							</TableRow>
						) : (
							rules.map((rule) => (
								<TableRow
									key={rule.id}
									className="hover:bg-muted/50 cursor-pointer"
									onClick={() => handleEdit(rule)}
									data-testid={`guardrails-rule-row-${rule.id}`}
								>
									<TableCell className="font-mono text-xs">{rule.id}</TableCell>
									<TableCell className="font-medium">{rule.name || "Untitled"}</TableCell>
									<TableCell>
										<Badge variant="outline" className="text-xs capitalize">
											{rule.apply_to}
										</Badge>
									</TableCell>
									<TableCell className="text-muted-foreground text-xs">{rule.sampling_rate ?? 100}%</TableCell>
									<TableCell>
										<div className="flex flex-wrap gap-1">
											{rule.provider_config_ids.map((pid) => {
												const p = providers.find((prov) => prov.id === pid);
												return (
													<Badge key={pid} variant="secondary" className="text-[11px] font-normal">
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
// Slide-over Sheet Component for Rule Configuration
// ---------------------------------------------------------------------------

interface RuleSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	rule: GuardrailRule | null;
	providers: GuardrailsPluginConfig["guardrail_providers"];
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

	const toggleProvider = (id: number) => {
		const has = formState.provider_config_ids.includes(id);
		const ids = has ? formState.provider_config_ids.filter((pid) => pid !== id) : [...formState.provider_config_ids, id];
		setFormState({ ...formState, provider_config_ids: ids });
	};

	const handleSubmit = () => {
		if (!formState.name.trim()) {
			toast.error("Rule name is required");
			return;
		}
		if (formState.provider_config_ids.length === 0) {
			toast.error("Please attach at least one guardrail provider");
			return;
		}
		onSave(formState);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col overflow-hidden p-0 sm:max-w-xl md:max-w-2xl">
				<SheetHeader className="border-b p-6">
					<SheetTitle>Guardrail Rule Configuration</SheetTitle>
					<SheetDescription>Configure rule gating, execution scope, and attached regex providers.</SheetDescription>
				</SheetHeader>

				<div className="flex-1 space-y-6 overflow-y-auto p-6">
					{/* Name */}
					<div className="space-y-1.5">
						<Label htmlFor="rule-name">
							Name <span className="text-destructive">*</span>
						</Label>
						<Input
							id="rule-name"
							value={formState.name}
							onChange={(e) => setFormState({ ...formState, name: e.target.value })}
							placeholder="e.g. block-pii-in-prompts"
							data-testid="guardrails-rule-sheet-name"
						/>
					</div>

					{/* Apply To */}
					<div className="space-y-1.5">
						<Label>Apply To</Label>
						<Select value={formState.apply_to} onValueChange={(v) => setFormState({ ...formState, apply_to: v as RuleApplyTo })}>
							<SelectTrigger className="w-full">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{APPLY_TO_OPTIONS.map((opt) => (
									<SelectItem key={opt.value} value={opt.value}>
										{opt.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>

					{/* CEL Expression */}
					<div className="space-y-1.5">
						<div className="flex items-center justify-between">
							<Label htmlFor="rule-cel">CEL Expression</Label>
							<span className="text-muted-foreground text-[11px]">e.g. true</span>
						</div>
						<Textarea
							id="rule-cel"
							className="font-mono text-xs"
							rows={3}
							value={formState.cel_expression ?? ""}
							onChange={(e) => setFormState({ ...formState, cel_expression: e.target.value })}
							placeholder={`headers["x-bf-tenant"] == "external"`}
							data-testid="guardrails-rule-sheet-cel"
						/>
						<p className="text-muted-foreground text-[11px]">
							Variables: <code>model</code>, <code>provider</code>, <code>headers</code>, <code>query</code>, <code>virtual_key_id</code>,{" "}
							<code>virtual_key_name</code>.
						</p>
					</div>

					{/* Sampling Rate */}
					<div className="space-y-1.5">
						<Label htmlFor="rule-sampling">Sampling Rate (%)</Label>
						<Input
							id="rule-sampling"
							type="number"
							min={0}
							max={100}
							value={formState.sampling_rate ?? 100}
							onChange={(e) => setFormState({ ...formState, sampling_rate: Number(e.target.value) })}
							data-testid="guardrails-rule-sheet-sampling"
						/>
					</div>

					{/* Guardrail Providers Checkboxes */}
					<div className="space-y-2">
						<Label>
							Attached Providers <span className="text-destructive">*</span>
						</Label>
						{providers.length === 0 ? (
							<div className="text-muted-foreground rounded-md border border-dashed p-3 text-xs">
								No providers configured. Create one in Guardrail Providers first.
							</div>
						) : (
							<div className="space-y-2 rounded-md border p-3">
								{providers.map((p) => (
									<label key={p.id} className="flex cursor-pointer items-center gap-2.5 text-sm select-none">
										<input
											type="checkbox"
											checked={formState.provider_config_ids.includes(p.id)}
											onChange={() => toggleProvider(p.id)}
											className="rounded"
											data-testid={`guardrails-rule-sheet-provider-${p.id}`}
										/>
										<span className="font-medium">{p.policy_name || "Untitled"}</span>
										<Badge variant="outline" className="font-mono text-[10px]">
											#{p.id}
										</Badge>
										<span className="text-muted-foreground text-xs">({p.config.patterns.length} pattern(s))</span>
									</label>
								))}
							</div>
						)}
					</div>
				</div>

				{/* Sticky footer */}
				<div className="bg-background flex items-center justify-between border-t p-4">
					<div className="flex items-center gap-2">
						<Switch
							id="rule-sheet-enabled"
							checked={formState.enabled}
							onCheckedChange={(v) => setFormState({ ...formState, enabled: v })}
							data-testid="guardrails-rule-sheet-enabled"
						/>
						<Label htmlFor="rule-sheet-enabled" className="text-xs">
							Enabled
						</Label>
					</div>

					<div className="flex items-center gap-2">
						<Button
							variant="ghost"
							size="sm"
							onClick={() => rule && setFormState(structuredClone(rule))}
							data-testid="guardrails-rule-sheet-reset"
						>
							Reset
						</Button>
						<Button size="sm" onClick={handleSubmit} disabled={isSaving} data-testid="guardrails-rule-sheet-save">
							Save Rule
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}