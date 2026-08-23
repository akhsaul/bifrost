import { CheckCircle2, Clock, KeyRound, RefreshCw, ShieldAlert, Sparkles, Zap } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import {
	useGetProvidersQuery,
	useLazyGetKeyQuotaQuery,
	useLazyGetModelsQuotaQuery,
	useLazyGetProviderKeysQuery,
	type KeyQuotaSummary,
	type ModelQuotaInfo,
} from "@/lib/store";
import type { ModelProviderKey } from "@/lib/types/config";

interface KeyQuotaState {
	keyId: string;
	keyName: string;
	provider: string;
	summary?: KeyQuotaSummary;
	modelsQuota?: Record<string, ModelQuotaInfo>;
	loading: boolean;
	error?: string;
}

export default function CircuitBreakerView() {
	const { data: providersData, isLoading: isProvidersLoading } = useGetProvidersQuery();
	const [fetchProviderKeys] = useLazyGetProviderKeysQuery();
	const [fetchKeyQuota] = useLazyGetKeyQuotaQuery();
	const [fetchModelsQuota] = useLazyGetModelsQuotaQuery();

	const [providerKeys, setProviderKeys] = useState<Record<string, ModelProviderKey[]>>({});
	const [quotaStates, setQuotaStates] = useState<Record<string, KeyQuotaState>>({});
	const [isRefreshingAll, setIsRefreshingAll] = useState(false);

	// Load quota for a specific key
	const loadQuotaForKey = useCallback(
		async (provider: string, keyId: string, keyName: string) => {
			const stateKey = `${provider}:${keyId}`;
			setQuotaStates((prev) => ({
				...prev,
				[stateKey]: { keyId, keyName, provider, loading: true },
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
						keyName,
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
						keyName,
						provider,
						loading: false,
						error: err?.message || "Failed to load quota",
					},
				}));
			}
		},
		[fetchKeyQuota, fetchModelsQuota],
	);

	// Fetch keys for supported providers and load their quotas on mount / provider list change
	useEffect(() => {
		if (!providersData) return;

		const supportedProviders = providersData.filter((p) => p.name === "antigravity");
		for (const provider of supportedProviders) {
			fetchProviderKeys(provider.name)
				.unwrap()
				.then((keys) => {
					setProviderKeys((prev) => ({ ...prev, [provider.name]: keys }));
					for (const key of keys) {
						loadQuotaForKey(provider.name, key.id, key.name || key.id);
					}
				})
				.catch(() => {});
		}
	}, [providersData, fetchProviderKeys, loadQuotaForKey]);

	const refreshAll = useCallback(async () => {
		setIsRefreshingAll(true);
		const promises: Promise<void>[] = [];

		for (const [provider, keys] of Object.entries(providerKeys)) {
			for (const key of keys) {
				promises.push(loadQuotaForKey(provider, key.id, key.name || key.id));
			}
		}

		await Promise.allSettled(promises);
		setIsRefreshingAll(false);
	}, [providerKeys, loadQuotaForKey]);

	// Gather list of key cards to render
	const keyEntries = Object.values(quotaStates);

	return (
		<div className="flex flex-col gap-6 p-6">
			{/* Header Section */}
			<div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Circuit Breaker & Quota Monitor</h1>
					<p className="text-muted-foreground text-sm">
						Proactively monitor rate limits, 5-hour/weekly quotas per account key, and prevent degraded requests from hitting exhausted keys.
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
					<p className="font-medium text-foreground">Multi-Account & Per-Key Smart Cooldown Active</p>
					<p className="text-muted-foreground mt-0.5">
						Each API key / subscription account is isolated independently. When one account reaches its 5-hour or weekly quota, the Circuit Breaker trips only that account key, allowing the other healthy keys in the pool to continue serving traffic seamlessly.
					</p>
				</div>
			</div>

			{/* Provider & Keys Cards */}
			<div className="grid grid-cols-1 gap-6 md:grid-cols-2">
				{keyEntries.length > 0 ? (
					keyEntries.map((state) => {
						const weeklyBucket = state?.summary?.groups?.flatMap((g) => g.buckets).find((b) => b.bucket_id.includes("weekly"));
						const fiveHourBucket = state?.summary?.groups?.flatMap((g) => g.buckets).find((b) => b.bucket_id.includes("5h"));

						const weeklyFraction = weeklyBucket?.remaining_fraction ?? 1.0;
						const fiveHourFraction = fiveHourBucket?.remaining_fraction ?? 1.0;

						const isFiveHourLimited = fiveHourFraction <= 0;
						const isWeeklyLimited = weeklyFraction <= 0;
						const isTripped = isFiveHourLimited || isWeeklyLimited;

						return (
							<Card key={`${state.provider}:${state.keyId}`} className="shadow-sm">
								<CardHeader className="flex flex-row items-center justify-between pb-2">
									<div className="space-y-1">
										<CardTitle className="capitalize flex items-center gap-2">
											<Sparkles className="h-4 w-4 text-amber-500" />
											{state.keyName || state.keyId}
										</CardTitle>
										<CardDescription className="flex items-center gap-1.5 font-mono text-xs">
											<KeyRound className="h-3 w-3" />
											{state.keyId} ({state.provider})
										</CardDescription>
									</div>
									<Badge
										variant="outline"
										className={
											isTripped
												? "bg-rose-500/10 text-rose-600 border-rose-500/20"
												: "bg-emerald-500/10 text-emerald-600 border-emerald-500/20"
										}
									>
										{isTripped ? "Circuit Tripped (Cooldown)" : "Healthy"}
									</Badge>
								</CardHeader>
								<CardContent className="flex flex-col gap-4">
									{state.error ? (
										<div className="text-xs text-rose-500 bg-rose-500/10 p-3 rounded border border-rose-500/20">
											{state.error}
										</div>
									) : (
										<>
											{/* Quota overview buckets */}
											<div className="rounded-md border p-4 bg-muted/30 flex flex-col gap-3">
												<div className="flex items-center justify-between">
													<span className="text-sm font-medium">{weeklyBucket?.display_name || "Weekly Limit"}</span>
													<span className="text-xs font-mono text-muted-foreground">
														{(weeklyFraction * 100).toFixed(1)}% Remaining
													</span>
												</div>
												<Progress value={weeklyFraction * 100} className="h-2" />

												<div className="flex items-center justify-between pt-2">
													<span className="text-sm font-medium">{fiveHourBucket?.display_name || "5-Hour Sliding Window"}</span>
													<span
														className={`text-xs font-mono font-semibold ${
															fiveHourFraction < 0.2 ? "text-rose-600" : "text-amber-600"
														}`}
													>
														{(fiveHourFraction * 100).toFixed(1)}% Remaining
													</span>
												</div>
												<Progress value={fiveHourFraction * 100} className="h-2" />
											</div>

											<div className="flex items-center justify-between pt-2">
												<div className="flex items-center gap-1.5 text-xs text-muted-foreground">
													<Clock className="h-3.5 w-3.5" />
													<span>
														{fiveHourBucket?.reset_time
															? `Resets at ${new Date(fiveHourBucket.reset_time).toLocaleTimeString()}`
															: "Sliding 5h auto-refreshes"}
													</span>
												</div>
												<Button
													size="sm"
													variant="secondary"
													onClick={() => loadQuotaForKey(state.provider, state.keyId, state.keyName)}
													disabled={state.loading}
												>
													{state.loading ? "Syncing..." : "Sync Now"}
												</Button>
											</div>
										</>
									)}
								</CardContent>
							</Card>
						);
					})
				) : (
					<Card className="shadow-sm">
						<CardHeader className="pb-2">
							<CardTitle className="flex items-center gap-2">
								<Sparkles className="h-4 w-4 text-amber-500" />
								No Accounts Discovered
							</CardTitle>
							<CardDescription>Configure Antigravity API keys to view quota monitors</CardDescription>
						</CardHeader>
						<CardContent className="text-sm text-muted-foreground py-6 text-center">
							No API keys found for providers supporting proactive quota probing.
						</CardContent>
					</Card>
				)}

				{/* General Circuit Breaker Status */}
				<Card className="shadow-sm">
					<CardHeader className="flex flex-row items-center justify-between pb-2">
						<div className="space-y-1">
							<CardTitle className="flex items-center gap-2">
								<ShieldAlert className="h-4 w-4 text-blue-500" />
								Pool Health Overview
							</CardTitle>
							<CardDescription>Status of available key rotation pools</CardDescription>
						</div>
						<Badge variant="outline" className="bg-blue-500/10 text-blue-600 border-blue-500/20">
							{keyEntries.some((k) => (k.summary?.groups?.flatMap((g) => g.buckets) || []).some((b) => b.remaining_fraction <= 0))
								? "Partial Cooldown"
								: "All Ready"}
						</Badge>
					</CardHeader>
					<CardContent className="flex flex-col gap-4">
						<div className="flex flex-col items-center justify-center py-6 text-center text-muted-foreground">
							<CheckCircle2 className="h-10 w-10 text-emerald-500/80 mb-2" />
							<p className="text-sm font-medium text-foreground">
								{keyEntries.length} Account Key{keyEntries.length > 1 ? "s" : ""} Monitored
							</p>
							<p className="text-xs max-w-xs mt-1">
								Traffic automatically fails over away from rate-limited keys to active healthy accounts.
							</p>
						</div>
					</CardContent>
				</Card>
			</div>
		</div>
	);
}