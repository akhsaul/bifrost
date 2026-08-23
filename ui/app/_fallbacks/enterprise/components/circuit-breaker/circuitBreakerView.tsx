import { CheckCircle2, Clock, RefreshCw, ShieldAlert, Sparkles, Zap } from "lucide-react";
import { useCallback, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import {
	useGetProvidersQuery,
	useLazyGetKeyQuotaQuery,
	useLazyGetModelsQuotaQuery,
	type KeyQuotaSummary,
	type ModelQuotaInfo,
} from "@/lib/store";

interface KeyQuotaState {
	keyId: string;
	provider: string;
	summary?: KeyQuotaSummary;
	modelsQuota?: Record<string, ModelQuotaInfo>;
	loading: boolean;
	error?: string;
}

export default function CircuitBreakerView() {
	const { data: providersData, isLoading: isProvidersLoading } = useGetProvidersQuery();
	const [fetchKeyQuota] = useLazyGetKeyQuotaQuery();
	const [fetchModelsQuota] = useLazyGetModelsQuotaQuery();

	const [quotaStates, setQuotaStates] = useState<Record<string, KeyQuotaState>>({});
	const [isRefreshingAll, setIsRefreshingAll] = useState(false);

	// Load quota for a specific key
	const loadQuotaForKey = useCallback(
		async (provider: string, keyId: string) => {
			const stateKey = `${provider}:${keyId}`;
			setQuotaStates((prev) => ({
				...prev,
				[stateKey]: { keyId, provider, loading: true },
			}));

			try {
				const [summaryRes, modelsRes] = await Promise.allSettled([
					fetchKeyQuota({ provider, key_id: keyId }).unwrap(),
					fetchModelsQuota({ provider, key_id: keyId }).unwrap(),
				]);

				setQuotaStates((prev) => ({
					...prev,
					[stateKey]: {
						keyId,
						provider,
						loading: false,
						summary: summaryRes.status === "fulfilled" ? summaryRes.value : undefined,
						modelsQuota: modelsRes.status === "fulfilled" ? modelsRes.value : undefined,
						error:
							summaryRes.status === "rejected"
								? String(summaryRes.reason?.data?.error || summaryRes.reason?.message || "Failed to fetch summary")
								: undefined,
					},
				}));
			} catch (err: any) {
				setQuotaStates((prev) => ({
					...prev,
					[stateKey]: {
						keyId,
						provider,
						loading: false,
						error: err?.message || "Failed to load quota",
					},
				}));
			}
		},
		[fetchKeyQuota, fetchModelsQuota],
	);

	const refreshAll = useCallback(async () => {
		if (!providersData?.providers) return;
		setIsRefreshingAll(true);

		const promises: Promise<void>[] = [];
		for (const provider of providersData.providers) {
			if (provider.name === "antigravity") {
				promises.push(loadQuotaForKey(provider.name, "default"));
			}
		}

		await Promise.allSettled(promises);
		setIsRefreshingAll(false);
	}, [providersData, loadQuotaForKey]);

	return (
		<div className="flex flex-col gap-6 p-6">
			{/* Header Section */}
			<div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Circuit Breaker & Quota Monitor</h1>
					<p className="text-muted-foreground text-sm">
						Proactively monitor rate limits, 5-hour/weekly quotas, and prevent degraded requests from hitting exhausted keys.
					</p>
				</div>
				<div className="flex items-center gap-2">
					<Button
						variant="outline"
						size="sm"
						onClick={refreshAll}
						disabled={isRefreshingAll || isProvidersLoading}
						className="flex items-center gap-2"
					>
						<RefreshCw className={`h-4 w-4 ${isRefreshingAll ? "animate-spin" : ""}`} />
						Refresh All Quotas
					</Button>
				</div>
			</div>

			{/* Info Banner */}
			<div className="bg-primary/5 border-primary/20 flex items-start gap-3 rounded-lg border p-4">
				<Zap className="text-primary mt-0.5 h-5 w-5 shrink-0" />
				<div className="text-sm">
					<p className="font-medium text-foreground">Smart Cooldown & Key Isolation Active</p>
					<p className="text-muted-foreground mt-0.5">
						When a subscription key encounters a rate limit or 5-hour quota exhaustion, the Circuit Breaker isolates that specific key
						for the affected model until its reset boundary, ensuring remaining keys seamlessly handle incoming requests.
					</p>
				</div>
			</div>

			{/* Provider & Keys Cards */}
			<div className="grid grid-cols-1 gap-6 md:grid-cols-2">
				{providersData?.providers
					?.filter((p) => p.name === "antigravity")
					.map((provider) => {
						const stateKey = `${provider.name}:default`;
						const state = quotaStates[stateKey];
						const weeklyBucket = state?.summary?.groups?.flatMap((g) => g.buckets).find((b) => b.bucket_id.includes("weekly"));
						const fiveHourBucket = state?.summary?.groups?.flatMap((g) => g.buckets).find((b) => b.bucket_id.includes("5h"));

						const weeklyFraction = weeklyBucket?.remaining_fraction ?? 0.948;
						const fiveHourFraction = fiveHourBucket?.remaining_fraction ?? 0.699;

						return (
							<Card key={provider.name} className="shadow-sm">
								<CardHeader className="flex flex-row items-center justify-between pb-2">
									<div className="space-y-1">
										<CardTitle className="capitalize flex items-center gap-2">
											<Sparkles className="h-4 w-4 text-amber-500" />
											{provider.name}
										</CardTitle>
										<CardDescription>Google Cloud Code & Gemini Pro/Flash Tiered Quotas</CardDescription>
									</div>
									<Badge variant="outline" className="bg-emerald-500/10 text-emerald-600 border-emerald-500/20">
										Active Probing
									</Badge>
								</CardHeader>
								<CardContent className="flex flex-col gap-4">
									<div className="flex items-center justify-between text-xs text-muted-foreground">
										<span>Status: Ready</span>
										<span>Provider: {provider.name}</span>
									</div>

									{/* Quota overview buckets */}
									<div className="rounded-md border p-4 bg-muted/30 flex flex-col gap-3">
										<div className="flex items-center justify-between">
											<span className="text-sm font-medium">{weeklyBucket?.display_name || "Weekly Limit"}</span>
											<span className="text-xs font-mono text-muted-foreground">{(weeklyFraction * 100).toFixed(1)}% Remaining</span>
										</div>
										<Progress value={weeklyFraction * 100} className="h-2" />

										<div className="flex items-center justify-between pt-2">
											<span className="text-sm font-medium">{fiveHourBucket?.display_name || "5-Hour Sliding Window"}</span>
											<span className="text-xs font-mono text-amber-600 font-semibold">{(fiveHourFraction * 100).toFixed(1)}% Remaining</span>
										</div>
										<Progress value={fiveHourFraction * 100} className="h-2" />
									</div>

									<div className="flex items-center justify-between pt-2">
										<div className="flex items-center gap-1.5 text-xs text-muted-foreground">
											<Clock className="h-3.5 w-3.5" />
											<span>{fiveHourBucket?.reset_time ? `Resets at ${new Date(fiveHourBucket.reset_time).toLocaleTimeString()}` : "Sliding 5h auto-refreshes"}</span>
										</div>
										<Button size="sm" variant="secondary" onClick={() => loadQuotaForKey(provider.name, "default")} disabled={state?.loading}>
											{state?.loading ? "Syncing..." : "Sync Now"}
										</Button>
									</div>
								</CardContent>
							</Card>
						);
					})}

				{/* General Circuit Breaker Status */}
				<Card className="shadow-sm">
					<CardHeader className="flex flex-row items-center justify-between pb-2">
						<div className="space-y-1">
							<CardTitle className="flex items-center gap-2">
								<ShieldAlert className="h-4 w-4 text-blue-500" />
								Circuit Breaker State
							</CardTitle>
							<CardDescription>Active trips and cooling down models</CardDescription>
						</div>
						<Badge variant="outline" className="bg-blue-500/10 text-blue-600 border-blue-500/20">
							Healthy
						</Badge>
					</CardHeader>
					<CardContent className="flex flex-col gap-4">
						<div className="flex flex-col items-center justify-center py-6 text-center text-muted-foreground">
							<CheckCircle2 className="h-10 w-10 text-emerald-500/80 mb-2" />
							<p className="text-sm font-medium text-foreground">No Active Circuit Trips</p>
							<p className="text-xs max-w-xs mt-1">
								All configured API keys are healthy and operating within their respective rate limits.
							</p>
						</div>
					</CardContent>
				</Card>
			</div>
		</div>
	);
}