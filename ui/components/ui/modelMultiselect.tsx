import { cn } from "@/components/ui/utils";
import { useLazyGetBaseModelsQuery, useLazyGetModelsQuery } from "@/lib/store/apis/providersApi";
import { X } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { components, MultiValueProps, OptionProps, SingleValueProps } from "react-select";
import { AsyncMultiSelect } from "./asyncMultiselect";
import { Option } from "./multiselectUtils";

const CHIP_ROW_RESERVE = 44; // right indicators + padding reserve
const CHIP_GAP = 4;

interface ModelMultiselectPropsBase {
	provider?: string;
	keys?: string[];
	vks?: string[];
	placeholder?: string;
	disabled?: boolean;
	className?: string;
	/** Load models even when no provider is selected.
	 * - `true`: loads all models from all providers
	 * - `"base_models"`: loads distinct base model names (useful for governance where cross-provider matching is needed)
	 */
	loadModelsOnEmptyProvider?: boolean | "base_models";
	/** Prepends an "Allow All Models" option (value: "*") to the dropdown */
	allowAllOption?: boolean;
	/** Hides the search icon rendered inside the control */
	hideSearchIcon?: boolean;
	/** id for the search input (accessibility) */
	inputId?: string;
	/** id of element that labels this control (accessibility) */
	ariaLabelledBy?: string;
	/** id of the element describing this control, e.g. a form error message (accessibility) */
	ariaDescribedBy?: string;
	/** marks the control invalid for assistive tech (accessibility) */
	ariaInvalid?: boolean;
	/** test selector for the container element */
	"data-testid"?: string;
	/** Menu position strategy. Use "absolute" inside popovers to avoid portal issues. Defaults to "fixed". */
	menuPosition?: "absolute" | "fixed";
	/** Target element for the menu portal. */
	menuPortalTarget?: HTMLElement | null;
	/** Custom rendering for a selected value chip (multi-select only). Defaults to the option label. */
	renderValueLabel?: (option: { label: string; value: string }) => ReactNode;
}

interface ModelMultiselectPropsSingle extends ModelMultiselectPropsBase {
	/** Single select mode - value and onChange will be string instead of string[] */
	isSingleSelect: true;
	unfiltered?: boolean;
	value: string;
	onChange: (model: string) => void;
	clearable?: boolean;
}

interface ModelMultiselectPropsMulti extends ModelMultiselectPropsBase {
	/** Multi select mode (default) - value and onChange will be string[] */
	isSingleSelect?: false;
	unfiltered?: boolean;
	value: string[];
	onChange: (models: string[]) => void;
	clearable?: boolean;
}

export type ModelMultiselectProps = ModelMultiselectPropsSingle | ModelMultiselectPropsMulti;

interface ModelOption {
	label: string;
	value: string;
	provider?: string;
	isDeprecated?: boolean;
	/** react-select reads this to make an option non-selectable */
	isDisabled?: boolean;
}

const ALL_MODELS_OPTION: ModelOption = { label: "All Models", value: "*" };

export function ModelMultiselect(props: ModelMultiselectProps) {
	const {
		provider,
		keys,
		vks,
		value,
		unfiltered = false,
		onChange,
		placeholder = "Search models...",
		disabled = false,
		className,
		loadModelsOnEmptyProvider = false,
		allowAllOption = false,
		clearable = false,
	} = props;
	const isSingleSelect = props.isSingleSelect === true;

	const [getModels, { data: modelsData, isFetching, isError }] = useLazyGetModelsQuery();
	const [getBaseModels, { data: baseModelsData, isFetching: isFetchingBaseModels, isError: isBaseModelsError }] =
		useLazyGetBaseModelsQuery();
	const [inputValue, setInputValue] = useState("");
	const inputValueRef = useRef("");
	const [isFocused, setIsFocused] = useState(false);

	// Convert value to options (handle both single and multi select)
	const stringValue = value as string;
	const arrayValue = value as string[];
	const selectedOptions: ModelOption[] = isSingleSelect
		? stringValue
			? [stringValue === "*" ? ALL_MODELS_OPTION : { label: stringValue, value: stringValue }]
			: []
		: arrayValue.map((model) => (model === "*" ? ALL_MODELS_OPTION : { label: model, value: model }));

	// ─── collapsed-display measurement ───────────────────────────────────────
	// When the control is not focused, the selected chips render on a single row.
	// How many fit depends on the control's width and on each model name's width,
	// so both are measured instead of hard-coding "show one chip + N more".
	// The overflow counter only appears when a chip genuinely does not fit, and a
	// name is only truncated when even a single chip is wider than the row.
	const wrapperRef = useRef<HTMLDivElement>(null);
	const measureRef = useRef<HTMLDivElement>(null);
	const [availableWidth, setAvailableWidth] = useState(0);
	const [chipWidths, setChipWidths] = useState<number[]>([]);
	const [moreWidth, setMoreWidth] = useState(0);

	const labelKey = selectedOptions.map((o) => o.label).join("\u0001");
	const isCollapsed = !isSingleSelect && !isFocused && inputValue.length === 0;

	useLayoutEffect(() => {
		const el = wrapperRef.current;
		if (!el) return;
		const update = () => setAvailableWidth(el.clientWidth);
		update();
		const observer = new ResizeObserver(update);
		observer.observe(el);
		return () => observer.disconnect();
	}, []);

	useLayoutEffect(() => {
		if (isSingleSelect) return;
		const root = measureRef.current;
		if (!root) return;
		const widths = Array.from(root.querySelectorAll<HTMLElement>("[data-measure-chip]"), (chip) => chip.offsetWidth);
		const measuredMore = root.querySelector<HTMLElement>("[data-measure-more]")?.offsetWidth ?? 0;
		setChipWidths((prev) => (prev.length === widths.length && prev.every((w, i) => w === widths[i]) ? prev : widths));
		setMoreWidth((prev) => (prev === measuredMore ? prev : measuredMore));
	}, [isSingleSelect, labelKey]);

	const { visibleCount, hiddenCount } = useMemo(() => {
		const total = selectedOptions.length;
		if (total === 0) return { visibleCount: 0, hiddenCount: 0 };
		if (!isCollapsed) return { visibleCount: total, hiddenCount: 0 };

		const rowWidth = availableWidth - CHIP_ROW_RESERVE;
		// Nothing measured yet: fall back to a single chip so the row never overflows.
		if (availableWidth <= 0 || chipWidths.length !== total) return { visibleCount: 1, hiddenCount: total - 1 };
		if (rowWidth <= 0) return { visibleCount: 1, hiddenCount: total - 1 };

		const spanOf = (count: number) => chipWidths.slice(0, count).reduce((sum, w) => sum + w, 0) + CHIP_GAP * (count - 1);
		if (spanOf(total) <= rowWidth) return { visibleCount: total, hiddenCount: 0 };
		for (let count = total - 1; count >= 1; count -= 1) {
			if (spanOf(count) + CHIP_GAP + moreWidth <= rowWidth) return { visibleCount: count, hiddenCount: total - count };
		}
		return { visibleCount: 1, hiddenCount: total - 1 };
	}, [availableWidth, chipWidths, isCollapsed, moreWidth, selectedOptions.length]);

	// The single visible chip is the only case where the row can still be too
	// narrow for the name itself; then — and only then — the label is truncated.
	const truncateSoleChip = isCollapsed && visibleCount === 1 && hiddenCount === 0 && chipWidths.length === 1 && chipWidths[0] > availableWidth - CHIP_ROW_RESERVE;

	// Determine if we should use base models (no provider selected + "base_models" mode)
	const shouldUseBaseModels = loadModelsOnEmptyProvider === "base_models" && !provider;
	const shouldLoadOnEmpty = !!loadModelsOnEmptyProvider;

	// Fetch initial models on mount or when provider/keys/vks change
	useEffect(() => {
		if (provider) {
			getModels({
				provider,
				keys: keys && keys.length > 0 ? keys : undefined,
				vks: vks && vks.length > 0 ? vks : undefined,
				limit: 5,
				unfiltered,
			});
		} else if (shouldUseBaseModels) {
			getBaseModels({ limit: 20 });
		} else if (shouldLoadOnEmpty) {
			getModels({
				keys: keys && keys.length > 0 ? keys : undefined,
				vks: vks && vks.length > 0 ? vks : undefined,
				limit: 20,
				unfiltered,
			});
		}
	}, [provider, keys, vks, getModels, getBaseModels, shouldLoadOnEmpty, shouldUseBaseModels]);

	// Load options function for AsyncMultiSelect
	const loadOptions = useCallback(
		(query: string, callback: (options: ModelOption[]) => void) => {
			// Prepend "Allow All Models" when allowAllOption is enabled and query matches (or is empty)
			const prefix: ModelOption[] = allowAllOption && (!query || "all models".includes(query.toLowerCase())) ? [ALL_MODELS_OPTION] : [];

			if (!provider && !shouldLoadOnEmpty) {
				callback(prefix);
				return;
			}

			if (shouldUseBaseModels) {
				getBaseModels({
					query: query || undefined,
					limit: query ? 50 : 20,
				})
					.unwrap()
					.then((response) => {
						const options = response.models.map((model) => ({
							label: model,
							value: model,
						}));
						callback([...prefix, ...options]);
					})
					.catch(() => {
						callback(prefix);
					});
			} else {
				getModels({
					query: query || undefined,
					provider: provider || undefined,
					keys: keys && keys.length > 0 ? keys : undefined,
					vks: vks && vks.length > 0 ? vks : undefined,
					limit: query ? 50 : shouldLoadOnEmpty && !provider ? 20 : 5,
					unfiltered,
				})
					.unwrap()
					.then((response) => {
						const options = response.models.map((model) => ({
							label: model.name,
							value: model.name,
							provider: model.provider,
							isDeprecated: model.is_deprecated,
							isDisabled: model.is_deprecated,
						}));
						callback([...prefix, ...options]);
					})
					.catch(() => {
						callback(prefix);
					});
			}
		},
		[getModels, getBaseModels, provider, keys, vks, shouldLoadOnEmpty, shouldUseBaseModels, allowAllOption],
	);

	// Handle selection change
	const handleChange = useCallback(
		(options: Option<ModelOption>[]) => {
			if (isSingleSelect) {
				const selected = options[0];
				(onChange as (model: string) => void)(selected?.value || "");
			} else {
				const modelNames = options.map((opt) => opt.value);
				(onChange as (models: string[]) => void)(modelNames);
			}

			// Refresh the list with current query to update available options
			const currentQuery = inputValueRef.current;
			if (provider) {
				getModels({
					query: currentQuery || undefined,
					provider,
					keys: keys && keys.length > 0 ? keys : undefined,
					vks: vks && vks.length > 0 ? vks : undefined,
					limit: currentQuery ? 20 : 5,
					unfiltered,
				});
			} else if (shouldUseBaseModels) {
				getBaseModels({
					query: currentQuery || undefined,
					limit: currentQuery ? 20 : 20,
				});
			} else if (shouldLoadOnEmpty) {
				getModels({
					query: currentQuery || undefined,
					keys: keys && keys.length > 0 ? keys : undefined,
					vks: vks && vks.length > 0 ? vks : undefined,
					limit: currentQuery ? 20 : 5,
					unfiltered,
				});
			}
		},
		[onChange, provider, keys, vks, getModels, getBaseModels, isSingleSelect, shouldLoadOnEmpty, shouldUseBaseModels],
	);

	// Handle input change - track in both state and ref
	// Per react-select docs: ignore input clear on blur, menu close, and set-value (selection)
	const handleInputChange = useCallback(
		(newValue: string, actionMeta: { action: string }) => {
			// Don't clear input on blur or menu close (preserves search while browsing)
			if (!isSingleSelect && (actionMeta.action === "input-blur" || actionMeta.action === "menu-close")) {
				return;
			}
			setInputValue(newValue);
			inputValueRef.current = newValue;
		},
		[isSingleSelect],
	);

	// Convert API data to options for default display
	const defaultOptions: ModelOption[] = useMemo(() => {
		const prefix = allowAllOption ? [ALL_MODELS_OPTION] : [];
		if (shouldUseBaseModels) {
			return [
				...prefix,
				...(baseModelsData?.models?.map((model) => ({
					label: model,
					value: model,
				})) || []),
			];
		}
		return [
			...prefix,
			...(modelsData?.models?.map((model) => ({
				label: model.name,
				value: model.name,
				provider: model.provider,
				isDeprecated: model.is_deprecated,
				isDisabled: model.is_deprecated,
			})) || []),
		];
	}, [modelsData, baseModelsData, shouldUseBaseModels, allowAllOption]);

	const shouldBeDisabled = disabled || (!provider && !shouldLoadOnEmpty);
	const modelsQueryEnabled = !!provider || shouldLoadOnEmpty;
	const activeIsFetching = shouldUseBaseModels ? isFetchingBaseModels : modelsQueryEnabled ? isFetching : false;
	const activeIsError = shouldUseBaseModels ? isBaseModelsError : modelsQueryEnabled ? isError : false;
	const modelLoadError = !activeIsFetching && activeIsError;

	return (
		<div ref={wrapperRef} className="relative w-full">
			{/* Off-screen measurement container: measures real browser-rendered widths */}
			{!isSingleSelect && (
				<div
					ref={measureRef}
					aria-hidden="true"
					className="pointer-events-none absolute -top-[9999px] left-0 flex items-center gap-1 opacity-0 whitespace-nowrap"
				>
					{selectedOptions.map((opt) => (
						<div
							key={opt.value}
							data-measure-chip
							className="bg-accent flex items-center gap-1 rounded-sm px-1.5 py-0.5 text-xs shrink-0 whitespace-nowrap"
						>
							<span>{opt.label}</span>
							<X className="h-3.5 w-3.5 shrink-0" />
						</div>
					))}
					<span
						data-measure-more
						className="bg-primary/10 text-primary border-primary/20 rounded border px-1.5 py-0.5 text-[10px] font-semibold whitespace-nowrap"
					>
						+99 more
					</span>
				</div>
			)}
			<AsyncMultiSelect<ModelOption>
				isSingleSelect={isSingleSelect}
				hideSelectedOptions
				hideSearchIcon={props.hideSearchIcon}
				inputId={props.inputId}
				ariaLabelledBy={props.ariaLabelledBy}
				ariaDescribedBy={props.ariaDescribedBy}
				ariaInvalid={props.ariaInvalid}
				data-testid={props["data-testid"]}
				value={selectedOptions}
				onChange={handleChange}
				reload={loadOptions}
				debounce={300}
				isCreatable={true}
				dynamicOptionCreation={true}
				createOptionText={"Press enter to add new model"}
				defaultOptions={defaultOptions.length > 0 ? defaultOptions : ([] as Option<ModelOption>[])}
				isLoading={activeIsFetching}
				placeholder={placeholder}
				disabled={shouldBeDisabled}
				className={cn(isCollapsed ? "!h-9 !min-h-9" : "!min-h-9", "w-full", className)}
				triggerClassName={cn(
					"!shadow-none !border-border px-1",
					isCollapsed ? "!h-9 !min-h-9 overflow-hidden flex-nowrap" : "!min-h-9 flex-wrap",
				)}
				menuClassName="!z-[100] max-h-[300px] overflow-y-auto w-full cursor-pointer custom-scrollbar"
				isClearable={clearable}
				closeMenuOnSelect={isSingleSelect}
				menuPlacement="auto"
				menuPosition={props.menuPosition}
				menuPortalTarget={props.menuPortalTarget}
				menuListClassName="mx-1"
				inputValue={inputValue}
				onInputChange={handleInputChange}
				onFocus={() => setIsFocused(true)}
				onBlur={() => setIsFocused(false)}
				onMenuOpen={() => setIsFocused(true)}
				onMenuClose={() => setIsFocused(false)}
				noResultsFoundPlaceholder={modelLoadError ? "Couldn’t load models." : "No matching models."}
				emptyResultPlaceholder={
					modelLoadError
						? "Couldn’t load models."
						: provider
							? "No models available for this provider."
							: shouldLoadOnEmpty
								? "No models available."
								: "Select a provider first."
				}
				views={{
					dropdownIndicator: isSingleSelect ? undefined : () => <></>,
					singleValue: isSingleSelect
						? (singleValueProps: SingleValueProps<ModelOption>) => (
								<span className="absolute left-1.5 text-sm">{singleValueProps.data.label}</span>
							)
						: undefined,
					multiValue: isSingleSelect
						? undefined
						: (multiValueProps: MultiValueProps<ModelOption>) => {
								if (isCollapsed && multiValueProps.index >= visibleCount) {
									return null;
								}
								const isLastVisible = isCollapsed && multiValueProps.index === visibleCount - 1;

								return (
									<div
										{...multiValueProps.innerProps}
										className={cn(
											"bg-accent dark:!bg-card flex cursor-pointer items-center gap-1 rounded-sm px-1.5 py-0.5 text-xs shrink-0",
											truncateSoleChip ? "max-w-[calc(100%-12px)]" : "max-w-full",
										)}
									>
										<span className={cn(truncateSoleChip && "truncate")}>
											{props.renderValueLabel ? props.renderValueLabel(multiValueProps.data) : multiValueProps.data.label}
										</span>
										<X
											className="hover:text-foreground text-muted-foreground h-3.5 w-3.5 shrink-0 cursor-pointer"
											onClick={(e) => {
												e.stopPropagation();
												multiValueProps.removeProps.onClick?.(e as any);
											}}
										/>
										{isLastVisible && hiddenCount > 0 && (
											<span className="bg-primary/10 text-primary border-primary/20 ml-1 shrink-0 rounded border px-1.5 py-0.5 text-[10px] font-semibold whitespace-nowrap">
												+{hiddenCount} more
											</span>
										)}
									</div>
								);
							},
					option: (optionProps: OptionProps<ModelOption>) => {
						const { Option } = components;
						const isDeprecated = optionProps.data.isDeprecated;
						return (
							<Option
								{...optionProps}
								className={cn(
									"flex w-full items-center gap-2 rounded-sm px-2 py-2 text-sm",
									isDeprecated ? "cursor-not-allowed opacity-60" : "cursor-pointer hover:bg-accent",
									!isDeprecated && optionProps.isFocused && "bg-accent dark:!bg-card",
									!isDeprecated && optionProps.isSelected && "bg-accent dark:!bg-card",
								)}
							>
								<span className={cn("grow truncate text-sm", isDeprecated && "text-muted-foreground")}>{optionProps.data.label}</span>
								{isDeprecated && (
									<span className="text-muted-foreground border-border shrink-0 rounded-sm border px-1.5 py-0.5 text-[10px] font-medium tracking-wide uppercase">
										Deprecated
									</span>
								)}
							</Option>
						);
					},
				}}
			/>
		</div>
	);
}