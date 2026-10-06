import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter, DialogDescription, DialogTrigger } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Label } from "@/components/ui/label";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { SettingsIcon, Trash, Play } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { getEndpointUrl } from "@/lib/utils/port";
import { useGetProviderKeysQuery } from "@/lib/store/apis/providersApi";
import ProviderConfigSheet from "../dialogs/providerConfigSheet";
import ModelProviderKeysTableView from "./modelProviderKeysTableView";

interface Props {
	provider: ModelProvider;
	onRequestDelete?: () => void;
}

function TestProviderDialog({ provider, isKeyless, children }: { provider: ModelProvider; isKeyless: boolean; children: React.ReactNode }) {
	const [isOpen, setIsOpen] = useState(false);
	const { data: keys = [] } = useGetProviderKeysQuery(provider.name);
	const [isTesting, setIsTesting] = useState(false);

	const [selectedModel, setSelectedModel] = useState<string>("");
	const [reasoningEffort, setReasoningEffort] = useState<string>("none");

	const noKeys = !isKeyless && keys.length === 0;

	const handleTest = async () => {
		const modelToUse = selectedModel || "default";
		setIsTesting(true);
		
		const num1 = Math.floor(Math.random() * 10) + 1;
		const num2 = Math.floor(Math.random() * 10) + 1;
		
		const payload: any = {
			model: `${provider.name}/${modelToUse}`,
			messages: [{ role: "user", content: `return only calculation for ${num1} + ${num2} = ?` }],
		};

		if (reasoningEffort !== "none") {
			payload.reasoning_effort = reasoningEffort;
		}

		const promise = fetch(getEndpointUrl("/v1/chat/completions"), {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(payload),
		}).then(async (res) => {
			if (!res.ok) {
				const text = await res.text();
				throw new Error(text);
			}
			const data = await res.json();
			if (data.error) {
				throw new Error(data.error.message || JSON.stringify(data.error));
			}
			return data.choices?.[0]?.message?.content || "No response";
		});

		toast.promise(promise, {
			loading: `Testing ${provider.name}...`,
			success: (data) => `Test success: ${data}`,
			error: (err) => `Test failed: ${err.message}`,
		});

		promise.finally(() => {
			setIsTesting(false);
			setIsOpen(false);
		});
	};

	return (
		<Dialog open={isOpen} onOpenChange={setIsOpen}>
			<DialogTrigger asChild>{children}</DialogTrigger>
			<DialogContent onClick={(e) => e.stopPropagation()}>
				<DialogHeader>
					<DialogTitle>Test Provider: {provider.name}</DialogTitle>
					<DialogDescription>
						Send a simple calculation test to verify the provider connection.
					</DialogDescription>
				</DialogHeader>

				<div className="flex flex-col gap-4 py-4">
					{noKeys ? (
						<div className="text-sm font-medium text-destructive">
							Error: This provider does not have any keys configured. Please add a key first to test it.
						</div>
					) : (
						<>
							<div className="flex flex-col gap-2">
								<Label>Model</Label>
								<ModelMultiselect
									provider={provider.name}
									value={selectedModel}
									onChange={(v) => setSelectedModel(v)}
									isSingleSelect
									placeholder="Select or type a model"
									unfiltered={true}
									menuPortalTarget={document.body}
								/>
							</div>
							<div className="flex flex-col gap-2">
								<Label>Reasoning Effort (Optional)</Label>
								<Select value={reasoningEffort} onValueChange={setReasoningEffort}>
									<SelectTrigger>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="none">None</SelectItem>
										<SelectItem value="low">Low</SelectItem>
										<SelectItem value="medium">Medium</SelectItem>
										<SelectItem value="high">High</SelectItem>
									</SelectContent>
								</Select>
							</div>
						</>
					)}
				</div>

				<DialogFooter>
					<Button variant="outline" onClick={() => setIsOpen(false)}>
						Cancel
					</Button>
					<Button disabled={noKeys || isTesting} onClick={handleTest}>
						Send Test
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

export default function ModelProviderConfig({ provider, onRequestDelete }: Props) {
	const [showConfigSheet, setShowConfigSheet] = useState(false);
	const hasDeleteProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);

	const showApiKeys = useMemo(() => {
		if (provider.custom_provider_config) {
			return !(provider.custom_provider_config?.is_key_less ?? false);
		}
		if (provider.name === "opencode-zen-free") {
			return false;
		}
		return true;
	}, [provider.name, provider.custom_provider_config?.is_key_less]);

	const editConfigButton = (
		<div className="flex min-w-0 flex-wrap items-center gap-2">
			<TestProviderDialog provider={provider} isKeyless={!showApiKeys}>
				<Button
					variant="outline"
					className="size-9 px-0 xl:h-9 xl:w-auto xl:px-4"
					aria-label="Test provider"
					data-testid="provider-test-btn"
				>
					<Play className="h-4 w-4" />
					<span className="hidden xl:inline ml-2">Test</span>
				</Button>
			</TestProviderDialog>
			{onRequestDelete && hasDeleteProviderAccess && (
				<Button
					variant="outline"
					onClick={onRequestDelete}
					className="text-destructive hover:bg-destructive/10 hover:text-destructive size-9 px-0"
					aria-label="Delete provider"
					data-testid="provider-delete-btn"
				>
					<Trash className="h-4 w-4" />
				</Button>
			)}
			<Tooltip>
				<TooltipTrigger asChild>
					<Button
						variant="outline"
						className="size-9 px-0 xl:h-9 xl:w-auto xl:px-4"
						onClick={() => setShowConfigSheet(true)}
						aria-label={hasUpdateProviderAccess ? "Edit provider configuration" : "View provider configuration"}
					>
						<SettingsIcon className="h-4 w-4" />
						<span className="hidden xl:inline ml-2">{hasUpdateProviderAccess ? "Edit Provider Config" : "View Provider Config"}</span>
					</Button>
				</TooltipTrigger>
				<TooltipContent className="xl:hidden">{hasUpdateProviderAccess ? "Edit Provider Config" : "View Provider Config"}</TooltipContent>
			</Tooltip>
		</div>
	);

	return (
		<div className="flex w-full flex-col gap-2">
			<ProviderConfigSheet show={showConfigSheet} onCancel={() => setShowConfigSheet(false)} provider={provider} />
			<ModelProviderKeysTableView provider={provider} headerActions={editConfigButton} isKeyless={!showApiKeys} />
		</div>
	);
}