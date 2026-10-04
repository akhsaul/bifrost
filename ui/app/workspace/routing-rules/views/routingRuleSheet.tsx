/**
 * Routing Rule Dialog (Sheet)
 * Create/Edit form for routing rules
 */

import { CustomerSelector } from "@/components/entitySelectors/customerSelector";
import { TeamSelector } from "@/components/entitySelectors/teamSelector";
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getUserPicker } from "@/lib/registries/userPicker";
import { getErrorMessage } from "@/lib/store";
import { useGetAllKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { useCreateRoutingRuleMutation, useGetRoutingRulesQuery, useUpdateRoutingRuleMutation } from "@/lib/store/apis/routingRulesApi";
import {
	CreateRoutingRuleRequest,
	DEFAULT_ROUTING_FALLBACK,
	DEFAULT_ROUTING_RULE_FORM_DATA,
	DEFAULT_ROUTING_TARGET,
	ROUTING_RULE_SCOPES,
	RoutingFallbackFormData,
	RoutingRule,
	RoutingRuleFormData,
	RoutingStrategy,
	RoutingTarget,
	RoutingTargetFormData,
	TargetModelItem,
} from "@/lib/types/routingRules";
import { denormalizeFallback, normalizeFallback } from "@/lib/utils/routingRules";
import { validateRateLimitAndBudgetRules, validateRoutingRules } from "@/lib/utils/celConverterRouting";
import { isValidRuleGroupType, normalizeRoutingRuleGroupQuery } from "@/lib/utils/routingRuleGroupQuery";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChevronDown, ChevronUp, Plus, Trash2, X } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { RuleGroupType } from "react-querybuilder";
import { toast } from "sonner";
// Side-effect import: registers the enterprise user picker (no-op in OSS builds).
import "@enterprise/lib/registrations/userPicker";

interface RoutingRuleDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	editingRule?: RoutingRule | null;
	onSuccess?: () => void;
}

const defaultQuery: RuleGroupType = {
	combinator: "and",
	rules: [],
};

type ConditionMode = "builder" | "cel";

/**
 * Decides which conditions editor a rule opens in. Rules authored outside the visual
 * builder (e.g. via the API) have a CEL expression but no usable `query`; those open in
 * CEL mode so the expression stays visible and editable instead of being silently cleared.
 */
function initialConditionMode(rule?: RoutingRule | null): ConditionMode {
	if (!rule) {
		return "builder";
	}
	const hasQuery = isValidRuleGroupType(rule.query) && (rule.query.rules?.length ?? 0) > 0;
	if (hasQuery) {
		return "builder";
	}
	return rule.cel_expression?.trim() ? "cel" : "builder";
}

// Lazy-load CEL builder (heavy dependency tree).
const CELRuleBuilderLazy = lazy(() =>
	import("@/app/workspace/routing-rules/components/celBuilder/celRuleBuilder").then((mod) => ({
		default: mod.CELRuleBuilder,
	})),
);
const CELRuleBuilder = (props: React.ComponentProps<typeof CELRuleBuilderLazy>) => (
	<Suspense fallback={<div className="text-sm text-gray-500">Loading CEL builder...</div>}>
		<CELRuleBuilderLazy {...props} />
	</Suspense>
);

export function RoutingRuleSheet({ open, onOpenChange, editingRule, onSuccess }: RoutingRuleDialogProps) {
	const { data: rulesData } = useGetRoutingRulesQuery();
	const rules = rulesData?.rules || [];
	const { data: providersData = [] } = useGetProvidersQuery();
	const { data: allKeysData = [] } = useGetAllKeysQuery();
	const [createRoutingRule, { isLoading: isCreating }] = useCreateRoutingRuleMutation();
	const [updateRoutingRule, { isLoading: isUpdating }] = useUpdateRoutingRuleMutation();

	// State for targets and query (managed outside react-hook-form for complex nested structures)
	const [targets, setTargets] = useState<RoutingTargetFormData[]>([{ ...DEFAULT_ROUTING_TARGET }]);
	const [query, setQuery] = useState<RuleGroupType>(defaultQuery);
	const [conditionMode, setConditionMode] = useState<ConditionMode>("builder");
	const [builderKey, setBuilderKey] = useState(0);
	// Server-side CEL compile error, surfaced inline under the CEL editor instead of a toast.
	const [celError, setCelError] = useState<string | null>(null);

	const {
		register,
		handleSubmit,
		setValue,
		watch,
		reset,
		formState: { errors },
	} = useForm<RoutingRuleFormData>({
		defaultValues: DEFAULT_ROUTING_RULE_FORM_DATA,
	});

	const isEditing = !!editingRule;
	const isLoading = isCreating || isUpdating;
	const canCreate = useRbac(RbacResource.RoutingRules, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.RoutingRules, RbacOperation.Update);
	const hasRequiredAccess = isEditing ? canUpdate : canCreate;
	const enabled = watch("enabled");
	const chainRule = watch("chain_rule");
	const scope = watch("scope");
	const scopeId = watch("scope_id");

	// Registered by the downstream build at module load; undefined in builds
	// without a user directory, which hides the "User" scope option.
	const UserPicker = getUserPicker();
	const fallbacks = watch("fallbacks");

	// Get available providers from configured providers, plus any provider already
	// referenced by the current targets, existing rules' targets, or rules' fallbacks
	// so edited/removed providers are still visible in the dropdown.
	const availableProviders = Array.from(
		new Set([
			...providersData.map((p) => p.name),
			...(targets.map((t) => t.provider).filter(Boolean) as string[]),
			...(rules.flatMap((r) => r.targets?.map((t) => t.provider).filter(Boolean) ?? []) as string[]),
			...rules.flatMap((r) => (r.fallbacks ?? []).map((f) => normalizeFallback(f).provider?.trim()).filter(Boolean) as string[]),
		]),
	);
	const providerOptions = availableProviders.map((prov) => ({
		label: getProviderLabel(prov),
		value: prov,
		icon: <RenderProviderIcon provider={prov as ProviderIconType} size="sm" className="h-4 w-4" />,
	}));

	// Initialize form data when editing rule changes
	useEffect(() => {
		if (editingRule) {
			setValue("id", editingRule.id);
			setValue("name", editingRule.name);
			setValue("description", editingRule.description);
			setValue("cel_expression", editingRule.cel_expression);
			setValue("strategy", editingRule.strategy ?? "weighted");
			setValue("fallbacks", (editingRule.fallbacks || []).map(normalizeFallback));
			setValue("scope", editingRule.scope);
			setValue("scope_id", editingRule.scope_id || "");
			setValue("priority", editingRule.priority);
			setValue("enabled", editingRule.enabled);
			setValue("chain_rule", editingRule.chain_rule ?? false);
			if (editingRule.targets && editingRule.targets.length > 0) {
				if (editingRule.strategy === "group_adaptive") {
					const cardMap = new Map<string, RoutingTargetFormData>();
					for (const t of editingRule.targets) {
						const key = `${t.provider || ""}|${t.key_id || ""}`;
						const prio = t.priority ?? 1;
						const existing = cardMap.get(key);
						if (existing) {
							if (t.model) {
								const updated = [...(existing.models || []), { model: t.model, priority: prio }];
								updated.sort((a, b) => a.priority - b.priority);
								existing.models = updated;
							}
						} else {
							cardMap.set(key, {
								...DEFAULT_ROUTING_TARGET,
								provider: t.provider || "",
								model: t.model || "",
								models: t.model ? [{ model: t.model, priority: prio }] : [],
								key_id: t.key_id || "",
								weight: t.weight,
								priority: prio,
							});
						}
					}
					setTargets(Array.from(cardMap.values()));
				} else {
					setTargets(
						editingRule.targets.map((t, idx) => ({
							...DEFAULT_ROUTING_TARGET,
							provider: t.provider || "",
							model: t.model || "",
							models: t.model ? [{ model: t.model, priority: t.priority ?? idx + 1 }] : [],
							key_id: t.key_id || "",
							weight: t.weight,
							priority: t.priority ?? idx + 1,
						})),
					);
				}
			} else {
				setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
			}
			// Only react-querybuilder-shaped queries are valid; config may store other JSON under `query`.
			setQuery(normalizeRoutingRuleGroupQuery(editingRule.query));
			setConditionMode(initialConditionMode(editingRule));
			setBuilderKey((prev) => prev + 1);
			setCelError(null);
		} else {
			reset();
			setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
			setQuery(defaultQuery);
			setConditionMode("builder");
			setBuilderKey((prev) => prev + 1);
			setCelError(null);
		}
	}, [editingRule, open, setValue, reset]);

	const handleQueryChange = useCallback(
		(expression: string, newQuery: RuleGroupType) => {
			setValue("cel_expression", expression);
			setQuery(newQuery);
			// Editing the expression clears a stale server-side CEL error.
			setCelError(null);
		},
		[setValue],
	);

	const handleModeChange = useCallback((mode: ConditionMode) => {
		setConditionMode(mode);
		setCelError(null);
	}, []);

	const addTarget = () => {
		const currentStrategy = watch("strategy") || "weighted";
		if (currentStrategy === "priority") {
			// Priority strategy: default a new target to one after the current max priority rank.
			const maxPriority = targets.reduce((max, t) => Math.max(max, t.priority || 0), 0);
			setTargets((prev) => [...prev, { ...DEFAULT_ROUTING_TARGET, weight: 1, priority: maxPriority + 1 }]);
			return;
		}
		const remaining = 1 - targets.reduce((sum, t) => sum + (t.weight || 0), 0);
		setTargets((prev) => [
			...prev,
			{
				...DEFAULT_ROUTING_TARGET,
				weight: Math.max(0, parseFloat(remaining.toFixed(4))),
			},
		]);
	};

	const removeTarget = (index: number) => {
		setTargets((prev) => prev.filter((_, i) => i !== index));
	};

	const updateTarget = (index: number, field: keyof RoutingTargetFormData, value: string | number | TargetModelItem[] | undefined) => {
		setTargets((prev) => prev.map((t, i) => (i === index ? { ...t, [field]: value } : t)));
	};

	const updateTargetModels = (index: number, models: string[]) => {
		setTargets((prev) =>
			prev.map((t, i) => {
				if (i !== index) return t;
				const existing = t.models || [];
				const next: TargetModelItem[] = models.map((m, idx) => {
					const found = existing.find((e) => e.model === m);
					return found ? found : { model: m, priority: idx + 1 };
				});
				next.sort((a, b) => a.priority - b.priority);
				return { ...t, models: next, model: next[0]?.model || "" };
			}),
		);
	};

	const moveModelPriority = (index: number, modelIndex: number, direction: "up" | "down") => {
		setTargets((prev) =>
			prev.map((t, i) => {
				if (i !== index || !t.models) return t;
				const sorted = [...t.models].sort((a, b) => a.priority - b.priority);
				const swapIdx = direction === "up" ? modelIndex - 1 : modelIndex + 1;
				if (swapIdx < 0 || swapIdx >= sorted.length) return t;
				[sorted[modelIndex], sorted[swapIdx]] = [sorted[swapIdx], sorted[modelIndex]];
				const reindexed = sorted.map((m, idx) => ({ ...m, priority: idx + 1 }));
				return { ...t, models: reindexed, model: reindexed[0]?.model || t.model };
			}),
		);
	};

	const setModelPriority = (index: number, model: string, priority: number) => {
		setTargets((prev) =>
			prev.map((t, i) => {
				if (i !== index || !t.models) return t;
				const updated = t.models.map((m) => (m.model === model ? { ...m, priority } : m));
				updated.sort((a, b) => a.priority - b.priority);
				return { ...t, models: updated };
			}),
		);
	};

	const updateFallback = (index: number, changes: Partial<RoutingFallbackFormData>) => {
		setValue(
			"fallbacks",
			(fallbacks || []).map((fb, i) => (i === index ? { ...fb, ...changes } : fb)),
		);
	};

	const removeFallback = (index: number) => {
		setValue(
			"fallbacks",
			(fallbacks || []).filter((_, i) => i !== index),
		);
	};

	const totalWeight = targets.reduce((sum, t) => sum + (t.weight || 0), 0);

	const onSubmit = (data: RoutingRuleFormData) => {
		setCelError(null);

		// Validate scope_id is required when scope is not global
		if (data.scope !== "global" && !data.scope_id?.trim()) {
			toast.error(
				`${data.scope === "team" ? "Team" : data.scope === "customer" ? "Customer" : data.scope === "user" ? "User" : "Virtual Key"} is required`,
			);
			return;
		}

		// Validate targets — semantics depend on strategy: priority uses the dedicated
		// integer priority rank (lower number = higher precedence, must be ≥ 1),
		// weighted/adaptive use probability weights that must sum to 1.
		if (targets.length === 0) {
			toast.error("At least one routing target is required");
			return;
		}
		const currentStrategy = data.strategy || "weighted";
		if (currentStrategy === "priority") {
			for (const t of targets) {
				if (t.priority == null || !Number.isInteger(t.priority) || t.priority < 1) {
					toast.error("Each target priority must be an integer ≥ 1 (lower number = higher priority)");
					return;
				}
			}
		} else {
			for (const t of targets) {
				if (t.weight <= 0) {
					toast.error("Each target weight must be greater than 0");
					return;
				}
			}
			// group_adaptive: weight is per provider card, so sum distinct card weights
			const effectiveTotal = currentStrategy === "group_adaptive" ? targets.reduce((sum, t) => sum + (t.weight || 0), 0) : totalWeight;
			if (Math.abs(effectiveTotal - 1) > 0.001) {
				toast.error(`Target weights must sum to 1, current total: ${effectiveTotal.toFixed(4)}`);
				return;
			}
		}

		// Builder-only validation: these inspect the visual query, which does not exist in
		// raw-CEL mode. In CEL mode the expression is validated server-side on save instead.
		if (conditionMode === "builder") {
			// Validate regex patterns in routing rules
			const regexErrors = validateRoutingRules(query);
			if (regexErrors.length > 0) {
				toast.error(`Invalid regex pattern:\n${regexErrors.join("\n")}`);
				return;
			}

			// Validate rate limit and budget rules
			const rateLimitErrors = validateRateLimitAndBudgetRules(query);
			if (rateLimitErrors.length > 0) {
				toast.error(`Invalid rule configuration:\n${rateLimitErrors.join("\n")}`);
				return;
			}
		}

		// Filter out incomplete fallbacks (empty provider)
		const validFallbacks = (data.fallbacks || []).filter((fb) => (fb.provider ?? "").trim().length > 0).map(denormalizeFallback);

		const finalTargets: RoutingTarget[] = targets.flatMap(({ provider, model, models, key_id, weight, priority }): RoutingTarget[] => {
			if (currentStrategy === "group_adaptive" && models && models.length > 0) {
				return [
					{
						provider: provider || undefined,
						key_id: key_id || undefined,
						weight: weight,
						models: models.map((m) => ({ model: m.model, priority: m.priority })),
					},
				];
			}
			return [
				{
					provider: provider || undefined,
					model: model || undefined,
					key_id: key_id || undefined,
					weight: currentStrategy === "priority" ? 1 : weight,
					priority: currentStrategy === "priority" ? priority : undefined,
				},
			];
		});

		const payload: CreateRoutingRuleRequest = {
			name: data.name,
			description: data.description,
			cel_expression: data.cel_expression,
			strategy: data.strategy || "weighted",
			targets: finalTargets,
			fallbacks: validFallbacks,
			scope: data.scope,
			scope_id: data.scope === "global" ? undefined : data.scope_id || undefined,
			priority: data.priority,
			enabled: data.enabled,
			chain_rule: data.chain_rule,
			query: query,
		};

		const submitPromise =
			isEditing && editingRule
				? updateRoutingRule({
						id: editingRule.id,
						data: payload,
					}).unwrap()
				: createRoutingRule(payload).unwrap();

		submitPromise
			.then(() => {
				toast.success(isEditing ? "Routing rule updated successfully" : "Routing rule created successfully");
				reset();
				setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
				setQuery(defaultQuery);
				setConditionMode("builder");
				setBuilderKey((prev) => prev + 1);
				setCelError(null);
				onOpenChange(false);
				onSuccess?.();
			})
			.catch((error: any) => {
				const message = getErrorMessage(error);
				// A malformed CEL expression is a field-level problem — show it beneath the CEL
				// editor rather than in a toast (which turns a syntax error into a jarring popup).
				if (conditionMode === "cel" && /cel expression/i.test(message)) {
					setCelError(message);
					return;
				}
				toast.error(message);
			});
	};

	const handleCancel = () => {
		reset();
		setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
		setQuery(defaultQuery);
		setConditionMode("builder");
		setBuilderKey((prev) => prev + 1);
		setCelError(null);
		onOpenChange(false);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full min-w-1/2 flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{isEditing ? "Edit Routing Rule" : "Create New Routing Rule"}</SheetTitle>
					<SheetDescription>
						{isEditing ? "Update the routing rule configuration" : "Create a new CEL-based routing rule for intelligent request routing"}
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="flex grow flex-col">
					<div className="flex grow flex-col gap-6 px-4 pb-6 md:px-8">
						{/* Rule Name */}
						<div className="space-y-3">
							<Label htmlFor="name">
								Rule Name <span className="text-red-500">*</span>
							</Label>
							<Input
								id="name"
								placeholder="e.g., Route GPT-4 to Azure"
								{...register("name", {
									required: "Rule name is required",
									maxLength: 255,
								})}
							/>
							{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
						</div>

						{/* Description */}
						<div className="space-y-3">
							<Label htmlFor="description">Description</Label>
							<Textarea id="description" placeholder="Describe what this rule does..." rows={2} {...register("description")} />
						</div>

						{/* Enabled Switch */}
						<div className="flex items-center justify-between rounded-lg border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="enabled">Enable Rule</Label>
								<p className="text-muted-foreground text-sm">Rule will be active and applied to matching requests</p>
							</div>
							<Switch id="enabled" checked={enabled} onCheckedChange={(checked) => setValue("enabled", checked)} />
						</div>

						{/* Chain Rule Switch */}
						<div className="flex items-center justify-between rounded-lg border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="chain_rule">Chain Rule</Label>
								<p className="text-muted-foreground text-sm">
									After this rule matches, re-evaluate routing rules using the resolved provider/model as the new context. Useful for
									composing rules, e.g. normalize a model alias first, then route based on the canonical name.
								</p>
							</div>
							<Switch
								id="chain_rule"
								checked={chainRule}
								onCheckedChange={(checked) => setValue("chain_rule", checked)}
								data-testid="routing-rule-chain-rule-switch"
							/>
						</div>

						{/* Scope and Priority - Side by Side */}
						<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
							<div className="space-y-3">
								<Label htmlFor="scope">Scope</Label>
								<Select
									value={scope}
									onValueChange={(value) => {
										setValue("scope", value as any);
										// Clear scope_id when scope changes
										setValue("scope_id", "");
									}}
								>
									<SelectTrigger className="w-full">
										<SelectValue placeholder="Select scope..." />
									</SelectTrigger>
									<SelectContent>
										{ROUTING_RULE_SCOPES.map((scopeOption) => (
											<SelectItem key={scopeOption.value} value={scopeOption.value}>
												{scopeOption.label}
											</SelectItem>
										))}
										{(UserPicker || scope === "user") && <SelectItem value="user">User</SelectItem>}
									</SelectContent>
								</Select>
							</div>

							<div className="space-y-3">
								<Label htmlFor="priority">
									Priority <span className="text-red-500">*</span>
								</Label>
								<Input
									id="priority"
									type="number"
									min={0}
									max={1000}
									{...register("priority", {
										required: "Priority is required",
										min: { value: 0, message: "Priority must be ≥ 0" },
										max: { value: 1000, message: "Priority must be ≤ 1000" },
										valueAsNumber: true,
									})}
								/>
								<p className="text-muted-foreground text-xs">Lower numbers = higher priority (0 is highest)</p>
								{errors.priority && <p className="text-destructive text-sm">{errors.priority.message}</p>}
							</div>
						</div>

						{scope !== "global" && (
							<div className="space-y-2">
								<Label htmlFor="scope_id">
									{scope === "team" ? "Team" : scope === "customer" ? "Customer" : scope === "user" ? "User" : "Virtual Key"}{" "}
									<span className="text-red-500">*</span>
								</Label>
								{/* A rule stores only its scope_id, so there is no name to seed
								    these with — each selector resolves its own selection. */}
								{scope === "team" && <TeamSelector value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />}
								{scope === "customer" && <CustomerSelector value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />}
								{scope === "virtual_key" && <VirtualKeySelector value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />}
								{scope === "user" &&
									(UserPicker ? (
										<UserPicker value={scopeId || ""} onChange={(value) => setValue("scope_id", value)} />
									) : (
										// No user directory in this build: keep a plain input so
										// existing user-scoped rules remain editable.
										<Input
											id="scope_id"
											data-testid="routing-rule-scope-user-input"
											placeholder="Governance user ID"
											value={scopeId || ""}
											onChange={(e) => setValue("scope_id", e.target.value)}
										/>
									))}
								{/* Teams, customers and virtual keys are all searched lazily inside their
								    selectors, each of which surfaces its own empty state. */}
								{errors.scope_id && <p className="text-destructive text-sm">{errors.scope_id.message}</p>}
							</div>
						)}

						<Separator />

						{/* CEL Rule Builder */}
						<div className="space-y-3">
							<Label>Rule Builder</Label>
							<p className="text-muted-foreground text-sm">
								Build conditions to determine when this rule should apply. Leave empty to apply this rule to all requests.
							</p>
							<CELRuleBuilder
								key={builderKey}
								initialQuery={query}
								onChange={handleQueryChange}
								providers={availableProviders}
								models={[]}
								allowCustomModels={true}
								allowCelMode={true}
								initialMode={conditionMode}
								initialCel={editingRule?.cel_expression ?? ""}
								onModeChange={handleModeChange}
								celError={celError}
							/>
						</div>

						{/* Note about Token/Request Limits and Budget Configuration */}
						<p className="text-muted-foreground text-xs">
							Note: Ensure token limits, request limits, and budget are configured in{" "}
							<strong>Model Providers → Configurations → {"{provider}"} → Governance</strong> (provider-level) or{" "}
							<strong>Model Providers → Budgets & Limits</strong> section (model-level) before using them in routing rules.
						</p>

						<Separator />

						{/* Routing Targets */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<div>
									<Label>Routing Targets</Label>
									<p className="text-muted-foreground mt-0.5 text-xs">
										Choose routing strategy and candidate targets. Leave provider or model empty to use the incoming request value.
									</p>
								</div>
								<div className="flex items-center gap-2">
									<Select value={watch("strategy") || "weighted"} onValueChange={(val: RoutingStrategy) => setValue("strategy", val)}>
										<SelectTrigger className="h-8 w-[140px] text-xs">
											<SelectValue placeholder="Strategy" />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="weighted">Static Weighted</SelectItem>
											<SelectItem value="adaptive">Adaptive (EWMA)</SelectItem>
											<SelectItem value="priority">Priority Order</SelectItem>
											<SelectItem value="group_adaptive">Group Adaptive (Provider EWMA)</SelectItem>
										</SelectContent>
									</Select>
									<Button
										type="button"
										variant="outline"
										size="sm"
										onClick={addTarget}
										className="shrink-0 gap-2"
										data-testid="routing-rule-target-add"
									>
										<Plus className="h-4 w-4" />
										Add Target
									</Button>
								</div>
							</div>

							<div className="space-y-3">
								{targets.map((target, index) => (
									<TargetRow
										key={index}
										target={target}
										index={index}
										strategy={watch("strategy") || "weighted"}
										providerOptions={providerOptions}
										allKeys={allKeysData}
										showRemove={targets.length > 1}
										onUpdate={updateTarget}
										onRemove={removeTarget}
										onUpdateModels={updateTargetModels}
										onMoveModelPriority={moveModelPriority}
										onSetModelPriority={setModelPriority}
									/>
								))}
							</div>

							{/* Weight sum indicator / priority hint — semantics depend on strategy */}
							{watch("strategy") === "priority" ? (
								<div className="text-muted-foreground flex items-center justify-end gap-2 text-xs font-medium">
									Priority: lower number = higher precedence (1 is highest)
								</div>
							) : watch("strategy") === "group_adaptive" ? (
								<div className="space-y-1">
									<p className="text-muted-foreground text-xs">
										Group Adaptive selects the optimal provider via provider-level EWMA, then rotates models within the chosen provider by
										priority (1 → 2 → 1).
									</p>
									<div
										className={`flex items-center justify-end gap-2 text-xs font-medium ${Math.abs(totalWeight - 1) > 0.001 ? "text-destructive" : "text-muted-foreground"}`}
									>
										Total provider weight: {totalWeight.toFixed(4)}
										{Math.abs(totalWeight - 1) > 0.001 && <span className="text-destructive">(must equal 1)</span>}
									</div>
								</div>
							) : (
								<div
									className={`flex items-center justify-end gap-2 text-xs font-medium ${Math.abs(totalWeight - 1) > 0.001 ? "text-destructive" : "text-muted-foreground"}`}
								>
									Total weight: {totalWeight.toFixed(4)}
									{Math.abs(totalWeight - 1) > 0.001 && <span className="text-destructive">(must equal 1)</span>}
								</div>
							)}
						</div>

						{/* Fallbacks */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<div>
									<Label>Fallbacks</Label>{" "}
									<p className="text-muted-foreground mt-0.5 text-xs">
										Provider is required, but model and API key are optional. Leave model empty to use the incoming request value.
									</p>
								</div>
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={() => setValue("fallbacks", [...(fallbacks || []), { ...DEFAULT_ROUTING_FALLBACK }])}
									className="gap-2"
								>
									<Plus className="h-4 w-4" />
									Add Fallback
								</Button>
							</div>
							<div className="space-y-2">
								{(fallbacks || []).length === 0 ? (
									<p className="text-muted-foreground text-sm">No fallbacks configured</p>
								) : (
									(fallbacks || []).map((fallback, index) => (
										<FallbackRow
											key={index}
											fallback={fallback}
											index={index}
											providerOptions={providerOptions}
											allKeys={allKeysData}
											onUpdate={updateFallback}
											onRemove={removeFallback}
										/>
									))
								)}
							</div>
							<p className="text-muted-foreground text-xs">Fallbacks will be used in the order they are defined</p>
						</div>
					</div>
					{/* Action Buttons */}
					<div className="bg-card sticky bottom-0 flex justify-end gap-3 border-t px-4 py-4 md:px-8">
						<Button type="button" variant="outline" onClick={handleCancel} disabled={isLoading}>
							Cancel
						</Button>
						<Button type="submit" disabled={isLoading || !hasRequiredAccess}>
							{isEditing ? "Update Rule" : "Save Rule"}
						</Button>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}

interface ProviderKeySelectProps {
	idPrefix: string;
	clearLabel: string;
	provider?: string;
	keyId?: string;
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onChange: (keyId: string) => void;
}

/** Renders nothing until a provider is chosen, since keys are scoped to one. */
function ProviderKeySelect({ idPrefix, clearLabel, provider, keyId, allKeys, onChange }: ProviderKeySelectProps) {
	const availableKeys = provider ? allKeys.filter((k) => k.provider === provider).map((k) => ({ id: k.key_id, name: k.name })) : [];
	if (!provider || (availableKeys.length === 0 && !keyId)) {
		return null;
	}

	return (
		<div className="space-y-1.5">
			<Label id={`${idPrefix}-apikey-label`} className="text-xs">
				API Key <span className="text-muted-foreground">(optional; leave unset for load-balanced selection)</span>
			</Label>
			<div className="flex gap-1.5">
				<Select value={keyId || ""} onValueChange={onChange}>
					<SelectTrigger
						id={`${idPrefix}-apikey-select`}
						aria-labelledby={`${idPrefix}-apikey-label`}
						className="h-9 flex-1 text-sm"
						data-testid={`${idPrefix}-apikey-select`}
					>
						<SelectValue placeholder="Select key (optional)" />
					</SelectTrigger>
					<SelectContent>
						{availableKeys.map((key) => (
							<SelectItem key={key.id} value={key.id}>
								{key.name}
							</SelectItem>
						))}
						{keyId && !availableKeys.some((k) => k.id === keyId) && (
							<SelectItem key={`pinned-${keyId}`} value={keyId}>
								(pinned) {keyId}
							</SelectItem>
						)}
					</SelectContent>
				</Select>
				{keyId && (
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onChange("")}
						className="h-9 w-9 p-0"
						aria-label={clearLabel}
						data-testid={`${idPrefix}-apikey-clear`}
					>
						<X className="h-3.5 w-3.5" />
					</Button>
				)}
			</div>
		</div>
	);
}

interface FallbackRowProps {
	fallback: RoutingFallbackFormData;
	index: number;
	providerOptions: Array<{
		label: string;
		value: string;
		icon: React.ReactNode;
	}>;
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onUpdate: (index: number, changes: Partial<RoutingFallbackFormData>) => void;
	onRemove: (index: number) => void;
}

function FallbackRow({ fallback, index, providerOptions, allKeys, onUpdate, onRemove }: FallbackRowProps) {
	const provider = fallback.provider || "";

	return (
		<div className="space-y-2 rounded-lg border p-3" data-testid={`routing-fallback-${index}`}>
			<div className="flex items-center gap-2">
				<div className="flex-1">
					<ComboboxSelect
						options={providerOptions}
						value={provider || null}
						// A key belongs to one provider, so switching providers invalidates the pin.
						onValueChange={(value) => onUpdate(index, { provider: value ?? "", model: "", key_id: "" })}
						placeholder="Select provider..."
						className="h-9"
						data-testid={`routing-fallback-${index}-provider-select`}
						noPortal
					/>
				</div>
				<div className="flex-1" data-testid={`routing-fallback-${index}-model-select`}>
					<ModelMultiselect
						provider={provider || undefined}
						value={fallback.model || ""}
						onChange={(value) => onUpdate(index, { model: value })}
						placeholder="Incoming (optional)"
						isSingleSelect
						disabled={!provider}
						className="!h-9 !min-h-9 w-full"
					/>
				</div>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					onClick={() => onRemove(index)}
					className="h-9 px-2"
					aria-label={`Remove fallback ${index + 1}`}
					data-testid={`routing-fallback-${index}-remove-button`}
				>
					<Trash2 className="h-4 w-4" />
				</Button>
			</div>

			<ProviderKeySelect
				idPrefix={`routing-fallback-${index}`}
				clearLabel={`Clear API key for fallback ${index + 1}`}
				provider={provider}
				keyId={fallback.key_id}
				allKeys={allKeys}
				onChange={(value) => onUpdate(index, { key_id: value })}
			/>
		</div>
	);
}

interface TargetRowProps {
	target: RoutingTargetFormData;
	index: number;
	strategy: RoutingStrategy;
	providerOptions: Array<{
		label: string;
		value: string;
		icon: React.ReactNode;
	}>;
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	showRemove: boolean;
	onUpdate: (index: number, field: keyof RoutingTargetFormData, value: string | number | TargetModelItem[] | undefined) => void;
	onRemove: (index: number) => void;
	onUpdateModels?: (index: number, models: string[]) => void;
	onMoveModelPriority?: (index: number, modelIndex: number, direction: "up" | "down") => void;
	onSetModelPriority?: (index: number, model: string, priority: number) => void;
}

function TargetRow({
	target,
	index,
	strategy,
	providerOptions,
	allKeys,
	showRemove,
	onUpdate,
	onRemove,
	onUpdateModels,
	onMoveModelPriority,
	onSetModelPriority,
}: TargetRowProps) {
	const availableKeys = target.provider
		? allKeys.filter((k) => k.provider === target.provider).map((k) => ({ id: k.key_id, name: k.name }))
		: [];
	// Priority strategy has a dedicated priority rank input (bound to target.priority);
	// weighted/adaptive/group_adaptive show the probability weight input (bound to target.weight).
	const isPriority = strategy === "priority";
	const isGroupAdaptive = strategy === "group_adaptive";
	return (
		<div className="space-y-3 rounded-lg border p-3" data-testid={`routing-target-${index}`}>
			<div className="flex items-center justify-between">
				<span className="text-muted-foreground text-sm font-medium">Target {index + 1}</span>
				<div className="flex items-center gap-2">
					<div className="flex items-center gap-1.5">
						<Label htmlFor={`routing-target-${index}-weight-input`} className="text-muted-foreground shrink-0 text-xs">
							{isPriority ? "Priority" : "Weight"}
						</Label>
						{isPriority ? (
							<Input
								id={`routing-target-${index}-weight-input`}
								type="number"
								min={1}
								step={1}
								value={target.priority ?? index + 1}
								onChange={(e) => onUpdate(index, "priority", Number.parseInt(e.target.value, 10) || 1)}
								className="h-8 w-24 text-sm"
								data-testid={`routing-target-${index}-priority-input`}
							/>
						) : (
							<Input
								id={`routing-target-${index}-weight-input`}
								type="number"
								min={0.001}
								max={1}
								step={0.001}
								value={target.weight}
								onChange={(e) => onUpdate(index, "weight", parseFloat(e.target.value) || 0)}
								className="h-8 w-24 text-sm"
								data-testid={`routing-target-${index}-weight-input`}
							/>
						)}
					</div>
					{showRemove && (
						<Button
							type="button"
							variant="ghost"
							size="sm"
							onClick={() => onRemove(index)}
							className="h-8 w-8 p-0"
							aria-label={`Remove target ${index + 1}`}
							data-testid={`routing-target-${index}-remove-button`}
						>
							<Trash2 className="h-3.5 w-3.5" />
						</Button>
					)}
				</div>
			</div>

			<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
				<div className="space-y-1.5">
					<Label id={`routing-target-${index}-provider-label`} className="text-xs">
						Provider
					</Label>
					<div className="flex gap-1.5">
						<ComboboxSelect
							options={providerOptions}
							value={target.provider || null}
							onValueChange={(value) => {
								onUpdate(index, "provider", value ?? "");
								onUpdate(index, "model", "");
								onUpdate(index, "models", []);
								onUpdate(index, "key_id", "");
							}}
							placeholder="Incoming (optional)"
							className="h-9 flex-1 text-sm"
							data-testid={`routing-target-${index}-provider-select`}
							noPortal
						/>
						{target.provider && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => {
									onUpdate(index, "provider", "");
									onUpdate(index, "model", "");
									onUpdate(index, "models", []);
									onUpdate(index, "key_id", "");
								}}
								className="h-9 w-9 p-0"
								aria-label={`Clear provider for target ${index + 1}`}
								data-testid={`routing-target-${index}-provider-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>

				<div className="space-y-1.5">
					<Label id={`routing-target-${index}-model-label`} className="text-xs">
						Model {isGroupAdaptive && <span className="text-muted-foreground font-normal">(1 or more)</span>}
					</Label>
					<div className="flex gap-1.5">
						<div className="flex-1" data-testid={`routing-target-${index}-model-select`}>
							{isGroupAdaptive ? (
								<ModelMultiselect
									provider={target.provider || undefined}
									value={target.models?.map((m) => m.model) || (target.model ? [target.model] : [])}
									onChange={(models) => onUpdateModels?.(index, models)}
									placeholder="Incoming (optional)"
									loadModelsOnEmptyProvider
									className="!h-9 !min-h-9"
									inputId={`routing-target-${index}-model-input`}
									ariaLabelledBy={`routing-target-${index}-model-label`}
								/>
							) : (
								<ModelMultiselect
									provider={target.provider || undefined}
									value={target.model}
									onChange={(value) => onUpdate(index, "model", value)}
									placeholder="Incoming (optional)"
									isSingleSelect
									loadModelsOnEmptyProvider
									className="!h-9 !min-h-9"
									inputId={`routing-target-${index}-model-input`}
									ariaLabelledBy={`routing-target-${index}-model-label`}
								/>
							)}
						</div>
						{((isGroupAdaptive && target.models && target.models.length > 0) || (!isGroupAdaptive && target.model)) && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => {
									onUpdate(index, "model", "");
									onUpdate(index, "models", []);
								}}
								className="h-9 w-9 p-0"
								aria-label={`Clear model for target ${index + 1}`}
								data-testid={`routing-target-${index}-model-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>
			</div>

			{/* Group Adaptive: Model Priority Rotation List */}
			{isGroupAdaptive && target.models && target.models.length > 1 && (
				<div className="bg-muted/40 space-y-1.5 rounded-md border p-2.5 text-xs">
					<div className="text-muted-foreground flex items-center justify-between font-medium">
						<span>Model Priority Rotation (1 → 2 → 1)</span>
						<span className="text-[10px]">Lower number = higher precedence</span>
					</div>
					<div className="space-y-1">
						{target.models.map((item, mIdx) => (
							<div key={item.model} className="bg-background flex items-center justify-between gap-2 rounded border px-2.5 py-1.5">
								<span className="font-mono text-xs font-semibold">{item.model}</span>
								<div className="flex items-center gap-1.5">
									<Label className="text-muted-foreground text-[10px]">Priority:</Label>
									<Input
										type="number"
										min={1}
										value={item.priority}
										onChange={(e) => {
											const prio = parseInt(e.target.value, 10) || 1;
											onSetModelPriority?.(index, item.model, prio);
										}}
										className="h-6 w-14 text-center text-xs"
									/>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										className="h-6 w-6 p-0"
										onClick={() => onMoveModelPriority?.(index, mIdx, "up")}
										disabled={mIdx === 0}
									>
										<ChevronUp className="h-3 w-3" />
									</Button>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										className="h-6 w-6 p-0"
										onClick={() => onMoveModelPriority?.(index, mIdx, "down")}
										disabled={mIdx === target.models!.length - 1}
									>
										<ChevronDown className="h-3 w-3" />
									</Button>
								</div>
							</div>
						))}
					</div>
				</div>
			)}

			<ProviderKeySelect
				idPrefix={`routing-target-${index}`}
				clearLabel={`Clear API key for target ${index + 1}`}
				provider={target.provider}
				keyId={target.key_id}
				allKeys={allKeys}
				onChange={(value) => onUpdate(index, "key_id", value)}
			/>
		</div>
	);
}