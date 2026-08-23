import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { isDevelopmentMode } from "@/lib/utils/port";
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

const mockTargetsDev: TargetMetricRow[] = [
	{
		target: "openai/gpt-4o",
		provider: "openai",
		model: "gpt-4o",
		keyId: "prod-key-1",
		ewmaLatencyMs: 85.4,
		ttftMs: 32.1,
		p90LatencyMs: 120.0,
		successRate: 99.8,
		rateLimitCount: 0,
		dynamicWeight: 0.62,
		status: "optimal",
	},
	{
		target: "azure/gpt-4o",
		provider: "azure",
		model: "gpt-4o",
		keyId: "eastus-key",
		ewmaLatencyMs: 160.2,
		ttftMs: 78.4,
		p90LatencyMs: 240.5,
		successRate: 98.5,
		rateLimitCount: 2,
		dynamicWeight: 0.28,
		status: "healthy",
	},
	{
		target: "groq/llama-3.3-70b",
		provider: "groq",
		model: "llama-3.3-70b",
		keyId: "fast-tier",
		ewmaLatencyMs: 42.0,
		ttftMs: 15.2,
		p90LatencyMs: 65.0,
		successRate: 99.9,
		rateLimitCount: 0,
		dynamicWeight: 0.85,
		status: "optimal",
	},
	{
		target: "anthropic/claude-3-5-sonnet",
		provider: "anthropic",
		model: "claude-3-5-sonnet",
		keyId: "backup-key",
		ewmaLatencyMs: 420.0,
		ttftMs: 190.0,
		p90LatencyMs: 680.0,
		successRate: 92.1,
		rateLimitCount: 14,
		dynamicWeight: 0.05,
		status: "degraded",
	},
];

export default function AdaptiveRoutingView() {
	const [searchQuery, setSearchQuery] = useState("");
	const isDev = useMemo(() => isDevelopmentMode(), []);

	// Data mock/dummy only rendered in dev mode (make dev / MODE=dev)
	const targets = isDev ? mockTargetsDev : [];

	const filteredTargets = targets.filter((t) =>
		t.target.toLowerCase().includes(searchQuery.toLowerCase()) ||
		t.provider.toLowerCase().includes(searchQuery.toLowerCase()) ||
		t.model.toLowerCase().includes(searchQuery.toLowerCase())
	);

	const avgLatency = targets.length > 0 ? (targets.reduce((acc, t) => acc + t.ewmaLatencyMs, 0) / targets.length).toFixed(1) : "0.0";
	const avgTTFT = targets.length > 0 ? (targets.reduce((acc, t) => acc + t.ttftMs, 0) / targets.length).toFixed(1) : "0.0";
	const total429s = targets.reduce((acc, t) => acc + t.rateLimitCount, 0);

	return (
		<div className="flex flex-col gap-6 w-full max-w-7xl mx-auto pb-12">
			{/* Header with Settings Navigation */}
			<div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
				<div>
					<div className="flex items-center gap-2">
						<Shuffle className="h-6 w-6 text-primary" />
						<h1 className="text-2xl font-bold tracking-tight">Adaptive Routing Dashboard</h1>
						<Badge variant="outline" className="text-xs bg-primary/10 text-primary border-primary/20">
							OSS Active
						</Badge>
						{isDev && (
							<Badge variant="secondary" className="text-xs bg-amber-500/10 text-amber-600 border-amber-200">
								Dev Mode (Mock Data Active)
							</Badge>
						)}
					</div>
					<p className="text-muted-foreground text-sm mt-1">
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
			<div className="grid grid-cols-1 md:grid-cols-4 gap-4">
				<Card>
					<CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
						<CardTitle className="text-sm font-medium">Average EWMA Latency</CardTitle>
						<Gauge className="h-4 w-4 text-muted-foreground" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{avgLatency} ms</div>
						<p className="text-xs text-muted-foreground mt-1">Weighted exponential moving average</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
						<CardTitle className="text-sm font-medium">Avg TTFT (Streaming)</CardTitle>
						<Zap className="h-4 w-4 text-yellow-500" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{avgTTFT} ms</div>
						<p className="text-xs text-muted-foreground mt-1">Time-to-first-token stream latency</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
						<CardTitle className="text-sm font-medium">Active Monitored Targets</CardTitle>
						<Server className="h-4 w-4 text-muted-foreground" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{targets.length} Routes</div>
						<p className="text-xs text-muted-foreground mt-1">Live monitored routes</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
						<CardTitle className="text-sm font-medium">Auto-Protected 429s</CardTitle>
						<Activity className="h-4 w-4 text-emerald-500" />
					</CardHeader>
					<CardContent>
						<div className="text-2xl font-bold">{total429s} Spikes</div>
						<p className="text-xs text-muted-foreground mt-1">Traffic automatically throttled away</p>
					</CardContent>
				</Card>
			</div>

			{/* Real-time Target Weight Distribution Table */}
			<Card>
				<CardHeader className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 pb-4">
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
						<Button variant="outline" size="sm" className="h-8 gap-1.5 text-xs">
							<RefreshCw className="h-3.5 w-3.5" />
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
									<TableCell colSpan={7} className="text-center py-8 text-muted-foreground">
										{isDev
											? "No matching targets found"
											: "No active adaptive routing telemetry recorded yet. Live requests will populate statistics automatically."}
									</TableCell>
								</TableRow>
							) : (
								filteredTargets.map((row) => (
									<TableRow key={row.target}>
										<TableCell className="font-medium">
											<div className="flex flex-col">
												<span className="font-semibold text-foreground">{row.target}</span>
												{row.keyId && <span className="text-[10px] text-muted-foreground font-mono">Key: {row.keyId}</span>}
											</div>
										</TableCell>
										<TableCell>
											<Badge
												variant="outline"
												className={
													row.status === "optimal"
														? "bg-emerald-500/10 text-emerald-600 border-emerald-200"
														: row.status === "healthy"
														? "bg-blue-500/10 text-blue-600 border-blue-200"
														: "bg-amber-500/10 text-amber-600 border-amber-200"
												}
											>
												{row.status.toUpperCase()}
											</Badge>
										</TableCell>
										<TableCell>{row.ewmaLatencyMs.toFixed(1)} ms</TableCell>
										<TableCell>{row.ttftMs.toFixed(1)} ms</TableCell>
										<TableCell>{row.p90LatencyMs.toFixed(1)} ms</TableCell>
										<TableCell>
											<span className={row.successRate > 98 ? "text-emerald-600 font-medium" : "text-amber-600 font-medium"}>
												{row.successRate.toFixed(1)}%
											</span>
										</TableCell>
										<TableCell className="text-right">
											<div className="flex items-center justify-end gap-2">
												<div className="w-16 bg-muted rounded-full h-2 overflow-hidden">
													<div
														className="bg-primary h-full rounded-full"
														style={{ width: `${Math.round(row.dynamicWeight * 100)}%` }}
													/>
												</div>
												<span className="font-bold text-foreground w-10 text-right">
													{(row.dynamicWeight * 100).toFixed(0)}%
												</span>
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