import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useGetAdaptiveRoutingMetricsQuery } from "@/lib/store/apis";
import { Link } from "@tanstack/react-router";
import { Activity, Gauge, RefreshCw, Server, Settings, Shuffle, Zap } from "lucide-react";
import { useMemo, useState } from "react";

interface TargetMetricRow {
	target: string;
	provider: string;
	model: string;
	keyId?: string;
	ewmaLatencyMs: number;
	ttftMs: number;
	p90LatencyMs: number;
	successRate: number;
	rateLimitCount: number;
	dynamicWeight: number;
	status: "healthy" | "degraded" | "optimal";
}

export default function AdaptiveRoutingView() {
	const [searchQuery, setSearchQuery] = useState("");

	// Query live EWMA and dynamic routing telemetry with 3-second auto polling
	const { data, isFetching, refetch } = useGetAdaptiveRoutingMetricsQuery(undefined, {
		pollingInterval: 3000,
	});

	const targets: TargetMetricRow[] = useMemo(() => {
		if (!data?.metrics) return [];
		return data.metrics.map((m) => ({
			target: m.target,
			provider: m.provider,
			model: m.model,
			keyId: m.key_id,
			ewmaLatencyMs: m.ewma_latency_ms,
			ttftMs: m.ttft_ms,
			p90LatencyMs: m.p90_latency_ms,
			successRate: m.success_rate,
			rateLimitCount: m.rate_limit_count,
			dynamicWeight: m.dynamic_weight,
			status: m.status,
		}));
	}, [data]);

	const filteredTargets = targets.filter(
		(t) =>
			t.target.toLowerCase().includes(searchQuery.toLowerCase()) ||
			t.provider.toLowerCase().includes(searchQuery.toLowerCase()) ||
			t.model.toLowerCase().includes(searchQuery.toLowerCase()),
	);

	const summary = data?.summary;
	const avgLatency = summary && targets.length > 0 ? summary.avg_ewma_latency_ms.toFixed(1) : "0.0";
	const avgTTFT = summary && summary.avg_ttft_ms > 0 ? summary.avg_ttft_ms.toFixed(1) : "0.0";
	const total429s = summary ? summary.total_429s : 0;

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-col gap-6 pb-12">
			{/* Header with Settings Navigation */}
			<div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-center">
				<div>
					<div className="flex items-center gap-2">
						<Shuffle className="text-primary h-6 w-6" />
						<h1 className="text-2xl font-bold tracking-tight">Adaptive Routing Dashboard</h1>
						<Badge variant="outline" className="bg-primary/10 text-primary border-primary/20 text-xs">
							OSS Active
						</Badge>
					</div>
					<p className="text-muted-foreground mt-1 text-sm">
						Real-time metrics, dynamic weight distribution, and load optimization across LLM providers.
					</p>
				</div>
				<div className="flex items-center gap-2">
					<Button variant="outline" size="sm" asChild className="gap-2">
						<Link to="/workspace/adaptive-routing/settings">
							<Settings className="h-4 w-4" />
							<span>Settings</span>
						</Link>
					</Button>
				</div>
			</div>

			{/* Summary Stats Cards */}
			<div className="grid grid-cols-1 gap-4 md:grid-cols-4">
				<Card>
					<CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
						<CardTitle className="text-sm font-medium">Average EWMA Latency</CardTitle>
						<Gauge className="text-muted-foreground h-4 w-4" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{avgLatency} ms</div>
						<p className="text-muted-foreground mt-1 text-xs">Weighted exponential moving average</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
						<CardTitle className="text-sm font-medium">Avg TTFT (Streaming)</CardTitle>
						<Zap className="h-4 w-4 text-yellow-500" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{avgTTFT} ms</div>
						<p className="text-muted-foreground mt-1 text-xs">Time-to-first-token stream latency</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
						<CardTitle className="text-sm font-medium">Active Monitored Targets</CardTitle>
						<Server className="text-muted-foreground h-4 w-4" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{targets.length} Routes</div>
						<p className="text-muted-foreground mt-1 text-xs">Live monitored routes</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
						<CardTitle className="text-sm font-medium">Auto-Protected 429s</CardTitle>
						<Activity className="h-4 w-4 text-emerald-500" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{total429s} Spikes</div>
						<p className="text-muted-foreground mt-1 text-xs">Traffic automatically throttled away</p>
					</CardContent>
				</Card>
			</div>

			{/* Real-time Target Weight Distribution Table */}
			<Card>
				<CardHeader className="flex flex-col justify-between gap-4 pb-4 sm:flex-row sm:items-center">
					<div>
						<CardTitle className="text-base font-semibold">Real-Time Routing & Weight Distribution</CardTitle>
						<CardDescription className="text-xs">
							Dynamic weight values are recomputed every 3 seconds and swapped with zero hot-path locks.
						</CardDescription>
					</div>
					<div className="flex items-center gap-2">
						<Input
							placeholder="Search provider, model..."
							value={searchQuery}
							onChange={(e) => setSearchQuery(e.target.value)}
							className="h-8 w-[200px] text-xs"
						/>
						<Button variant="outline" size="sm" className="h-8 gap-1.5 text-xs" onClick={() => refetch()} disabled={isFetching}>
							<RefreshCw className={`h-3.5 w-3.5 ${isFetching ? "animate-spin" : ""}`} />
							Refresh
						</Button>
					</div>
				</CardHeader>
				<CardContent className="p-0">
					<Table>
						<TableHeader>
							<TableRow className="text-xs">
								<TableHead>Target Route</TableHead>
								<TableHead>Status</TableHead>
								<TableHead>EWMA Latency</TableHead>
								<TableHead>Stream TTFT</TableHead>
								<TableHead>P90 Latency</TableHead>
								<TableHead>Success Rate</TableHead>
								<TableHead className="text-right">Active Dynamic Weight</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody className="text-xs">
							{filteredTargets.length === 0 ? (
								<TableRow>
									<TableCell colSpan={7} className="text-muted-foreground py-8 text-center">
										No active adaptive routing telemetry recorded yet. Live requests will populate statistics automatically.
									</TableCell>
								</TableRow>
							) : (
								filteredTargets.map((row) => (
									<TableRow key={row.target}>
										<TableCell className="font-medium">
											<div className="flex flex-col">
												<span className="text-foreground font-semibold">{row.target}</span>
												{row.keyId && <span className="text-muted-foreground font-mono text-[10px]">Key: {row.keyId}</span>}
											</div>
										</TableCell>
										<TableCell>
											<Badge
												variant="outline"
												className={
													row.status === "optimal"
														? "border-emerald-200 bg-emerald-500/10 text-emerald-600"
														: row.status === "healthy"
															? "border-blue-200 bg-blue-500/10 text-blue-600"
															: "border-amber-200 bg-amber-500/10 text-amber-600"
												}
											>
												{row.status.toUpperCase()}
											</Badge>
										</TableCell>
										<TableCell>{row.ewmaLatencyMs.toFixed(1)} ms</TableCell>
										<TableCell>{row.ttftMs.toFixed(1)} ms</TableCell>
										<TableCell>{row.p90LatencyMs.toFixed(1)} ms</TableCell>
										<TableCell>
											<span className={row.successRate > 98 ? "font-medium text-emerald-600" : "font-medium text-amber-600"}>
												{row.successRate.toFixed(1)}%
											</span>
										</TableCell>
										<TableCell className="text-right">
											<div className="flex items-center justify-end gap-2">
												<div className="bg-muted h-2 w-16 overflow-hidden rounded-full">
													<div className="bg-primary h-full rounded-full" style={{ width: `${Math.round(row.dynamicWeight * 100)}%` }} />
												</div>
												<span className="text-foreground w-10 text-right font-bold">{(row.dynamicWeight * 100).toFixed(0)}%</span>
											</div>
										</TableCell>
									</TableRow>
								))
							)}
						</TableBody>
					</Table>
				</CardContent>
			</Card>
		</div>
	);
}