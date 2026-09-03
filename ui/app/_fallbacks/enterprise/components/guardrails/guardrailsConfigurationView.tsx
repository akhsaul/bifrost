import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
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
import { PlusIcon, Trash2Icon } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const APPLY_TO: RuleApplyTo[] = ["input", "output", "both"];

function parseConfig(raw: any): GuardrailsPluginConfig {
	if (!raw || typeof raw !== "object") return defaultGuardrailsConfig();
	return {
		guardrail_providers: Array.isArray(raw.guardrail_providers) ? raw.guardrail_providers : [],
		guardrail_rules: Array.isArray(raw.guardrail_rules) ? raw.guardrail_rules : [],
	};
}

function nextRuleId(cfg: GuardrailsPluginConfig): number {
	const ids = cfg.guardrail_rules.map((r) => r.id);
	return ids.length ? Math.max(...ids) + 1 : 201;
}

function newRule(id: number, providerIds: number[]): GuardrailRule {
	return {
		id,
		name: "",
		enabled: true,
		cel_expression: "true",
		apply_to: "input",
		sampling_rate: 100,
		provider_config_ids: [...providerIds],
	};
}

export default function GuardrailsConfigurationView() {
	const { data: plugin, isLoading } = useGetPluginQuery(GUARDRAILS_PLUGIN_NAME);
	const [updatePlugin, { isLoading: isSaving }] = useUpdatePluginMutation();
	const [rules, setRules] = useState<GuardrailRule[]>([]);
	const [pluginEnabled, setPluginEnabled] = useState(false);

	useEffect(() => {
		const cfg = parseConfig(plugin?.config);
		setRules(cfg.guardrail_rules);
		setPluginEnabled(Boolean(plugin?.enabled));
	}, [plugin]);

	const providers = useMemo(() => parseConfig(plugin?.config).guardrail_providers, [plugin]);

	const dirty = useMemo(() => JSON.stringify(rules) !== JSON.stringify(parseConfig(plugin?.config).guardrail_rules), [rules, plugin]);

	const buildPayload = () => {
		const cfg = parseConfig(plugin?.config);
		return {
			enabled: pluginEnabled,
			config: { ...cfg, guardrail_rules: rules } satisfies GuardrailsPluginConfig,
		};
	};

	const save = async () => {
		for (const r of rules) {
			if (!r.name?.trim()) {
				toast.error(`Rule #${r.id}: name is required`);
				return;
			}
			if (r.provider_config_ids.length === 0) {
				toast.error(`Rule "${r.name}": attach at least one guardrail provider`);
				return;
			}
		}
		try {
			await updatePlugin({ name: GUARDRAILS_PLUGIN_NAME, data: buildPayload() }).unwrap();
			toast.success("Guardrail rules saved");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const addRule = () => {
		setRules((prev) => [
			...prev,
			newRule(
				nextRuleId({ guardrail_providers: providers, guardrail_rules: prev }),
				providers.map((p) => p.id),
			),
		]);
	};

	const updateRule = (idx: number, patch: Partial<GuardrailRule>) => {
		setRules((prev) => prev.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
	};

	const toggleProvider = (rule: GuardrailRule, providerId: number) => {
		const has = rule.provider_config_ids.includes(providerId);
		const ids = has ? rule.provider_config_ids.filter((id) => id !== providerId) : [...rule.provider_config_ids, providerId];
		setRules((prev) => prev.map((r) => (r.id === rule.id ? { ...r, provider_config_ids: ids } : r)));
	};

	if (isLoading) {
		return <div className="text-muted-foreground p-8 text-sm">Loading guardrails…</div>;
	}

	if (providers.length === 0) {
		return (
			<div className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
				No guardrail providers configured yet. Create one under <strong>Guardrails → Providers</strong> first.
			</div>
		);
	}

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-lg font-semibold">Guardrail Rules</h2>
					<p className="text-muted-foreground text-sm">
						Rules gate when providers run via a CEL expression (variables: model, provider, headers, query, virtual_key_id,
						virtual_key_name) and scan request input / response output.
					</p>
				</div>
				<div className="flex items-center gap-3">
					<div className="flex items-center gap-2">
						<Switch checked={pluginEnabled} onCheckedChange={setPluginEnabled} data-testid="guardrails-rules-plugin-enabled-switch" />
						<Label>Plugin enabled</Label>
					</div>
					<Button variant="outline" onClick={addRule} data-testid="guardrails-rule-add">
						<PlusIcon className="h-4 w-4" /> Add Rule
					</Button>
					<Button onClick={save} disabled={!dirty || isSaving} data-testid="guardrails-rule-save">
						Save
					</Button>
				</div>
			</div>

			{rules.length === 0 && (
				<div className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
					No guardrail rules yet. Add one to start enforcing patterns.
				</div>
			)}

			{rules.map((rule, idx) => (
				<div key={rule.id} className="rounded-lg border p-4" data-testid={`guardrails-rule-${rule.id}`}>
					<div className="mb-3 flex flex-wrap items-center gap-3">
						<Badge variant="outline">#{rule.id}</Badge>
						<Input
							className="max-w-xs"
							placeholder="Rule name (e.g. block-email-input)"
							value={rule.name}
							onChange={(e) => updateRule(idx, { name: e.target.value })}
							data-testid={`guardrails-rule-name-${rule.id}`}
						/>
						<Select value={rule.apply_to} onValueChange={(v) => updateRule(idx, { apply_to: v as RuleApplyTo })}>
							<SelectTrigger className="w-32" data-testid={`guardrails-rule-applyto-${rule.id}`}>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{APPLY_TO.map((a) => (
									<SelectItem key={a} value={a}>
										{a}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Switch
							checked={rule.enabled}
							onCheckedChange={(v) => updateRule(idx, { enabled: v })}
							data-testid={`guardrails-rule-enabled-${rule.id}`}
						/>
						<Button
							variant="ghost"
							size="icon"
							onClick={() => setRules((prev) => prev.filter((_, i) => i !== idx))}
							data-testid={`guardrails-rule-delete-${rule.id}`}
						>
							<Trash2Icon className="h-4 w-4" />
						</Button>
					</div>

					<div className="grid gap-3 md:grid-cols-2">
						<div className="space-y-1">
							<Label>CEL expression</Label>
							<Textarea
								className="font-mono text-xs"
								rows={2}
								value={rule.cel_expression ?? ""}
								onChange={(e) => updateRule(idx, { cel_expression: e.target.value })}
								placeholder={`headers["x-bf-tenant"] == "external"`}
								data-testid={`guardrails-rule-cel-${rule.id}`}
							/>
						</div>
						<div className="space-y-1">
							<Label>Sampling rate ({rule.sampling_rate ?? 100}%)</Label>
							<Input
								type="number"
								min={0}
								max={100}
								value={rule.sampling_rate ?? 100}
								onChange={(e) => updateRule(idx, { sampling_rate: Number(e.target.value) })}
								data-testid={`guardrails-rule-sampling-${rule.id}`}
							/>
						</div>
					</div>

					<div className="mt-3 space-y-1">
						<Label>Guardrail providers</Label>
						<div className="flex flex-wrap gap-2">
							{providers.map((p) => (
								<label key={p.id} className="flex items-center gap-1.5 rounded-md border px-2 py-1 text-sm">
									<input
										type="checkbox"
										checked={rule.provider_config_ids.includes(p.id)}
										onChange={() => toggleProvider(rule, p.id)}
										data-testid={`guardrails-rule-provider-${rule.id}-${p.id}`}
									/>
									{p.policy_name || `#${p.id}`}
								</label>
							))}
						</div>
					</div>
				</div>
			))}
		</div>
	);
}