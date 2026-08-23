import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Slider } from "@/components/ui/slider";
import { Switch } from "@/components/ui/switch";
import { Link } from "@tanstack/react-router";
import { Activity, ArrowLeft, Cpu, Save, Settings, ShieldCheck, Zap } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

export default function LoadBalancerSettingsView() {
	const [enabled, setEnabled] = useState(true);
	const [explorationFloor, setExplorationFloor] = useState(5);
	const [ewmaAlpha, setEwmaAlpha] = useState(20);
	const [powerK, setPowerK] = useState(15); // 1.5
	const [penalty429, setPenalty429] = useState(50); // 5.0
	const [penalty5xx, setPenalty5xx] = useState(100); // 10.0

	const handleSave = () => {
		toast.success("Adaptive routing settings saved successfully");
	};

	return (
		<div className="flex flex-col gap-6 w-full max-w-5xl mx-auto pb-12">
			{/* Header */}
			<div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
				<div className="flex items-center gap-3">
					<Button variant="ghost" size="icon" asChild>
						<Link to="/workspace/adaptive-routing">
							<ArrowLeft className="h-4 w-4" />
						</Link>
					</Button>
					<div>
						<div className="flex items-center gap-2">
							<Settings className="h-5 w-5 text-primary" />
							<h1 className="text-xl font-bold tracking-tight">Adaptive Routing Settings</h1>
							<Badge variant="outline" className="text-xs bg-primary/10 text-primary border-primary/20">
								OSS
							</Badge>
						</div>
						<p className="text-muted-foreground text-xs mt-0.5">
							Configure real-time tuning algorithms, penalty weights, and exploration floor.
						</p>
					</div>
				</div>
				<Button onClick={handleSave} size="sm" className="gap-2">
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
							When enabled, routing rules with strategy "adaptive" modulate candidate provider weights dynamically based on real-time metrics.
						</CardDescription>
					</div>
					<Switch checked={enabled} onCheckedChange={setEnabled} />
				</CardHeader>
			</Card>

			{/* Core Tuning Parameters */}
			<div className="grid grid-cols-1 md:grid-cols-2 gap-6">
				<Card>
					<CardHeader>
						<CardTitle className="text-sm font-semibold flex items-center gap-2">
							<Cpu className="h-4 w-4 text-primary" />
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
						<Slider
							value={[explorationFloor]}
							onValueChange={(val) => setExplorationFloor(val[0])}
							min={1}
							max={20}
							step={1}
						/>
						<p className="text-[11px] text-muted-foreground">
							Default: 5%. Lower values shift traffic more aggressively to the fastest target.
						</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader>
						<CardTitle className="text-sm font-semibold flex items-center gap-2">
							<Activity className="h-4 w-4 text-primary" />
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
						<Slider
							value={[ewmaAlpha]}
							onValueChange={(val) => setEwmaAlpha(val[0])}
							min={5}
							max={50}
							step={5}
						/>
						<p className="text-[11px] text-muted-foreground">
							Default: 0.20. Higher values react faster to instant network spikes.
						</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader>
						<CardTitle className="text-sm font-semibold flex items-center gap-2">
							<Zap className="h-4 w-4 text-primary" />
							Inverse Latency Exponent (k)
						</CardTitle>
						<CardDescription className="text-xs">
							Power exponent controlling the steepness of the inverse latency curve.
						</CardDescription>
					</CardHeader>
					<CardContent className="space-y-4">
						<div className="flex items-center justify-between text-xs font-semibold">
							<span>Power Exponent (k)</span>
							<span>{(powerK / 10).toFixed(1)}</span>
						</div>
						<Slider
							value={[powerK]}
							onValueChange={(val) => setPowerK(val[0])}
							min={10}
							max={30}
							step={1}
						/>
						<p className="text-[11px] text-muted-foreground">
							Default: 1.5. Higher values penalize moderate latency differences more severely.
						</p>
					</CardContent>
				</Card>

				<Card>
					<CardHeader>
						<CardTitle className="text-sm font-semibold flex items-center gap-2">
							<ShieldCheck className="h-4 w-4 text-primary" />
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
							<Slider
								value={[penalty429]}
								onValueChange={(val) => setPenalty429(val[0])}
								min={10}
								max={100}
								step={5}
							/>
						</div>
						<div className="space-y-2 pt-2">
							<div className="flex items-center justify-between text-xs font-semibold">
								<span>HTTP 5xx Multiplier</span>
								<span>{(penalty5xx / 10).toFixed(1)}x</span>
							</div>
							<Slider
								value={[penalty5xx]}
								onValueChange={(val) => setPenalty5xx(val[0])}
								min={20}
								max={200}
								step={10}
							/>
						</div>
					</CardContent>
				</Card>
			</div>
		</div>
	);
}