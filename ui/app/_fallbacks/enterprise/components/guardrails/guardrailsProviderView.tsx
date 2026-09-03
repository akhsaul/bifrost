import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage, useGetPluginQuery, useUpdatePluginMutation } from "@/lib/store";
import {
	GUARDRAILS_PLUGIN_NAME,
	defaultGuardrailsConfig,
	type GuardrailPattern,
	type GuardrailProvider,
	type GuardrailsPluginConfig,
	type PatternAction,
	type RedactionStrategy,
} from "@/lib/types/guardrails";
import { PlusIcon, Trash2Icon } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const ACTIONS: PatternAction[] = ["block", "redact", "detect_only"];
const STRATEGIES: RedactionStrategy[] = ["replace", "mask", "hash"];
const FLAGS = ["", "i", "m", "s", "im", "is", "ms", "ims"];

function parseConfig(raw: any): GuardrailsPluginConfig {
	if (!raw || typeof raw !== "object") return defaultGuardrailsConfig();
	return {
		guardrail_providers: Array.isArray(raw.guardrail_providers) ? raw.guardrail_providers : [],
		guardrail_rules: Array.isArray(raw.guardrail_rules) ? raw.guardrail_rules : [],
	};
}

function nextProviderId(cfg: GuardrailsPluginConfig): number {
	const ids = cfg.guardrail_providers.map((p) => p.id);
	return ids.length ? Math.max(...ids) + 1 : 20;
}

function newPattern(): GuardrailPattern {
	return { pattern: "", description: "", action: "block", redaction_strategy: "replace" };
}

export default function guardrailsProviderView() {
	const { data: plugin, isLoading } = useGetPluginQuery(GUARDRAILS_PLUGIN_NAME);
	const [updatePlugin, { isLoading: isSaving }] = useUpdatePluginMutation();
	const [providers, setProviders] = useState<GuardrailProvider[]>([]);
	const [pluginEnabled, setPluginEnabled] = useState(false);

	useEffect(() => {
		const cfg = parseConfig(plugin?.config);
		setProviders(cfg.guardrail_providers);
		setPluginEnabled(Boolean(plugin?.enabled));
	}, [plugin]);

	const dirty = useMemo(
		() => JSON.stringify(providers) !== JSON.stringify(parseConfig(plugin?.config).guardrail_providers),
		[providers, plugin],
	);

	const buildPayload = () => {
		const cfg = parseConfig(plugin?.config);
		return {
			enabled: pluginEnabled,
			config: { ...cfg, guardrail_providers: providers } satisfies GuardrailsPluginConfig,
		};
	};

	const save = async () => {
		for (const p of providers) {
			if (!p.policy_name?.trim()) {
				toast.error(`Provider #${p.id}: name is required`);
				return;
			}
			for (const pat of p.config.patterns) {
				if (!pat.pattern?.trim()) {
					toast.error(`Provider "${p.policy_name}": regex pattern is required`);
					return;
				}
			}
		}
		try {
			await updatePlugin({ name: GUARDRAILS_PLUGIN_NAME, data: buildPayload() }).unwrap();
			toast.success("Guardrails configuration saved");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const addProvider = () => {
		setProviders((prev) => [
			...prev,
			{
				id: nextProviderId({ guardrail_providers: prev, guardrail_rules: [] }),
				provider_name: "regex" as const,
				policy_name: "",
				enabled: true,
				config: { patterns: [newPattern()] },
			},
		]);
	};

	const updateProvider = (idx: number, patch: Partial<GuardrailProvider>) => {
		setProviders((prev) => prev.map((p, i) => (i === idx ? { ...p, ...patch } : p)));
	};

	const updatePattern = (pIdx: number, patIdx: number, patch: Partial<GuardrailPattern>) => {
		setProviders((prev) =>
			prev.map((p, i) =>
				i === pIdx ? { ...p, config: { patterns: p.config.patterns.map((pat, j) => (j === patIdx ? { ...pat, ...patch } : pat)) } } : p,
			),
		);
	};

	if (isLoading) {
		return <div className="text-muted-foreground p-8 text-sm">Loading guardrails…</div>;
	}

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-lg font-semibold">Guardrail Providers</h2>
					<p className="text-muted-foreground text-sm">
						OSS guardrails support the in-process regex provider (Go RE2). Streaming responses are not scanned.
					</p>
				</div>
				<div className="flex items-center gap-3">
					<div className="flex items-center gap-2">
						<Switch checked={pluginEnabled} onCheckedChange={setPluginEnabled} data-testid="guardrails-plugin-enabled-switch" />
						<Label>Plugin enabled</Label>
					</div>
					<Button variant="outline" onClick={addProvider} data-testid="guardrails-provider-add">
						<PlusIcon className="h-4 w-4" /> Add Configuration
					</Button>
					<Button onClick={save} disabled={!dirty || isSaving} data-testid="guardrails-provider-save">
						Save
					</Button>
				</div>
			</div>

			{providers.length === 0 && (
				<div className="text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
					No guardrail provider configurations yet. Add one to define regex patterns.
				</div>
			)}

			{providers.map((provider, pIdx) => (
				<div key={provider.id} className="rounded-lg border p-4" data-testid={`guardrails-provider-${provider.id}`}>
					<div className="mb-3 flex items-center gap-3">
						<Badge variant="outline">#{provider.id}</Badge>
						<Input
							className="max-w-xs"
							placeholder="Configuration name (e.g. pii-detection)"
							value={provider.policy_name}
							onChange={(e) => updateProvider(pIdx, { policy_name: e.target.value })}
							data-testid={`guardrails-provider-name-${provider.id}`}
						/>
						<Switch
							checked={provider.enabled}
							onCheckedChange={(v) => updateProvider(pIdx, { enabled: v })}
							data-testid={`guardrails-provider-enabled-${provider.id}`}
						/>
						<Button
							variant="ghost"
							size="icon"
							onClick={() => setProviders((prev) => prev.filter((_, i) => i !== pIdx))}
							data-testid={`guardrails-provider-delete-${provider.id}`}
						>
							<Trash2Icon className="h-4 w-4" />
						</Button>
					</div>

					<Table>
						<TableHeader>
							<TableRow>
								<TableHead className="w-[30%]">Pattern</TableHead>
								<TableHead>Description</TableHead>
								<TableHead>Entity type</TableHead>
								<TableHead>Flags</TableHead>
								<TableHead>Action</TableHead>
								<TableHead>Redaction</TableHead>
								<TableHead />
							</TableRow>
						</TableHeader>
						<TableBody>
							{provider.config.patterns.map((pat, patIdx) => (
								<TableRow key={patIdx}>
									<TableCell>
										<Input
											placeholder={`\\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\\.[A-Z]{2,}\\b`}
											value={pat.pattern}
											onChange={(e) => updatePattern(pIdx, patIdx, { pattern: e.target.value })}
											data-testid={`guardrails-pattern-value-${provider.id}-${patIdx}`}
										/>
									</TableCell>
									<TableCell>
										<Input value={pat.description ?? ""} onChange={(e) => updatePattern(pIdx, patIdx, { description: e.target.value })} />
									</TableCell>
									<TableCell>
										<Input value={pat.entity_type ?? ""} onChange={(e) => updatePattern(pIdx, patIdx, { entity_type: e.target.value })} />
									</TableCell>
									<TableCell>
										<Select value={pat.flags ?? ""} onValueChange={(v) => updatePattern(pIdx, patIdx, { flags: v || undefined })}>
											<SelectTrigger className="w-20">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												{FLAGS.map((f) => (
													<SelectItem key={f || "none"} value={f}>
														{f || "none"}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</TableCell>
									<TableCell>
										<Select
											value={pat.action ?? "block"}
											onValueChange={(v) => updatePattern(pIdx, patIdx, { action: v as PatternAction })}
										>
											<SelectTrigger className="w-32">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												{ACTIONS.map((a) => (
													<SelectItem key={a} value={a}>
														{a}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</TableCell>
									<TableCell>
										<Select
											value={pat.redaction_strategy ?? "replace"}
											onValueChange={(v) => updatePattern(pIdx, patIdx, { redaction_strategy: v as RedactionStrategy })}
										>
											<SelectTrigger className="w-28">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												{STRATEGIES.map((s) => (
													<SelectItem key={s} value={s}>
														{s}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</TableCell>
									<TableCell>
										<Button
											variant="ghost"
											size="icon"
											onClick={() =>
												setProviders((prev) =>
													prev.map((p, i) =>
														i === pIdx
															? {
																	...p,
																	config: {
																		patterns: p.config.patterns.filter((_, j) => j !== patIdx),
																	},
																}
															: p,
													),
												)
											}
											data-testid={`guardrails-pattern-delete-${provider.id}-${patIdx}`}
										>
											<Trash2Icon className="h-4 w-4" />
										</Button>
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>

					<Button
						variant="outline"
						size="sm"
						className="mt-2"
						onClick={() => updateProvider(pIdx, { config: { patterns: [...provider.config.patterns, newPattern()] } })}
						data-testid={`guardrails-pattern-add-${provider.id}`}
					>
						<PlusIcon className="h-4 w-4" /> Add Pattern
					</Button>
				</div>
			))}
		</div>
	);
}