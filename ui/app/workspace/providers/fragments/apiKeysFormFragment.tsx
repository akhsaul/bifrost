import { ModelAccessSelector } from "@/components/modelAccess";
import { Button } from "@/components/ui/button";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tagInput";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import {
	useLazyGetAntigravityAuthUrlQuery,
	useExchangeAntigravityAuthCodeMutation,
	useStartClineDeviceFlowMutation,
	usePollClineDeviceAuthMutation,
	type ClineDeviceChallenge,
} from "@/lib/store/apis/providersApi";
import { hasAntigravityOAuthRefresh, hasClineApiToken, hasClineOAuthRefresh, hasCopilotApiToken, isRedacted } from "@/lib/utils/validation";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { CheckCircle2, Info, Loader2, RefreshCw, Copy, Check } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";
import { toast } from "sonner";
import { DeploymentsTable } from "./deploymentsTable";

// Providers that support batch APIs
const BATCH_SUPPORTED_PROVIDERS = ["openai", "bedrock", "anthropic", "gemini", "azure", "vertex", "wafer"];

interface Props {
	control: Control<any>;
	providerName: string;
	// For custom providers, the underlying base provider type (e.g. "bedrock").
	// Drives which credential UI renders; falls back to providerName for native providers.
	baseProviderType?: string;
	form: UseFormReturn<any>;
}

// Batch API form field for all providers
function BatchAPIFormField({ control }: { control: Control<any>; form: UseFormReturn<any> }) {
	return (
		<FormField
			control={control}
			name={`key.use_for_batch_api`}
			render={({ field }) => (
				<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
					<div className="space-y-1.5">
						<FormLabel>Use for Batch APIs</FormLabel>
						<FormDescription>
							Enable this key for batch API operations. Only keys with this enabled will be used for batch requests.
						</FormDescription>
					</div>
					<FormControl>
						<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
					</FormControl>
				</FormItem>
			)}
		/>
	);
}

// AWS endpoint services Bifrost dials for Bedrock. `name` is the config field, `placeholder` the
// DNS name shape for that service - S3 differs from the rest, so each is spelled out.
const BEDROCK_VPC_ENDPOINT_SERVICES = [
	{
		name: "runtime",
		label: "Runtime",
		description: "Serves all inference.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "control_plane",
		label: "Control Plane",
		description: "Serves model listing and batch jobs.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "mantle",
		label: "Mantle",
		description: "Serves mantle-routed models.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-mantle.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "agent_runtime",
		label: "Agent Runtime",
		description: "Serves rerank.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-agent-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "s3",
		label: "S3",
		description: "Serves batch file I/O. Requires the bucket-prefixed endpoint name. A Gateway endpoint needs no value here.",
		placeholder: "bucket.vpce-0abc123-x1y2z3.s3.us-east-1.vpce.amazonaws.com",
	},
];

// VPC endpoint host overrides for AWS PrivateLink. Collapsed by default: most deployments reach
// Bedrock over the public regional endpoints and never set these.
function VPCEndpointsFormField({
	control,
	configKey,
	services,
}: {
	control: Control<any>;
	configKey: string;
	services: typeof BEDROCK_VPC_ENDPOINT_SERVICES;
}) {
	return (
		<Accordion type="single" collapsible className="w-full">
			<AccordionItem value="vpc-endpoints" className="rounded-sm border px-2 last:border-b">
				<AccordionTrigger className="py-2 hover:no-underline" data-testid="bedrock-vpc-endpoints-trigger">
					<span className="block space-y-1.5 pr-2">
						<span className="block text-sm leading-none font-medium">VPC Endpoints (Optional)</span>
						<span className="text-muted-foreground block text-sm font-normal">
							Route traffic through interface VPC endpoints instead of the public regional endpoints. Use each endpoint&apos;s DNS name from
							the VPC console, not its ID. Region is still required — it sets the request signing scope.
						</span>
					</span>
				</AccordionTrigger>
				<AccordionContent className="space-y-4 pt-2 pb-3">
					{services.map((service) => (
						<FormField
							key={service.name}
							control={control}
							name={`${configKey}.endpoints.${service.name}`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{service.label}</FormLabel>
									<FormDescription>{service.description}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid={`apikey-bedrock-endpoint-${service.name}-input`}
											placeholder={service.placeholder}
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					))}
				</AccordionContent>
			</AccordionItem>
		</Accordion>
	);
}

export function ApiKeyFormFragment({ control, providerName, baseProviderType, form }: Props) {
	// Credential UI keys off the base provider type for custom providers; the
	// model list, deployments table, and API calls still use the real providerName.
	const effectiveProvider = baseProviderType ?? providerName;
	const isBedrock = effectiveProvider === "bedrock";
	const isBedrockMantle = effectiveProvider === "bedrock_mantle";
	const isVertex = effectiveProvider === "vertex";
	const isAzure = effectiveProvider === "azure";
	const isReplicate = effectiveProvider === "replicate";
	const isVLLM = effectiveProvider === "vllm";
	const isOllama = effectiveProvider === "ollama";
	const isSGL = effectiveProvider === "sgl";
	const isDeepseek = effectiveProvider === "deepseek";
	const isFireworks = effectiveProvider === "fireworks";
	const isAntigravity = effectiveProvider === "antigravity";
	const isDatabricks = effectiveProvider === "databricks";
	const isGithubCopilot = effectiveProvider === "github-copilot";
	const isCline = effectiveProvider === "cline";
	// Reactive, so the App-credential labels stay truthful. Once a Copilot token is present
	// those fields genuinely are optional, and a static "(Required)" would contradict the
	// section note telling the operator they can leave them blank.
	const copilotAppSuffix = hasCopilotApiToken(form.watch("key.value")) ? "(Optional)" : "(Required)";
	// Reactive, so the OAuth labels stay truthful. Once a static Cline key is present
	// the refresh token genuinely is optional, and vice versa.
	const clineOAuthSuffix = hasClineApiToken(form.watch("key.value")) ? "(Optional)" : "(Required)";
	const clineKeySuffix = hasClineOAuthRefresh(form.watch("key.cline_key_config.refresh_token")) ? "(Optional)" : "";
	const isKeylessProvider = isOllama || isSGL;
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(effectiveProvider);

	// Auth type state for Azure: 'api_key', 'entra_id', or 'default_credential'
	const [azureAuthType, setAzureAuthType] = useState<"api_key" | "entra_id" | "default_credential">("api_key");

	// Auth type state for Bedrock: 'iam_role', 'explicit', or 'api_key'
	const [bedrockAuthType, setBedrockAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Bedrock Mantle: 'iam_role', 'explicit', or 'api_key'
	const [bedrockMantleAuthType, setBedrockMantleAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Databricks: 'pat' (personal access token) or 'oauth_m2m' (service principal)
	const [databricksAuthType, setDatabricksAuthType] = useState<"pat" | "oauth_m2m">("pat");

	// Auth type state for Vertex: 'service_account', 'service_account_json', or 'api_key'
	const [vertexAuthType, setVertexAuthType] = useState<"service_account" | "service_account_json" | "api_key">("service_account");

	// Auth type state for Antigravity: 'oauth' or 'manual'
	const [antigravityAuthType, setAntigravityAuthType] = useState<"oauth" | "manual">("oauth");
	// Pinned loopback redirect: the shared Google OAuth client only accepts
	// http://localhost:8085, so auth-url generation and code exchange must use
	// the same URI regardless of which port Bifrost itself serves on.
	const ANTIGRAVITY_REDIRECT_URI = "http://localhost:8085";
	const [manualCode, setManualCode] = useState("");
	const [manualRedirectUri, setManualRedirectUri] = useState<string>("http://localhost:8085");
	const [hasCopiedLink, setHasCopiedLink] = useState(false);
	const [isCopyingUrl, setIsCopyingUrl] = useState(false);
	const [authError, setAuthError] = useState<string | null>(null);
	// True once the operator clicks Authenticate: reveals the waiting box
	// (disabled popup placeholder + copy link + paste box), Cline-style.
	const [antigravityAuthStarted, setAntigravityAuthStarted] = useState(false);
	const [getAuthUrl, { isLoading: isFetchingUrl }] = useLazyGetAntigravityAuthUrlQuery();
	const [exchangeCode, { isLoading: isExchanging }] = useExchangeAntigravityAuthCodeMutation();

	// Cline OAuth (WorkOS device flow) state. WorkOS sends no approval callback,
	// so the UI polls the poll endpoint every second until the flow resolves.
	const [startClineDeviceFlow, { isLoading: isStartingClineFlow }] = useStartClineDeviceFlowMutation();
	const [pollClineDeviceAuth] = usePollClineDeviceAuthMutation();
	const [clineChallenge, setClineChallenge] = useState<ClineDeviceChallenge | null>(null);
	const [clinePolling, setClinePolling] = useState(false);
	const [clineError, setClineError] = useState<string | null>(null);
	const [clineExpiresLeft, setClineExpiresLeft] = useState(0);
	const [clineCopied, setClineCopied] = useState<"url" | "code" | null>(null);
	const clinePollTimer = useRef<ReturnType<typeof setInterval> | null>(null);
	const clinePollActive = useRef(false);

	// Detect Antigravity auth type
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isAntigravity) {
			const authType = form.getValues("key.antigravity_key_config._auth_type");
			if (authType) {
				setAntigravityAuthType(authType);
			} else {
				const hasManualCreds = Boolean(
					form.getValues("key.antigravity_key_config.client_id")?.value ||
					form.getValues("key.antigravity_key_config.client_secret")?.value,
				);
				if (hasManualCreds) {
					setAntigravityAuthType("manual");
					form.setValue("key.antigravity_key_config._auth_type", "manual");
				} else {
					setAntigravityAuthType("oauth");
					form.setValue("key.antigravity_key_config._auth_type", "oauth");
				}
			}
		}
	}, [isAntigravity, form, form.formState.defaultValues]);

	const handleGoogleLogin = async () => {
		setAuthError(null);
		setAntigravityAuthStarted(true);
		setIsCopyingUrl(true);
		try {
			const res = await getAuthUrl({ redirect_uri: ANTIGRAVITY_REDIRECT_URI }).unwrap();
			if (!res.auth_url) throw new Error("No authorization URL returned");

			await navigator.clipboard.writeText(res.auth_url);
			setManualRedirectUri(ANTIGRAVITY_REDIRECT_URI);
			setHasCopiedLink(true);
			setTimeout(() => setHasCopiedLink(false), 2500);
			toast.success("Google OAuth URL copied to clipboard! Open it in your local browser, then paste the redirect URL below.");
		} catch (err: unknown) {
			setAuthError(getErrorMessage(err));
			toast.error("Failed to generate Google OAuth URL");
		} finally {
			setIsCopyingUrl(false);
		}
	};

	const handleCopyGoogleLoginUrl = async () => {
		setAuthError(null);
		setIsCopyingUrl(true);
		try {
			const res = await getAuthUrl({ redirect_uri: ANTIGRAVITY_REDIRECT_URI }).unwrap();
			if (!res.auth_url) throw new Error("No authorization URL returned");

			await navigator.clipboard.writeText(res.auth_url);
			setManualRedirectUri(ANTIGRAVITY_REDIRECT_URI);
			setHasCopiedLink(true);
			setTimeout(() => setHasCopiedLink(false), 2500);
			toast.success("Google OAuth URL copied to clipboard! Open it in your local browser, then paste the redirect URL below.");
		} catch (err: unknown) {
			setAuthError(getErrorMessage(err));
			toast.error("Failed to copy Google OAuth URL");
		} finally {
			setIsCopyingUrl(false);
		}
	};

	const completeExchange = async (code: string, redirectUri?: string) => {
		setAuthError(null);
		try {
			const uri = redirectUri || ANTIGRAVITY_REDIRECT_URI;
			const res = await exchangeCode({
				code: code.trim(),
				redirect_uri: uri,
			}).unwrap();

			form.setValue("key.value", { value: "", ref: "" }, { shouldDirty: true, shouldValidate: true });
			form.setValue("key.antigravity_key_config.refresh_token", { value: res.refresh_token, ref: "" }, { shouldDirty: true });
			if (res.project_id) {
				form.setValue("key.antigravity_key_config.project_id", { value: res.project_id, ref: "" }, { shouldDirty: true });
			}
			const currentName = form.getValues("key.name");
			if (!currentName || currentName === "google-antigravity-auth" || currentName.startsWith("oauth-antigravity-")) {
				const keyName = res.email ? `oauth-antigravity-${res.email}` : "oauth-antigravity-account";
				form.setValue("key.name", keyName, { shouldDirty: true, shouldValidate: true });
			}
			toast.success(res.email ? `Connected as ${res.email}!` : "Antigravity Google OAuth connected successfully!");
			setManualCode("");
			setAntigravityAuthStarted(false);
		} catch (err: unknown) {
			setAuthError(getErrorMessage(err));
		}
	};

	const stopClinePolling = () => {
		clinePollActive.current = false;
		setClinePolling(false);
		if (clinePollTimer.current) {
			clearInterval(clinePollTimer.current);
			clinePollTimer.current = null;
		}
	};

	// Stop polling if the form unmounts mid-flow.
	useEffect(() => {
		return () => {
			clinePollActive.current = false;
			if (clinePollTimer.current) clearInterval(clinePollTimer.current);
		};
	}, []);

	const handleClineConnect = async () => {
		setClineError(null);
		stopClinePolling();
		try {
			const challenge = await startClineDeviceFlow({}).unwrap();
			if (!challenge?.device_code || !challenge?.verification_uri_complete) {
				throw new Error("No device challenge returned");
			}
			setClineChallenge(challenge);
			setClineExpiresLeft(challenge.expires_in || 300);
			// A new tab is opened automatically; popup blockers may stop it, in
			// which case the operator uses the Open tab / Copy URL buttons below.
			window.open(challenge.verification_uri_complete, "_blank", "noopener,noreferrer");
			clinePollActive.current = true;
			setClinePolling(true);
			clinePollTimer.current = setInterval(() => {
				void pollClineOnce(challenge.device_code);
			}, 1000);
		} catch (err: any) {
			setClineError(err?.data?.error || err?.message || "Failed to start Cline device flow");
		}
	};

	const pollClineOnce = async (deviceCode: string) => {
		if (!clinePollActive.current) return;
		try {
			const res = await pollClineDeviceAuth({ device_code: deviceCode }).unwrap();
			if (!clinePollActive.current) return;
			if (res.status === "success" && res.refresh_token) {
				stopClinePolling();
				form.setValue("key.cline_key_config.refresh_token", { value: res.refresh_token, ref: "" }, { shouldDirty: true });
				const currentName = form.getValues("key.name");
				if (!currentName || currentName.startsWith("oauth-cline-")) {
					form.setValue("key.name", res.email ? `oauth-cline-${res.email}` : "oauth-cline-account", {
						shouldDirty: true,
						shouldValidate: true,
					});
				}
				setClineChallenge(null);
				toast.success(res.email ? `Cline connected as ${res.email}!` : "Cline OAuth connected successfully!");
			} else if (res.status === "expired" || res.status === "denied" || res.status === "error") {
				stopClinePolling();
				setClineError(res.message || `Device flow ${res.status}. Start over to get a new code.`);
			}
			// "pending" (and slow_down notes) keep the 1s cadence going.
			setClineExpiresLeft((s) => {
				if (s <= 1) {
					stopClinePolling();
					setClineError("The device code expired. Start over to get a new one.");
					return 0;
				}
				return s - 1;
			});
		} catch (err: any) {
			// A single failed poll (network blip, 5xx) must not kill the flow;
			// the next tick retries. Only expired/denied stop it, via the body above.
			if (!clinePollActive.current) return;
		}
	};

	const handleClineCopy = async (kind: "url" | "code") => {
		if (!clineChallenge) return;
		try {
			await navigator.clipboard.writeText(kind === "url" ? clineChallenge.verification_uri_complete : clineChallenge.user_code);
			setClineCopied(kind);
			setTimeout(() => setClineCopied(null), 2500);
		} catch {
			toast.error("Failed to copy to clipboard");
		}
	};

	// Detect auth type from existing form values when editing
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isAzure) {
			const clientId = form.getValues("key.azure_key_config.client_id");
			const clientSecret = form.getValues("key.azure_key_config.client_secret");
			const tenantId = form.getValues("key.azure_key_config.tenant_id");
			const apiKey = form.getValues("key.value");
			const hasEntraField =
				clientId?.value || clientId?.ref || clientSecret?.value || clientSecret?.ref || tenantId?.value || tenantId?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "api_key" | "entra_id" | "default_credential" = "api_key";
			if (hasEntraField) {
				detected = "entra_id";
			} else if (!hasApiKey) {
				detected = "default_credential";
			}
			setAzureAuthType(detected);
			form.setValue("key.azure_key_config._auth_type", detected);
		}
	}, [isAzure, form]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isVertex) {
			const authCredentials = form.getValues("key.vertex_key_config.auth_credentials")?.value;
			const authCredentialsEnv = form.getValues("key.vertex_key_config.auth_credentials")?.ref;
			const apiKey = form.getValues("key.value")?.value;
			const apiKeyEnv = form.getValues("key.value")?.ref;
			let detected: "service_account" | "service_account_json" | "api_key" = "service_account";
			if (authCredentials || authCredentialsEnv) {
				detected = "service_account_json";
			} else if (apiKey || apiKeyEnv) {
				detected = "api_key";
			}
			setVertexAuthType(detected);
			form.setValue("key.vertex_key_config._auth_type", detected);
		}
	}, [isVertex, form]);

	const databricksDefaults = form.formState.defaultValues?.key?.databricks_key_config;
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isDatabricks) {
			const clientId = form.getValues("key.databricks_key_config.client_id");
			const clientSecret = form.getValues("key.databricks_key_config.client_secret");
			const hasServicePrincipal = clientId?.value || clientId?.ref || clientSecret?.value || clientSecret?.ref;
			const detected: "pat" | "oauth_m2m" = hasServicePrincipal ? "oauth_m2m" : "pat";
			setDatabricksAuthType(detected);
			form.setValue("key.databricks_key_config._auth_type", detected);
		}
		// databricksDefaults re-runs detection after the key form resets itself, which
		// happens once the key resolves - after this effect has already run once against
		// an empty form and settled on the personal access token tab.
	}, [isDatabricks, form, databricksDefaults]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isBedrock) {
			const accessKey = form.getValues("key.bedrock_key_config.access_key");
			const secretKey = form.getValues("key.bedrock_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.ref || secretKey?.value || secretKey?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "iam_role" | "explicit" | "api_key" = "iam_role";
			if (hasExplicitCreds) {
				detected = "explicit";
			} else if (hasApiKey) {
				detected = "api_key";
			}
			setBedrockAuthType(detected);
			form.setValue("key.bedrock_key_config._auth_type", detected);
		}
	}, [isBedrock, form]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isBedrockMantle) {
			const accessKey = form.getValues("key.bedrock_mantle_key_config.access_key");
			const secretKey = form.getValues("key.bedrock_mantle_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.ref || secretKey?.value || secretKey?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "iam_role" | "explicit" | "api_key" = "iam_role";
			if (hasExplicitCreds) {
				detected = "explicit";
			} else if (hasApiKey) {
				detected = "api_key";
			}
			setBedrockMantleAuthType(detected);
			form.setValue("key.bedrock_mantle_key_config._auth_type", detected);
		}
		// form.formState.defaultValues is a dependency so detection re-runs when ProviderKeyForm
		// repopulates an existing key via form.reset(...) after mount, not only on first render.
	}, [isBedrockMantle, form, form.formState.defaultValues]);

	return (
		<div data-tab="api-keys" className="space-y-4 overflow-hidden">
			<div className="flex items-start gap-4">
				<div className="flex-1">
					<FormField
						control={control}
						name={`key.name`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Name</FormLabel>
								<FormControl>
									<Input placeholder="Production Key" type="text" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
				<FormField
					control={control}
					name={`key.weight`}
					render={({ field }) => (
						<FormItem>
							<div className="flex items-center gap-2">
								<FormLabel>Weight</FormLabel>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span>
												<Info className="text-muted-foreground h-3 w-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent className="max-w-sm">
											<p>
												Determines traffic distribution between keys. Higher weights receive more requests. Not used when adaptive load
												balancing is enabled - key selection is then based on live performance.
											</p>
										</TooltipContent>
									</Tooltip>
								</TooltipProvider>
							</div>
							<FormControl>
								<Input
									placeholder="1.0"
									className="w-[260px]"
									value={field.value === undefined || field.value === null ? "" : String(field.value)}
									onChange={(e) => {
										// Keep as string during typing to allow partial input
										field.onChange(e.target.value === "" ? "" : e.target.value);
									}}
									onBlur={(e) => {
										const v = e.target.value.trim();
										if (v !== "") {
											const num = parseFloat(v);
											if (!isNaN(num)) {
												field.onChange(num);
											}
										}
										field.onBlur();
									}}
									name={field.name}
									ref={field.ref}
									type="text"
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			</div>
			{/* Hide API Key field for providers with dedicated auth tabs */}
			{!isAzure && !isBedrock && !isBedrockMantle && !isVertex && !isAntigravity && !isDatabricks && !isGithubCopilot && (
				<FormField
					control={control}
					name={`key.value`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>
								{isGithubCopilot ? "Copilot API Token" : isCline ? "Cline API Key" : "API Key"}{" "}
								{isVLLM || isGithubCopilot ? "(Optional)" : isCline ? clineKeySuffix : ""}
							</FormLabel>
							{isGithubCopilot && (
								<FormDescription>
									Requires Network Config &gt; Base URL set to the host the token was issued for, because a Copilot token does not carry
									one. Also expires after about 30 minutes, and Bifrost cannot refresh a token it did not mint, so prefer the GitHub App
									below for anything long-running.
								</FormDescription>
							)}
							{isCline && (
								<FormDescription>
									A static key from the Cline dashboard, or leave blank to use OAuth below. OAuth needs no manual key: Bifrost exchanges the
									refresh token for short-lived access tokens automatically.
								</FormDescription>
							)}
							<FormControl>
								<SecretVarInput
									placeholder={
										isGithubCopilot
											? "Copilot API token, or leave blank to use a GitHub App"
											: isCline
												? "Cline API key, or leave blank to use OAuth"
												: "API Key or env.MY_KEY"
									}
									type="text"
									{...field}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
			<>
				<FormField
					control={control}
					name={`key.models`}
					render={({ field }) => (
						<FormItem>
							<FormControl>
								<ModelAccessSelector
									mode="allow"
									data-testid="api-keys-models-multiselect"
									provider={providerName}
									unfiltered
									value={field.value || []}
									onChange={field.onChange}
									label={
										<>
											<FormLabel>Allowed Models</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent className="max-w-sm">
														<p>
															Select specific models this key applies to, or choose "Allow All Models" to allow all. Leave empty to deny
															all. Aliases must be added by their alias name - listing only the underlying model does not allow the alias
															(an alias best-model → gpt-4o requires "best-model" here, not just "gpt-4o").
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</>
									}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.blacklisted_models`}
					render={({ field }) => (
						<FormItem data-testid="apikey-blacklisted-models-field">
							<FormControl>
								<ModelAccessSelector
									mode="block"
									data-testid="api-keys-blocked-models-multiselect"
									provider={providerName}
									unfiltered
									value={field.value || []}
									onChange={field.onChange}
									label={
										<>
											<FormLabel>Blocked Models</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent className="max-w-sm">
														<p>
															Models this key must never serve. The denylist always wins - if a model appears in both Allowed Models and
															here, it is blocked. Select "All Models" to block every model on this key. Aliases are matched by their alias
															name - blocking only the underlying model does not block aliases that point to it.
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</>
									}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.aliases`}
					render={({ field }) => (
						<FormItem data-testid="apikey-deployments-field">
							<FormLabel>Deployments (Optional)</FormLabel>
							<FormDescription>
								Map a request model name to the provider&apos;s identifier (deployment name, inference profile ID, etc.). Expand a row for
								canonical name, model family, and provider overrides - these drive cost logs and family-based routing.
								{isReplicate && (
									<>
										{" "}
										Replicate deployments are listed only while &quot;Use Deployments Endpoint&quot; is on - otherwise type the owner/name
										and press Enter.
									</>
								)}
							</FormDescription>
							<FormControl>
								<div data-testid="apikey-deployments-table">
									<DeploymentsTable
										providerName={providerName}
										value={field.value}
										onChange={(next) => {
											form.clearErrors("key.aliases");
											field.onChange(Object.keys(next).length > 0 ? next : {});
										}}
									/>
								</div>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			</>
			{supportsBatchAPI && !isBedrock && !isAzure && !isVertex && <BatchAPIFormField control={control} form={form} />}
			{isAzure && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={azureAuthType}
							onValueChange={(v) => {
								setAzureAuthType(v as "api_key" | "entra_id" | "default_credential");
								form.setValue("key.azure_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "entra_id" || v === "default_credential") {
									// Clear API key when switching away from API Key
									form.setValue("key.value", undefined, { shouldDirty: true });
								}
								if (v === "api_key" || v === "default_credential") {
									// Clear Entra ID fields when switching away from Entra ID
									form.setValue("key.azure_key_config.client_id", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.client_secret", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.tenant_id", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.scopes", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-azure-default-credential-tab" value="default_credential">
									Default Credential
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-api-key-tab" value="api_key">
									API Key
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-entra-id-tab" value="entra_id">
									Entra ID (Service Principal)
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{azureAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>
										API Key {isVertex ? "(Supported only for gemini and fine-tuned models)" : isVLLM ? "(Optional)" : ""}
									</FormLabel>
									<FormControl>
										<SecretVarInput placeholder="API Key or env.MY_KEY" type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{azureAuthType === "default_credential" && (
						<p className="text-muted-foreground text-sm">
							Uses DefaultAzureCredential - automatically detects managed identity on Azure VMs and containers, workload identity in AKS,
							environment variables, and Azure CLI. No credentials required.
						</p>
					)}

					<FormField
						control={control}
						name={`key.azure_key_config.endpoint`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Endpoint (Required)</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{azureAuthType === "entra_id" && (
						<>
							<FormField
								control={control}
								name={`key.azure_key_config.client_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Client ID (Required)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-client-id or env.AZURE_CLIENT_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.client_secret`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Client Secret (Required)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-client-secret or env.AZURE_CLIENT_SECRET" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.tenant_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Tenant ID (Required)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-tenant-id or env.AZURE_TENANT_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.scopes`}
								render={({ field }) => (
									<FormItem>
										<div className="flex items-center gap-2">
											<FormLabel>Scopes (Optional)</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>
															Optional OAuth scopes for token requests. By default we use https://cognitiveservices.azure.com/.default - add
															additional scopes here if your setup requires extra permissions.
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<TagInput
												data-testid="apikey-azure-scopes-input"
												placeholder="Add scope (Enter or comma)"
												value={field.value ?? []}
												onValueChange={field.onChange}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
			{isVertex && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={vertexAuthType}
							onValueChange={(v) => {
								setVertexAuthType(v as "service_account" | "service_account_json" | "api_key");
								form.setValue("key.vertex_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "service_account" || v === "api_key") {
									// Clear auth credentials when switching away from service account JSON
									form.setValue("key.vertex_key_config.auth_credentials", undefined, { shouldDirty: true });
								}
								if (v === "service_account" || v === "service_account_json") {
									// Clear API key when switching away from API Key
									form.setValue("key.value", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-vertex-service-account-tab" value="service_account">
									Service Account (Attached)
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-service-account-json-tab" value="service_account_json">
									Service Account (JSON)
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-api-key-tab" value="api_key">
									API Key
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{vertexAuthType === "service_account" && (
							<p className="text-muted-foreground text-sm">
								Uses the service account attached to your environment (GCE, GKE, Cloud Run). No credentials required.
							</p>
						)}
					</div>

					<FormField
						control={control}
						name={`key.vertex_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Project ID (Required)</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="your-gcp-project-id or env.VERTEX_PROJECT_ID" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.project_number`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Project Number (Required only for fine-tuned models)</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="your-gcp-project-number or env.VERTEX_PROJECT_NUMBER" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Region (Required)</FormLabel>
								<FormDescription>
									Multi-region-only models are automatically routed to Google&apos;s matching multi-region endpoint. Turn on{" "}
									<span className="font-medium">Force single region</span> below to always use exactly this region.
								</FormDescription>
								<FormControl>
									<SecretVarInput placeholder="us-central1 or env.VERTEX_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					{vertexAuthType === "service_account_json" && (
						<FormField
							control={control}
							name={`key.vertex_key_config.auth_credentials`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>Auth Credentials (Required)</FormLabel>
									<FormDescription>Service account JSON object or env.VAR_NAME</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-vertex-auth-credentials-input"
											variant="textarea"
											rows={4}
											placeholder='{"type":"service_account","project_id":"your-gcp-project",...} or env.VERTEX_CREDENTIALS'
											inputClassName="font-mono text-sm"
											{...field}
										/>
									</FormControl>
									{isRedacted(field.value?.value ?? "") && (
										<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
											<Info className="h-3 w-3" />
											<span>Credentials are stored securely. Edit to update.</span>
										</div>
									)}
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					{vertexAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>API Key (Supported only for gemini and fine-tuned models)</FormLabel>
									<FormControl>
										<SecretVarInput data-testid="apikey-vertex-api-key-input" placeholder="API Key or env.MY_KEY" type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					<FormField
						control={control}
						name="key.vertex_key_config.force_single_region"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>Force single region</FormLabel>
									<FormDescription>
										Always call the region set above and skip automatic promotion of multi-region-only models to a multi-region endpoint.
										Enable when serving these models from a single region via provisioned throughput.
									</FormDescription>
								</div>
								<FormControl>
									<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
			{isReplicate && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name="key.replicate_key_config.use_deployments_endpoint"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>Use Deployments Endpoint</FormLabel>
									<FormDescription>
										Sends <strong>every</strong> model on this key to /v1/deployments/&#123;owner&#125;/&#123;name&#125;/predictions, so
										plain model names stop working. To switch just one model, leave this off and set &quot;Use deployments endpoint&quot; on
										its row above.
									</FormDescription>
									<FormMessage />
								</div>
								<FormControl>
									<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isVLLM && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name="key.vllm_key_config.url"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Server URL (Required)</FormLabel>
								<FormDescription>Base URL of the vLLM server (e.g. http://vllm-server:8000 or env.VLLM_URL)</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-vllm-url" placeholder="http://vllm-server:8000" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.vllm_key_config.model_name"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Model Name (Required)</FormLabel>
								<FormDescription>Exact model name served on this vLLM instance</FormDescription>
								<FormControl>
									<Input data-testid="key-input-vllm-model-name" placeholder="meta-llama/Llama-3-70b-hf" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isDatabricks && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.databricks_key_config.workspace_url"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Workspace URL (Required)</FormLabel>
								<FormDescription>
									Your Databricks workspace URL (e.g. https://dbc-1234abcd-5678.cloud.databricks.com or env.DATABRICKS_WORKSPACE_URL)
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-databricks-workspace-url"
										placeholder="https://dbc-1234abcd-5678.cloud.databricks.com"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.databricks_key_config.api_format"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Inference Surface</FormLabel>
								<FormDescription>
									Auto picks by model name: a dotted name such as system.ai.claude-sonnet-4-5 goes to the Unity AI Gateway, anything else to
									Model Serving. Choose explicitly to pin one surface.
								</FormDescription>
								<Select value={field.value ?? "auto"} onValueChange={field.onChange}>
									<FormControl>
										<SelectTrigger data-testid="key-select-databricks-api-format">
											<SelectValue placeholder="Auto" />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value="auto">Auto (by model name)</SelectItem>
										<SelectItem value="model_serving">Model Serving (/serving-endpoints)</SelectItem>
										<SelectItem value="ai_gateway">Unity AI Gateway (/ai-gateway/mlflow/v1)</SelectItem>
									</SelectContent>
								</Select>
								<FormMessage />
							</FormItem>
						)}
					/>
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={databricksAuthType}
							onValueChange={(v) => {
								setDatabricksAuthType(v as "pat" | "oauth_m2m");
								form.setValue("key.databricks_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "oauth_m2m") {
									// The token and the service principal are alternatives, never both.
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else {
									form.setValue("key.databricks_key_config.client_id", undefined, { shouldDirty: true });
									form.setValue("key.databricks_key_config.client_secret", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger data-testid="apikey-databricks-pat-tab" value="pat">
									Personal Access Token
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-databricks-oauth-tab" value="oauth_m2m">
									OAuth M2M (Service Principal)
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{databricksAuthType === "pat" && (
						<FormField
							control={control}
							name="key.value"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Personal Access Token</FormLabel>
									<FormDescription>Generate one from Settings &gt; Developer &gt; Access tokens in your workspace.</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="key-input-databricks-pat"
											placeholder="dapi... or env.DATABRICKS_TOKEN"
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{databricksAuthType === "oauth_m2m" && (
						<>
							<p className="text-muted-foreground text-sm">
								Databricks recommends OAuth machine-to-machine for production. Tokens are minted from the workspace OIDC endpoint and
								refreshed automatically.
							</p>
							<FormField
								control={control}
								name="key.databricks_key_config.client_id"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Client ID</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-databricks-client-id"
												placeholder="Service principal client ID or env.DATABRICKS_CLIENT_ID"
												type="text"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name="key.databricks_key_config.client_secret"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Client Secret</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-databricks-client-secret"
												placeholder="Service principal secret or env.DATABRICKS_CLIENT_SECRET"
												type="text"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<FormField
						control={control}
						name="key.databricks_key_config.forward_gateway_tags"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="databricks-forward-gateway-tags-switch">Forward Governance Tags</FormLabel>
									<FormDescription>
										Sends the virtual key, team and customer names as Databricks-Ai-Gateway-Request-Tags, so Databricks usage tracking
										attributes spend the same way Bifrost does. Names only, never user identifiers.
									</FormDescription>
								</div>
								<FormControl>
									<Switch id="databricks-forward-gateway-tags-switch" checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isKeylessProvider && (
				<div className="space-y-4">
					<FormField
						control={control}
						name={`key.${isOllama ? "ollama_key_config" : "sgl_key_config"}.url`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Server URL (Required)</FormLabel>
								<FormDescription>
									Base URL of the {isOllama ? "Ollama" : "SGLang"} server (e.g.{" "}
									{isOllama ? "http://localhost:11434" : "http://localhost:30000"} or {isOllama ? "env.OLLAMA_URL" : "env.SGL_URL"})
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid={`key-input-${isOllama ? "ollama" : "sgl"}-url`}
										placeholder={isOllama ? "http://localhost:11434" : "http://localhost:30000"}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isGithubCopilot && (
				<div className="space-y-4">
					<Separator />
					<div className="bg-muted/50 flex items-start gap-2 rounded-md border p-3">
						<Info className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0" />
						<p className="text-muted-foreground text-sm">
							Copilot accepts either credential. Fill in <strong>one</strong> of the two. <strong>GitHub App</strong> is the option for a
							shared gateway: usage bills to the organization that owns the installation and no individual Copilot seat is used. A{" "}
							<strong>Copilot API token</strong> in the field above is simpler but expires after about 30 minutes, so it suits testing
							rather than a running gateway.{" "}
							<a
								href="https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/server-to-server-tokens"
								target="_blank"
								rel="noopener noreferrer"
								className="text-primary hover:underline"
								data-testid="copilot-docs-link-server-to-server"
							>
								Set up a GitHub App for Copilot
							</a>
							{" or "}
							<a
								href="https://docs.github.com/en/copilot/how-tos/copilot-sdk/authenticate-copilot-sdk/authenticate-copilot-sdk"
								target="_blank"
								rel="noopener noreferrer"
								className="text-primary hover:underline"
								data-testid="copilot-docs-link-api-token"
							>
								get a Copilot API token
							</a>
							.
						</p>
					</div>
					<div className="space-y-1.5">
						{/* Label, not FormLabel: this heads a section rather than labelling one
						    control, so there is no FormItem id for htmlFor to point at. */}
						<Label>GitHub App Credentials</Label>
						<p className="text-muted-foreground text-sm">
							Leave these blank if you supplied a Copilot API token above. Otherwise all four are needed together. The App needs the Copilot
							Requests permission at Read &amp; write, installed on the organization that should be billed with All repositories access, and
							that organization must allow Copilot requests from GitHub App installations.
						</p>
					</div>
					<FormField
						control={control}
						name="key.github_copilot_key_config.app_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>App ID {copilotAppSuffix}</FormLabel>
								<FormDescription>
									The GitHub App&apos;s App ID or Client ID, from its settings page.{" "}
									<a
										href="https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app"
										target="_blank"
										rel="noopener noreferrer"
										className="text-primary hover:underline"
										data-testid="copilot-docs-link-create-app"
									>
										Create a GitHub App
									</a>
								</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-copilot-app-id" placeholder="123456 or env.COPILOT_APP_ID" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.installation_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Installation ID {copilotAppSuffix}</FormLabel>
								<FormDescription>
									The installation on the organization that should be billed.{" "}
									<a
										href="https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app"
										target="_blank"
										rel="noopener noreferrer"
										className="text-primary hover:underline"
										data-testid="copilot-docs-link-installation-id"
									>
										Find your installation ID
									</a>
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-installation-id"
										placeholder="87654321 or env.COPILOT_INSTALLATION_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.repository_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Repository ID {copilotAppSuffix}</FormLabel>
								<FormDescription>
									Any repository the installation can access. Copilot&apos;s permission check requires one in the token request even though
									the installation itself needs All repositories access, so this is not really a scoping choice.
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-repository-id"
										placeholder="999000111 or env.COPILOT_REPOSITORY_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.private_key"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Private Key {copilotAppSuffix}</FormLabel>
								<FormDescription>
									The App&apos;s private key in PEM form, as downloaded from GitHub. PKCS#1 and PKCS#8 both work.
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-private-key"
										variant="textarea"
										rows={4}
										placeholder="-----BEGIN RSA PRIVATE KEY----- or env.COPILOT_PRIVATE_KEY"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.github_domain"
						render={({ field }) => (
							<FormItem>
								<FormLabel>GitHub Enterprise Domain (Optional)</FormLabel>
								<FormDescription>Leave blank for github.com</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-copilot-github-domain" placeholder="acme.ghe.com" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isCline && (
				<div className="space-y-4">
					<Separator />
					<div className="bg-muted/20 space-y-3 rounded-md border p-4">
						<div className="flex items-center justify-between gap-2">
							<div>
								<div className="text-sm font-semibold">Connect with Cline (OAuth)</div>
								<p className="text-muted-foreground text-xs">
									Approve in your browser; Bifrost fills the refresh token in automatically. No manual key needed.
								</p>
							</div>
							<Button
								type="button"
								variant={clineChallenge || hasClineOAuthRefresh(form.watch("key.cline_key_config.refresh_token")) ? "outline" : "default"}
								size="sm"
								data-testid="cline-oauth-connect-btn"
								onClick={handleClineConnect}
								disabled={isStartingClineFlow || clinePolling}
							>
								{isStartingClineFlow || clinePolling ? (
									<Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
								) : (
									<RefreshCw className="mr-2 h-3.5 w-3.5" />
								)}
								{clinePolling
									? "Waiting for approval…"
									: hasClineOAuthRefresh(form.watch("key.cline_key_config.refresh_token"))
										? "Reconnect"
										: "Authenticate"}
							</Button>
						</div>
						{clineError && <p className="text-destructive text-xs">{clineError}</p>}
						{clineChallenge && (
							<div className="space-y-2 rounded-md border p-3">
								<div className="flex items-center justify-between gap-2">
									<span className="text-xs font-medium">
										User code:{" "}
										<span data-testid="cline-oauth-user-code" className="font-mono text-sm">
											{clineChallenge.user_code}
										</span>
									</span>
									<span className="text-muted-foreground text-xs">expires in {clineExpiresLeft}s</span>
								</div>
								<div className="flex flex-wrap gap-2">
									<Button type="button" variant="outline" size="sm" className="text-xs" asChild>
										<a
											href={clineChallenge.verification_uri_complete}
											target="_blank"
											rel="noopener noreferrer"
											data-testid="cline-oauth-open-tab-btn"
										>
											Open approval tab
										</a>
									</Button>
									<Button
										type="button"
										variant="outline"
										size="sm"
										className="text-xs"
										data-testid="cline-oauth-copy-url-btn"
										onClick={() => void handleClineCopy("url")}
									>
										{clineCopied === "url" ? <Check className="mr-2 h-3.5 w-3.5 text-green-600" /> : <Copy className="mr-2 h-3.5 w-3.5" />}
										{clineCopied === "url" ? "Copied!" : "Copy URL"}
									</Button>
									<Button
										type="button"
										variant="outline"
										size="sm"
										className="text-xs"
										data-testid="cline-oauth-copy-code-btn"
										onClick={() => void handleClineCopy("code")}
									>
										{clineCopied === "code" ? <Check className="mr-2 h-3.5 w-3.5 text-green-600" /> : <Copy className="mr-2 h-3.5 w-3.5" />}
										{clineCopied === "code" ? "Copied!" : "Copy code"}
									</Button>
								</div>
								{clinePolling && <p className="text-muted-foreground text-xs">Checking approval every second…</p>}
							</div>
						)}
					</div>
					<div className="bg-muted/50 flex items-start gap-2 rounded-md border p-3">
						<Info className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0" />
						<p className="text-muted-foreground text-sm">
							Cline accepts either credential. Fill in <strong>one</strong> of the two. <strong>OAuth</strong> is the option that needs no
							manual key: complete the WorkOS device flow once and paste the refresh token here — Bifrost exchanges it for short-lived
							access tokens automatically. A <strong>static API key</strong> in the field above is simpler for testing.
						</p>
					</div>
					<div className="space-y-1.5">
						<Label>OAuth Credentials</Label>
						<p className="text-muted-foreground text-sm">
							Leave these blank if you supplied a Cline API key above. Otherwise the refresh token is needed; the client ID defaults to the
							built-in Cline client.
						</p>
					</div>
					<FormField
						control={control}
						name="key.cline_key_config.refresh_token"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Refresh Token {clineOAuthSuffix}</FormLabel>
								<FormDescription>The OAuth refresh token from the WorkOS device flow.</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-cline-refresh-token"
										placeholder="paste-refresh-token or env.CLINE_REFRESH_TOKEN"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.cline_key_config.client_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Client ID (Optional)</FormLabel>
								<FormDescription>Leave blank for the built-in Cline client.</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-cline-client-id" placeholder="client_01K3A541FN8TA3EPPHTD2325AR" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{(isSGL || isDeepseek || isFireworks || isVLLM) && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.use_anthropic_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="use-anthropic-endpoints-alias-override-switch">Use Anthropic Endpoints</FormLabel>
									<FormDescription>Routes chat completions and responses requests through Anthropic-compatible endpoints.</FormDescription>
								</div>
								<FormControl>
									<Switch
										id="use-anthropic-endpoints-alias-override-switch"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isBedrock && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.use_openai_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="use-openai-endpoints-switch">Use OpenAI Endpoints</FormLabel>
									<FormDescription>Routes requests through Bedrock&apos;s OpenAI-compatible endpoints instead of Converse.</FormDescription>
								</div>
								<FormControl>
									<Switch
										id="use-openai-endpoints-switch"
										data-testid="key-switch-bedrock-use-openai-endpoints"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={bedrockAuthType}
							onValueChange={(v) => {
								setBedrockAuthType(v as "iam_role" | "explicit" | "api_key");
								form.setValue("key.bedrock_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "iam_role") {
									// Clear explicit credentials and API key when switching to IAM Role
									form.setValue("key.bedrock_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "explicit") {
									// Clear API key when switching to Explicit Credentials
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "api_key") {
									// Clear AWS credentials and assume-role fields when switching to API Key
									form.setValue("key.bedrock_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.role_arn", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.external_id", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_name", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-bedrock-iam-role-tab" value="iam_role">
									IAM Role (Inherited)
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-explicit-credentials-tab" value="explicit">
									Explicit Credentials
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-api-key-tab" value="api_key">
									API Key
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">Uses IAM roles attached to your environment (EC2, Lambda, ECS, EKS).</p>
						)}
						{bedrockAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">Uses a Bearer token for API key authentication.</p>
						)}
					</div>

					{bedrockAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Access Key (Required)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.secret_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Secret Key (Required)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.session_token`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Session Token (Optional)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{bedrockAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>API Key</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-api-key-input"
											placeholder="API Key or env.BEDROCK_API_KEY"
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					<FormField
						control={control}
						name={`key.bedrock_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Region (Required)</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="us-east-1 or env.AWS_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Mantle Project ID (Optional)</FormLabel>
								<FormDescription>
									Scopes Bedrock Mantle-routed models (OpenAI-family / Gemma) to a specific project via the OpenAI-Project header. Leave
									empty to use the account&apos;s default project.
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="apikey-bedrock-project-id-input"
										placeholder="proj_xxxxxxxx or env.BEDROCK_PROJECT_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{bedrockAuthType !== "api_key" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Assume Role ARN (Optional)</FormLabel>
										<FormDescription>
											Assume an IAM role before requests. Works with both explicit credentials and inherited IAM (EC2, ECS, EKS).
										</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-role-arn-input"
												placeholder="arn:aws:iam::123456789:role/MyRole or env.AWS_ROLE_ARN"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.external_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>External ID (Optional)</FormLabel>
										<FormDescription>Required by the role's trust policy when using cross-account access</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-external-id-input"
												placeholder="external-id or env.AWS_EXTERNAL_ID"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.session_name`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Session Name (Optional)</FormLabel>
										<FormDescription>AssumeRole session name (defaults to bifrost-session)</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-session-name-input"
												placeholder="bifrost-session or env.AWS_SESSION_NAME"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<FormField
						control={control}
						name={`key.bedrock_key_config.arn`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>ARN (Optional)</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="arn:aws:bedrock:us-east-1:123:inference-profile or env.AWS_ARN" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{supportsBatchAPI && (
						<FormField
							control={control}
							name={`key.bedrock_key_config.batch_role_arn`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>Batch Role ARN (Optional)</FormLabel>
									<FormDescription>
										Service role Bedrock assumes for batch S3 access. When set, it takes priority over the role_arn sent in requests.
									</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-batch-role-arn-input"
											placeholder="arn:aws:iam::123456789:role/BatchRole or env.AWS_BATCH_ROLE_ARN"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
					<VPCEndpointsFormField control={control} configKey="key.bedrock_key_config" services={BEDROCK_VPC_ENDPOINT_SERVICES} />
				</div>
			)}

			{isBedrockMantle && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={bedrockMantleAuthType}
							onValueChange={(v) => {
								setBedrockMantleAuthType(v as "iam_role" | "explicit" | "api_key");
								form.setValue("key.bedrock_mantle_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "iam_role") {
									// Clear explicit credentials and API key when switching to IAM Role
									form.setValue("key.bedrock_mantle_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "explicit") {
									// Clear API key when switching to Explicit Credentials
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "api_key") {
									// Clear AWS credentials and assume-role fields when switching to API Key
									form.setValue("key.bedrock_mantle_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.role_arn", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.external_id", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_name", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-bedrock-mantle-iam-role-tab" value="iam_role">
									IAM Role (Inherited)
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-mantle-explicit-credentials-tab" value="explicit">
									Explicit Credentials
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-mantle-api-key-tab" value="api_key">
									API Key
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockMantleAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">Uses IAM roles attached to your environment (EC2, Lambda, ECS, EKS).</p>
						)}
						{bedrockMantleAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">Uses a Bedrock Mantle API key sent as a Bearer token.</p>
						)}
					</div>

					{bedrockMantleAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Access Key (Required)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.secret_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Secret Key (Required)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.session_token`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Session Token (Optional)</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{bedrockMantleAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>API Key</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-mantle-api-key-input"
											placeholder="API Key or env.BEDROCK_MANTLE_API_KEY"
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					<FormField
						control={control}
						name={`key.bedrock_mantle_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Region (Required)</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="us-east-1 or env.AWS_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					<FormField
						control={control}
						name={`key.bedrock_mantle_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Project ID (Optional)</FormLabel>
								<FormDescription>
									Scopes inference and model listing to a specific Bedrock project (sent as the OpenAI-Project / anthropic-workspace-id
									header). Leave empty to use the account&apos;s default project.
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="apikey-bedrock-mantle-project-id-input"
										placeholder="proj_xxxxxxxx or env.BEDROCK_PROJECT_ID"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					{bedrockMantleAuthType !== "api_key" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Assume Role ARN (Optional)</FormLabel>
										<FormDescription>
											Assume an IAM role before requests. Works with both explicit credentials and inherited IAM (EC2, ECS, EKS).
										</FormDescription>
										<FormControl>
											<SecretVarInput placeholder="arn:aws:iam::123456789:role/MyRole or env.AWS_ROLE_ARN" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.external_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>External ID (Optional)</FormLabel>
										<FormDescription>Required by the role&apos;s trust policy when using cross-account access.</FormDescription>
										<FormControl>
											<SecretVarInput placeholder="external-id or env.AWS_EXTERNAL_ID" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.session_name`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Session Name (Optional)</FormLabel>
										<FormDescription>AssumeRole session name (defaults to bifrost-session).</FormDescription>
										<FormControl>
											<SecretVarInput placeholder="bifrost-session or env.AWS_SESSION_NAME" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<VPCEndpointsFormField
						control={control}
						configKey="key.bedrock_mantle_key_config"
						services={BEDROCK_VPC_ENDPOINT_SERVICES.filter((s) => s.name === "mantle")}
					/>
				</div>
			)}
			{isAntigravity && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={antigravityAuthType}
							onValueChange={(v) => {
								const t = v as "oauth" | "manual";
								setAntigravityAuthType(t);
								form.setValue("key.antigravity_key_config._auth_type", t, { shouldDirty: true });
							}}
						>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger data-testid="apikey-antigravity-oauth-tab" value="oauth">
									Google OAuth (Recommended)
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-antigravity-manual-tab" value="manual">
									Manual Key / JSON
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>

					{antigravityAuthType === "oauth" ? (
						<div className="bg-muted/20 space-y-4 rounded-md border p-4">
							{Boolean(
								hasAntigravityOAuthRefresh(form.watch("key.antigravity_key_config.refresh_token")) || form.watch("key.value")?.value,
							) ? (
								<div className="flex flex-col gap-3">
									<div className="flex items-center gap-3 rounded-md border border-green-200 bg-green-50 p-3 text-green-700 dark:border-green-800/40 dark:bg-green-950/40 dark:text-green-400">
										<CheckCircle2 className="h-5 w-5 shrink-0" />
										<div className="text-sm">
											<div className="font-semibold">Google Account Connected</div>
											<div className="mt-0.5 text-xs opacity-90">
												{form.watch("key.antigravity_key_config.project_id")?.value ? (
													<span>
														Google Cloud Project ID:{" "}
														<span className="font-mono font-medium">{form.watch("key.antigravity_key_config.project_id")?.value}</span>
													</span>
												) : (
													"OAuth Refresh Token loaded and auto-refreshed"
												)}
											</div>
										</div>
									</div>
									<div className="flex items-center gap-2">
										<Button
											type="button"
											variant="outline"
											size="sm"
											className="text-xs"
											onClick={() => {
												setAntigravityAuthStarted(false);
												void handleGoogleLogin();
											}}
											disabled={isFetchingUrl || isExchanging || isCopyingUrl}
										>
											{isFetchingUrl || isExchanging || isCopyingUrl ? (
												<Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
											) : (
												<RefreshCw className="mr-2 h-3.5 w-3.5" />
											)}
											Reconnect / Switch Google Account
										</Button>
										<TooltipProvider>
											<Tooltip>
												<TooltipTrigger asChild>
													<Button
														type="button"
														variant="outline"
														size="sm"
														className="text-xs"
														onClick={handleCopyGoogleLoginUrl}
														disabled={isFetchingUrl || isExchanging || isCopyingUrl}
													>
														{isCopyingUrl ? (
															<Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
														) : hasCopiedLink ? (
															<Check className="mr-2 h-3.5 w-3.5 text-green-600 dark:text-green-400" />
														) : (
															<Copy className="mr-2 h-3.5 w-3.5" />
														)}
														{hasCopiedLink ? "Copied!" : "Copy Link"}
													</Button>
												</TooltipTrigger>
												<TooltipContent className="max-w-xs">
													<p>Copy a fresh Google OAuth URL (redirecting to localhost:8085) to open in your local browser.</p>
												</TooltipContent>
											</Tooltip>
										</TooltipProvider>
									</div>
								</div>
							) : !antigravityAuthStarted ? (
								<div className="space-y-3">
									<p className="text-muted-foreground text-sm">
										Sign in with your Google account. Bifrost will automatically retrieve the OAuth tokens and discover your Cloud Code
										project.
									</p>
									<Button
										type="button"
										variant="default"
										data-testid="antigravity-oauth-login-btn"
										className="flex w-full items-center justify-center gap-2"
										onClick={() => void handleGoogleLogin()}
										disabled={isFetchingUrl || isExchanging || isCopyingUrl}
									>
										{isFetchingUrl || isCopyingUrl ? (
											<Loader2 className="h-4 w-4 animate-spin" />
										) : (
											<svg className="h-4 w-4" viewBox="0 0 24 24">
												<path
													fill="#EA4335"
													d="M12 5c1.6 0 3 .6 4.1 1.7l3.1-3.1C17.3 1.8 14.8 1 12 1 7.5 1 3.7 3.6 1.9 7.3l3.7 2.9C6.5 7.4 9 5 12 5z"
												/>
												<path
													fill="#4285F4"
													d="M23.5 12.3c0-.8-.1-1.7-.2-2.3H12v4.6h6.5c-.3 1.5-1.1 2.8-2.4 3.7l3.7 2.9c2.2-2 3.7-5 3.7-8.9z"
												/>
												<path
													fill="#FBBC05"
													d="M5.6 14.8c-.2-.7-.4-1.5-.4-2.3s.2-1.6.4-2.3L1.9 7.3C.7 9.7 0 12.3 0 15s.7 5.3 1.9 7.7l3.7-2.9z"
												/>
												<path
													fill="#34A853"
													d="M12 23c3.2 0 6-1.1 8-3l-3.7-2.9c-1.1.7-2.5 1.2-4.3 1.2-3 0-5.5-2.4-6.4-5.2L1.9 16c1.8 3.7 5.6 7 10.1 7z"
												/>
											</svg>
										)}
										Authenticate
									</Button>
								</div>
							) : (
								<div className="space-y-3">
									<div className="flex items-center justify-between gap-2">
										<div>
											<div className="text-sm font-semibold">Connect with Google (OAuth)</div>
											<p className="text-muted-foreground text-xs">
												Approve in your browser, then paste the redirect URL below. No manual key needed.
											</p>
										</div>
										<Button type="button" variant="outline" size="sm" data-testid="antigravity-oauth-waiting-btn" disabled>
											<Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
											Waiting for authentication…
										</Button>
									</div>
									<div className="space-y-2 rounded-md border p-3">
										<div className="flex flex-wrap gap-2">
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span className="inline-flex">
															<Button
																type="button"
																variant="outline"
																size="sm"
																className="text-xs"
																data-testid="antigravity-oauth-open-popup-btn"
																disabled
															>
																Open popup (not supported)
															</Button>
														</span>
													</TooltipTrigger>
													<TooltipContent className="max-w-xs">
														<p>
															Popup sign-in is not supported: the shared Google OAuth client only accepts the loopback redirect{" "}
															<span className="font-mono">http://localhost:8085</span>, which the popup cannot capture. Use Copy link below.
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
											<Button
												type="button"
												variant="outline"
												size="sm"
												className="text-xs"
												data-testid="antigravity-oauth-copy-link-btn"
												onClick={() => void handleCopyGoogleLoginUrl()}
												disabled={isFetchingUrl || isExchanging || isCopyingUrl}
											>
												{isCopyingUrl ? (
													<Loader2 className="h-4 w-4 animate-spin" />
												) : hasCopiedLink ? (
													<Check className="h-4 w-4 text-green-600 dark:text-green-400" />
												) : (
													<Copy className="h-4 w-4" />
												)}
												{hasCopiedLink ? "Copied!" : "Copy link"}
											</Button>
										</div>
										<div className="flex gap-2">
											<Input
												placeholder="4/0A... or http://localhost:8085/?code=..."
												value={manualCode}
												onChange={(e) => {
													const val = e.target.value;
													if (val.includes("code=")) {
														try {
															const urlObj = new URL(val.startsWith("http") ? val : `http://localhost:8085/${val}`);
															const c = urlObj.searchParams.get("code");
															if (c) {
																setManualCode(c);
																const detectedRedirect = urlObj.origin + (urlObj.pathname === "/" ? "" : urlObj.pathname);
																if (detectedRedirect && detectedRedirect !== "http://localhost") {
																	setManualRedirectUri(detectedRedirect);
																} else if (urlObj.origin) {
																	setManualRedirectUri(urlObj.origin);
																}
																return;
															}
														} catch {}
													}
													setManualCode(val);
												}}
												className="text-xs"
											/>
											<Button
												type="button"
												variant="secondary"
												size="sm"
												onClick={() => completeExchange(manualCode, manualRedirectUri || ANTIGRAVITY_REDIRECT_URI)}
												disabled={!manualCode.trim() || isExchanging}
											>
												{isExchanging ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : "Exchange"}
											</Button>
										</div>
									</div>
								</div>
							)}

							{authError && <p className="text-destructive mt-2 text-xs">{authError}</p>}
						</div>
					) : (
						<div className="space-y-4">
							<FormField
								control={control}
								name={`key.value`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Refresh Token / JSON / API Key</FormLabel>
										<FormControl>
											<SecretVarInput placeholder="Google OAuth refresh token, access token, or JSON" type="text" {...field} />
										</FormControl>
										<FormDescription>
											Paste your Google OAuth refresh_token, ya29. access token, or credentials JSON object.
										</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
					)}

					<FormField
						control={control}
						name={`key.antigravity_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Google Cloud Project ID (Optional override)</FormLabel>
								<FormControl>
									<SecretVarInput placeholder="Auto-discovered or custom GCP project ID" maskNonEnvValue={false} type="text" {...field} />
								</FormControl>
								<FormDescription>Leave blank to use the project ID discovered automatically via Cloud Code assist.</FormDescription>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
		</div>
	);
}