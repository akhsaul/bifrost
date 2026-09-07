import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import { useGetAdaptiveRoutingConfigQuery, useUpdateAdaptiveRoutingConfigMutation } from "@/lib/store/apis";
import { Link } from "@tanstack/react-router";
import { Activity, ArrowLeft, Cpu, Save, Settings, ShieldCheck, Zap } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

export default function LoadBalancerSettingsView() {
	const { data: currentConfig, isLoading } = useGetAdaptiveRoutingConfigQuery();
	const [updateConfig, { isLoading: isSaving }] = useUpdateAdaptiveRoutingConfigMutation();

	const [enabled, setEnabled] = useState(true);
	const [explorationFloor, setExplorationFloor] = useState(5);
	const [ewmaAlpha, setEwmaAlpha] = useState(20);
	const [powerK, setPowerK] = useState(15); // 1.5
	const [penalty429, setPenalty429] = useState(50); // 5.0
	const [penalty5xx, setPenalty5xx] = useState(100); // 10.0

	// Hydrate local state from server config once loaded
	useEffect(() => {
		if (!currentConfig) return;
		setEnabled(currentConfig.enabled);
		if (currentConfig.exploration_floor !== undefined) {
			setExplorationFloor(Math.round(currentConfig.exploration_floor * 100));
		}
		if (currentConfig.alpha !== undefined) {
			setEwmaAlpha(Math.round(currentConfig.alpha * 100));
		}
		if (currentConfig.power_k !== undefined) {
			setPowerK(Math.round(currentConfig.power_k * 10));
		}
		if (currentConfig.penalty_429 !== undefined) {
			setPenalty429(Math.round(currentConfig.penalty_429 * 10));
		}
		if (currentConfig.penalty_5xx !== undefined) {
			setPenalty5xx(Math.round(currentConfig.penalty_5xx * 10));
		}
	}, [currentConfig]);

	const handleSave = async () => {
		try {
			await updateConfig({
				enabled,
				exploration_floor: explorationFloor / 100,
				alpha: ewmaAlpha / 100,
				power_k: powerK / 10,
				penalty_429: penalty429 / 10,
				penalty_5xx: penalty5xx / 10,
				tuning_interval: currentConfig?.tuning_interval,
				window_size: currentConfig?.window_size,
			}).unwrap();
			toast.success("Adaptive routing settings saved successfully");
		} catch {
			toast.error("Failed to save adaptive routing settings");
		}
	};

	return (
		<div className="mx-auto flex w-full max-w-5xl flex-col gap-6 pb-12">
			{/* Header */}
			<div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-center">
				<div className="flex items-center gap-3">
					<Button variant="ghost" size="icon" asChild>
						<Link to="/workspace/adaptive-routing">
							<ArrowLeft className="h-4 w-4" />
						</Link>
					</Button>
					<div>
						<div className="flex items-center gap-2">
							<Settings className="text-primary h-5 w-5" />
							<h1 className="text-xl font-bold tracking-tight">Adaptive Routing Settings</h1>
							<Badge variant="outline" className="bg-primary/10 text-primary border-primary/20 text-xs">
								OSS
							</Badge>
						</div>
						<p className="text-muted-foreground mt-0.5 text-xs">
							Configure real-time tuning algorithms, penalty weights, and exploration floor.
						</p>
					</div>
				</div>
				<Button onClick={handleSave} disabled={isLoading || isSaving} size="sm" className="gap-2">
					<Save className="h-4 w-4" />
					Save Changes
				</Button>
			</div>

			{/* Master Switch */}
			<Card>
				<CardHeader className="flex flex-row items-center justify-between space-y-0 pb-4">
					<div>
						<CardTitle className="text-sm font-semibold">Enable Adaptive Routing</CardTitle>
						<CardDescription className="text-xs">
							When enabled, routing rules with strategy "adaptive" modulate candidate provider weights dynamically based on real-time
							metrics.
						</CardDescription>
					</div>
					<Switch checked={enabled} onCheckedChange={setEnabled} />
				</CardHeader>
			</Card>

			{/* Core Tuning Parameters */}
			<div className="grid grid-cols-1 gap-6 md:grid-cols-2">
				<Card>
					<CardHeader>
						<CardTitle className="flex items-center gap-2 text-sm font-semibold">
							<Cpu className="text-primary h-4 w-4" />
							Exploration Floor (Epsilon ε)
						</CardTitle>
						<CardDescription className="text-xs">
							Minimum traffic share allocated to slower or degraded routes to probe latency recovery and prevent provider starvation.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="flex items-center justify-between text-xs font-semibold">
							<span>Min Exploration Traffic</span>
							<span>{explorationFloor}%</span>
						</div>
						<Slider value={[explorationFloor]} onValueChange={(val) => setExplorationFloor(val[0])} min={1} max={20} step={1} />
						<p className="text-muted-foreground text-[11px]">
							Default: 5%. Lower values shift traffic more aggressively to the fastest target.
						</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader>
						<CardTitle className="flex items-center gap-2 text-sm font-semibold">
							<Activity className="text-primary h-4 w-4" />
							EWMA Decay Factor (Alpha α)
						</CardTitle>
						<CardDescription className="text-xs">
							Determines how quickly the adaptive engine responds to recent latency changes and spikes.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="flex items-center justify-between text-xs font-semibold">
							<span>Responsiveness (Alpha)</span>
							<span>{(ewmaAlpha / 100).toFixed(2)}</span>
						</div>
						<Slider value={[ewmaAlpha]} onValueChange={(val) => setEwmaAlpha(val[0])} min={5} max={50} step={5} />
						<p className="text-muted-foreground text-[11px]">Default: 0.20. Higher values react faster to instant network spikes.</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader>
						<CardTitle className="flex items-center gap-2 text-sm font-semibold">
							<Zap className="text-primary h-4 w-4" />
							Inverse Latency Exponent (k)
						</CardTitle>
						<CardDescription className="text-xs">Power exponent controlling the steepness of the inverse latency curve.</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="flex items-center justify-between text-xs font-semibold">
							<span>Power Exponent (k)</span>
							<span>{(powerK / 10).toFixed(1)}</span>
						</div>
						<Slider value={[powerK]} onValueChange={(val) => setPowerK(val[0])} min={10} max={30} step={1} />
						<p className="text-muted-foreground text-[11px]">
							Default: 1.5. Higher values penalize moderate latency differences more severely.
						</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader>
						<CardTitle className="flex items-center gap-2 text-sm font-semibold">
							<ShieldCheck className="text-primary h-4 w-4" />
							Error Penalty Multipliers
						</CardTitle>
						<CardDescription className="text-xs">
							Virtual latency penalty applied when upstream returns 429 rate limits or 5xx server errors.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="space-y-2">
							<div className="flex items-center justify-between text-xs font-semibold">
								<span>HTTP 429 Multiplier</span>
								<span>{(penalty429 / 10).toFixed(1)}x</span>
							</div>
							<Slider value={[penalty429]} onValueChange={(val) => setPenalty429(val[0])} min={10} max={100} step={5} />
						</div>
						<div className="space-y-2 pt-2">
							<div className="flex items-center justify-between text-xs font-semibold">
								<span>HTTP 5xx Multiplier</span>
								<span>{(penalty5xx / 10).toFixed(1)}x</span>
							</div>
							<Slider value={[penalty5xx]} onValueChange={(val) => setPenalty5xx(val[0])} min={20} max={200} step={10} />
						</div>
					</CardContent>
				</Card>
			</div>
		</div>
	);
}