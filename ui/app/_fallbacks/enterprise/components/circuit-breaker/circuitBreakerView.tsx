import { CheckCircle2, Clock, KeyRound, RefreshCw, ShieldAlert, Sparkles, Zap } from "lucide-react";
import { useCallback, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import {
	useGetKeyQuotaQuery,
	useGetProviderKeysQuery,
	useGetProvidersQuery,
	type ModelQuotaInfo,
} from "@/lib/store";
import type { ModelProviderKey } from "@/lib/types/config";

interface KeyQuotaCardProps {
	provider: string;
	keyItem: ModelProviderKey;
}

function KeyQuotaCard({ provider, keyItem }: KeyQuotaCardProps) {
	const {
		data: summary,
		isLoading,
		isFetching,
		error,
		refetch,
	} = useGetKeyQuotaQuery({ provider, key_id: keyItem.id });

	const weeklyBucket = summary?.groups?.flatMap((g) => g.buckets).find((b) => b.bucket_id.includes("weekly"));
	const fiveHourBucket = summary?.groups?.flatMap((g) => g.buckets).find((b) => b.bucket_id.includes("5h"));

	const weeklyFraction = weeklyBucket?.remaining_fraction ?? 1.0;
	const fiveHourFraction = fiveHourBucket?.remaining_fraction ?? 1.0;

	const isFiveHourLimited = fiveHourFraction <= 0;
	const isWeeklyLimited = weeklyFraction <= 0;
	const isTripped = isFiveHourLimited || isWeeklyLimited;

	const errorMessage = error ? (typeof error === "object" && "data" in error ? JSON.stringify((error as any).data) : String(error)) : null;

	const modelList = summary?.models
		? Object.values(summary.models).sort((a, b) => {
				const isLimitedA = a.is_limited || a.remaining_fraction <= 0;
				const isLimitedB = b.is_limited || b.remaining_fraction <= 0;
				// 1. Put limited/tripped models at the top
				if (isLimitedA !== isLimitedB) {
					return isLimitedA ? -1 : 1;
				}
				// 2. Put lowest remaining fraction next (ascending)
				const fracA = a.remaining_fraction ?? 1.0;
				const fracB = b.remaining_fraction ?? 1.0;
				if (Math.abs(fracA - fracB) > 0.001) {
					return fracA - fracB;
				}
				// 3. Alphabetical order by display name / model ID
				const nameA = (a.display_name || a.model || "").toLowerCase();
				const nameB = (b.display_name || b.model || "").toLowerCase();
				return nameA.localeCompare(nameB);
			})
		: [];
	const limitedModels = modelList.filter((m) => m.is_limited || m.remaining_fraction <= 0);

	const formatResetTime = (dateStr?: string) => {
		if (!dateStr) return null;
		try {
			const date = new Date(dateStr);
			if (isNaN(date.getTime())) return null;
			const browserLocale =
				(typeof navigator !== "undefined" && (navigator.languages?.[0] || navigator.language)) ||
				Intl.DateTimeFormat().resolvedOptions().locale ||
				undefined;

			const datePart = date.toLocaleDateString(browserLocale, {
				day: "numeric",
				month: "short",
				year: "2-digit"
			});

			const timePart = date.toLocaleTimeString('en', {
				second: "2-digit",
				minute: "2-digit",
				hour: "numeric",
				hour12: false
			});

			return `${datePart} ${timePart}`;
		} catch {
			return null;
		}
	};

	const weeklyResetFormatted = formatResetTime(weeklyBucket?.reset_time);
	const fiveHourResetFormatted = formatResetTime(fiveHourBucket?.reset_time);

	return (
		<Card className="shadow-sm">
			<CardHeader className="flex flex-row items-center justify-between pb-2">
				<div className="space-y-1">
					<CardTitle className="capitalize flex items-center gap-2">
						<Sparkles className="h-4 w-4 text-amber-500" />
						{keyItem.name || keyItem.id}
					</CardTitle>
					<CardDescription className="flex items-center gap-1.5 font-mono text-xs">
						<KeyRound className="h-3 w-3" />
						{keyItem.id} ({provider})
					</CardDescription>
				</div>
				<Badge
					variant="outline"
					className={
						isTripped || limitedModels.length > 0
							? "bg-rose-500/10 text-rose-600 border-rose-500/20"
							: "bg-emerald-500/10 text-emerald-600 border-emerald-500/20"
					}
				>
					{isTripped
						? "Key Rate Limited"
						: limitedModels.length > 0
							? `${limitedModels.length} Model${limitedModels.length > 1 ? "s" : ""} Limited`
							: "Healthy"}
				</Badge>
			</CardHeader>
			<CardContent className="flex flex-col gap-4">
				{errorMessage ? (
					<div className="text-xs text-rose-500 bg-rose-500/10 p-3 rounded border border-rose-500/20">
						{errorMessage}
					</div>
				) : (
					<>
						{/* Quota overview buckets */}
						<div className="rounded-md border p-4 bg-muted/30 flex flex-col gap-3">
							{/* Weekly Limit Bucket */}
							<div className="flex flex-col gap-1">
								<div className="flex items-center justify-between">
									<div className="flex items-center gap-1.5">
										<span className="text-sm font-medium">{weeklyBucket?.display_name || "Weekly Limit"}</span>
										{weeklyResetFormatted && (
											<span className="text-[11px] text-muted-foreground">
												(Resets at {weeklyResetFormatted})
											</span>
										)}
									</div>
									<span className="text-xs font-mono text-muted-foreground">
										{(weeklyFraction * 100).toFixed(1)}% Remaining
									</span>
								</div>
								<Progress value={weeklyFraction * 100} className="h-2" />
							</div>

							{/* Five Hour Window Bucket */}
							<div className="flex flex-col gap-1 pt-1">
								<div className="flex items-center justify-between">
									<div className="flex items-center gap-1.5">
										<span className="text-sm font-medium">{fiveHourBucket?.display_name || "5-Hour Sliding Window"}</span>
										{fiveHourResetFormatted && (
											<span className="text-[11px] text-muted-foreground">
												(Resets at {fiveHourResetFormatted})
											</span>
										)}
									</div>
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
						</div>

						{/* Affected Models / Model Quotas Section */}
						{modelList.length > 0 && (
							<div className="rounded-md border p-3 bg-muted/10 flex flex-col gap-2">
								<div className="flex items-center justify-between pb-1">
									<span className="text-xs font-semibold text-foreground uppercase tracking-wider">
										Models & Quota Allocation
									</span>
									<span className="text-[11px] text-muted-foreground">
										{modelList.length} models tracked
									</span>
								</div>

								<div className="flex flex-col gap-2 max-h-48 overflow-y-auto pr-1">
									{modelList.map((modelInfo) => {
										const modelDisplayName = modelInfo.display_name?.trim() || modelInfo.model?.trim() || "";
										if (!modelDisplayName) return null;

										const modelFraction = modelInfo.remaining_fraction ?? 1.0;
										const isModelLimited = modelInfo.is_limited || modelFraction <= 0;

										return (
											<div
												key={modelInfo.model}
												className="flex items-center justify-between text-xs py-1 px-2 rounded bg-background/60 border border-border/50"
											>
												<div className="flex flex-col truncate pr-2">
													<span className="font-medium truncate text-foreground">
														{modelDisplayName}
													</span>
													{modelInfo.display_name && modelInfo.model && modelInfo.display_name !== modelInfo.model && (
														<span className="text-[10px] font-mono text-muted-foreground truncate">
															{modelInfo.model}
														</span>
													)}
												</div>

												<div className="flex items-center gap-2 shrink-0">
													<span
														className={`font-mono text-[11px] font-semibold ${
															isModelLimited
																? "text-rose-600"
																: modelFraction < 0.3
																	? "text-amber-600"
																	: "text-emerald-600"
														}`}
													>
														{(modelFraction * 100).toFixed(0)}%
													</span>
													{isModelLimited && (
														<Badge
															variant="outline"
															className="bg-rose-500/10 text-rose-600 border-rose-500/20 text-[10px] py-0 px-1"
														>
															Limited
														</Badge>
													)}
												</div>
											</div>
										);
									})}
								</div>
							</div>
						)}

						<div className="flex items-center justify-between pt-2">
							<div className="flex items-center gap-1.5 text-xs text-muted-foreground">
								<Clock className="h-3.5 w-3.5" />
								<span>
									{fiveHourResetFormatted
										? `5h Window Resets at ${fiveHourResetFormatted}`
										: "Sliding 5h auto-refreshes"}
								</span>
							</div>
							<Button
								size="sm"
								variant="secondary"
								onClick={() => refetch()}
								disabled={isLoading || isFetching}
							>
								{isFetching ? "Syncing..." : "Sync Now"}
							</Button>
						</div>
					</>
				)}
			</CardContent>
		</Card>
	);
}

function ProviderKeysSection({ provider }: { provider: string }) {
	const { data: keys = [], isLoading } = useGetProviderKeysQuery(provider);
	const enabledKeys = keys.filter((k) => k.enabled ?? true);

	if (isLoading) {
		return (
			<Card className="shadow-sm">
				<CardHeader className="pb-2">
					<CardTitle className="text-sm">Loading keys for {provider}...</CardTitle>
				</CardHeader>
			</Card>
		);
	}

	if (enabledKeys.length === 0) {
		return (
			<Card className="shadow-sm">
				<CardHeader className="pb-2">
					<CardTitle className="flex items-center gap-2">
						<Sparkles className="h-4 w-4 text-amber-500" />
						No API Keys Configured
					</CardTitle>
					<CardDescription>
						{keys.length > 0
							? `All keys for ${provider} are currently disabled. Enable keys in the Providers page.`
							: `Configure keys for ${provider} in the Providers page.`}
					</CardDescription>
				</CardHeader>
			</Card>
		);
	}

	return (
		<>
			{enabledKeys.map((keyItem) => (
				<KeyQuotaCard key={keyItem.id} provider={provider} keyItem={keyItem} />
			))}
		</>
	);
}

export default function CircuitBreakerView() {
	const { data: providersData, isLoading: isProvidersLoading, refetch: refetchProviders } = useGetProvidersQuery();
	const supportedProviders = providersData?.filter((p) => p.name === "antigravity") || [];

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
						onClick={() => refetchProviders()}
						disabled={isProvidersLoading}
						className="flex items-center gap-2"
					>
						<RefreshCw className={`h-4 w-4 ${isProvidersLoading ? "animate-spin" : ""}`} />
						Refresh Providers
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
				{supportedProviders.length > 0 ? (
					supportedProviders.map((prov) => (
						<ProviderKeysSection key={prov.name} provider={prov.name} />
					))
				) : (
					<Card className="shadow-sm">
						<CardHeader className="pb-2">
							<CardTitle className="flex items-center gap-2">
								<Sparkles className="h-4 w-4 text-amber-500" />
								No Supported Providers
							</CardTitle>
							<CardDescription>Antigravity provider is not yet configured.</CardDescription>
						</CardHeader>
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
							All Ready
						</Badge>
					</CardHeader>
					<CardContent className="flex flex-col gap-4">
						<div className="flex flex-col items-center justify-center py-6 text-center text-muted-foreground">
							<CheckCircle2 className="h-10 w-10 text-emerald-500/80 mb-2" />
							<p className="text-sm font-medium text-foreground">
								Multi-Key Pool Isolation Active
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