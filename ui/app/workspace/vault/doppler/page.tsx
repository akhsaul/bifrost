import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { useFlushVaultCacheMutation, useGetVaultDopplerStatusQuery } from "@/lib/store";
import {
	CheckCircle2,
	Database,
	ExternalLink,
	FolderGit,
	Info,
	KeyRound,
	Lock,
	RefreshCw,
	ShieldAlert,
	ShieldCheck,
	XCircle,
	Zap,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

export default function DopplerVaultPage() {
	// refetchOnMountOrArgChange ensures fresh status when visiting the page without aggressive polling
	const { data: status, isLoading, isFetching, refetch } = useGetVaultDopplerStatusQuery();
	const [flushCache, { isLoading: isFlushing }] = useFlushVaultCacheMutation();
	const [lastFlushedAt, setLastFlushedAt] = useState<string | null>(null);

	const handleFlushCache = async () => {
		try {
			const res = await flushCache().unwrap();
			setLastFlushedAt(new Date().toLocaleTimeString());
			toast.success("Vault cache flushed", {
				description: res.message || "Next secret lookup will fetch fresh values from Doppler.",
			});
			refetch();
		} catch (err: any) {
			toast.error("Failed to flush vault cache", {
				description: err?.data?.message || err?.message || "An unexpected error occurred.",
			});
		}
	};

	if (isLoading) {
		return (
			<div className="flex h-64 w-full items-center justify-center">
				<RefreshCw className="text-muted-foreground h-6 w-6 animate-spin" />
				<span className="text-muted-foreground ml-2 text-sm">Loading Doppler status...</span>
			</div>
		);
	}

	const isEnabled = status?.enabled === true;
	const isConnected = isEnabled && status?.connected === true;

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-col gap-6 p-6">
			{/* Page Header */}
			<div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
				<div>
					<div className="flex items-center gap-2.5">
						<div className="bg-primary/10 text-primary flex h-9 w-9 items-center justify-center rounded-lg">
							<KeyRound className="h-5 w-5" />
						</div>
						<h1 className="text-2xl font-bold tracking-tight">Doppler SecretOps</h1>
						<Badge variant="outline" className="border-primary/20 bg-primary/5 text-primary text-xs font-semibold">
							Vault Provider
						</Badge>
					</div>
					<p className="text-muted-foreground mt-1 text-sm">
						Centralized secret management integration via Doppler API v3 for runtime secret resolution and rotation.
					</p>
				</div>

				{/* Header Actions */}
				<div className="flex items-center gap-2.5">
					<Button
						variant="outline"
						size="sm"
						onClick={() => refetch()}
						disabled={isFetching}
						className="cursor-pointer gap-1.5 text-xs"
					>
						<RefreshCw className={`h-3.5 w-3.5 ${isFetching ? "animate-spin" : ""}`} />
						Refresh Status
					</Button>
					{isEnabled && (
						<Button
							variant="default"
							size="sm"
							onClick={handleFlushCache}
							disabled={isFlushing}
							className="cursor-pointer gap-1.5 text-xs"
						>
							<Zap className={`h-3.5 w-3.5 ${isFlushing ? "animate-pulse" : ""}`} />
							{isFlushing ? "Flushing..." : "Flush Cache"}
						</Button>
					)}
				</div>
			</div>

			<Separator />

			{/* Vault Disabled Alert */}
			{!isEnabled && (
				<Alert variant="destructive" className="border-destructive/30 bg-destructive/5">
					<ShieldAlert className="h-4 w-4" />
					<AlertTitle>Doppler Vault Integration is Disabled</AlertTitle>
					<AlertDescription className="mt-2 space-y-2 text-xs">
						<p>
							To enable Doppler as your external secret manager, configure the{" "}
							<code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">vault_store</code> block inside{" "}
							<code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">config_store</code> in your{" "}
							<code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">config.json</code>:
						</p>
						<pre className="bg-background/80 text-foreground overflow-x-auto rounded-md border p-3 font-mono text-xs">
{`"config_store": {
  "vault_store": {
    "enabled": true,
    "type": "doppler",
    "prefix": "bifrost",
    "access_mode": "read_only",
    "doppler": {
      "token": "env.DOPPLER_TOKEN",
      "project": "my-project",
      "config": "prd"
    }
  }
}`}
						</pre>
					</AlertDescription>
				</Alert>
			)}

			{/* Top Overview Cards */}
			<div className="grid grid-cols-1 gap-4 md:grid-cols-3">
				{/* 1. Connection Status Card */}
				<Card>
					<CardHeader className="pb-2">
						<CardDescription className="text-xs">Connection Status</CardDescription>
						<CardTitle className="flex items-center gap-2 text-base">
							{isConnected ? (
								<>
									<CheckCircle2 className="h-5 w-5 text-emerald-500" />
									<span className="text-emerald-600 dark:text-emerald-400">Connected</span>
								</>
							) : isEnabled ? (
								<>
									<XCircle className="text-destructive h-5 w-5" />
									<span className="text-destructive">Connection Failed</span>
								</>
							) : (
								<>
									<Lock className="text-muted-foreground h-5 w-5" />
									<span className="text-muted-foreground">Disabled</span>
								</>
							)}
						</CardTitle>
					</CardHeader>
					<CardContent className="text-muted-foreground space-y-1 text-xs">
						<div className="flex justify-between">
							<span>API Endpoint:</span>
							<span className="font-mono font-medium text-foreground">{status?.base_url || "https://api.doppler.com"}</span>
						</div>
						{status?.error && (
							<p className="text-destructive mt-1 rounded bg-destructive/10 p-1.5 text-xs">{status.error}</p>
						)}
					</CardContent>
				</Card>

				{/* 2. Access Mode Card */}
				<Card>
					<CardHeader className="pb-2">
						<CardDescription className="text-xs">Access Mode</CardDescription>
						<CardTitle className="flex items-center gap-2 text-base">
							{status?.access_mode === "read_and_write" ? (
								<>
									<ShieldCheck className="h-5 w-5 text-emerald-500" />
									<span>Read & Write</span>
								</>
							) : (
								<>
									<ShieldCheck className="h-5 w-5 text-sky-500" />
									<span>Read Only</span>
								</>
							)}
						</CardTitle>
					</CardHeader>
					<CardContent className="text-muted-foreground text-xs">
						{status?.access_mode === "read_and_write" ? (
							<p>Bifrost automatically resolves references, auto-stores keys created via UI, and removes deleted keys.</p>
						) : (
							<p>Bifrost resolves existing references only. Writes and deletes to Doppler are disabled.</p>
						)}
					</CardContent>
				</Card>

				{/* 3. Namespace & Prefix */}
				<Card>
					<CardHeader className="pb-2">
						<CardDescription className="text-xs">Namespace Prefix</CardDescription>
						<CardTitle className="flex items-center gap-2 font-mono text-base">
							<FolderGit className="text-muted-foreground h-4 w-4" />
							{status?.prefix || "bifrost"}
						</CardTitle>
					</CardHeader>
					<CardContent className="text-muted-foreground text-xs">
						<p>
							Prefix applied to Bifrost-managed secrets (e.g.{" "}
							<code className="bg-muted rounded px-1 font-mono text-foreground">
								{(status?.prefix || "BIFROST").toUpperCase()}_KEYS_*
							</code>
							).
						</p>
					</CardContent>
				</Card>
			</div>

			{/* Main Content Grid */}
			<div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
				{/* Token & Authentication Info */}
				<Card className="flex flex-col">
					<CardHeader>
						<div className="flex items-center gap-2">
							<ShieldCheck className="text-primary h-4 w-4" />
							<CardTitle className="text-base">Token & Identity Details</CardTitle>
						</div>
						<CardDescription className="text-xs">
							Information about the authenticated Doppler principal and workplace scope.
						</CardDescription>
					</CardHeader>
					<CardContent className="flex-1 space-y-4 text-xs">
						<div className="grid grid-cols-2 gap-3">
							<div className="rounded-lg border p-3">
								<span className="text-muted-foreground block text-[11px]">Workplace / Org</span>
								<span className="font-semibold text-foreground">
									{status?.authenticated_entity?.workplace?.name || "Configured Workspace"}
								</span>
							</div>

							<div className="rounded-lg border p-3">
								<span className="text-muted-foreground block text-[11px]">Identity / Token Name</span>
								<span className="font-semibold text-foreground">
									{status?.authenticated_entity?.name || status?.authenticated_entity?.token?.name || "Service Token"}
								</span>
							</div>

							<div className="rounded-lg border p-3">
								<span className="text-muted-foreground block text-[11px]">Default Project</span>
								<span className="font-mono font-semibold text-foreground">
									{status?.project || "(Per Reference)"}
								</span>
							</div>

							<div className="rounded-lg border p-3">
								<span className="text-muted-foreground block text-[11px]">Token Type</span>
								<span className="font-mono font-semibold text-foreground capitalize">
									{status?.authenticated_entity?.token?.type || status?.authenticated_entity?.type || "Service Token"}
								</span>
							</div>
						</div>
					</CardContent>
				</Card>

				{/* In-Memory Cache & Rotation Controls */}
				<Card className="flex flex-col">
					<CardHeader>
						<div className="flex items-center gap-2">
							<Database className="text-primary h-4 w-4" />
							<CardTitle className="text-base">In-Memory Cache & Rotation</CardTitle>
						</div>
						<CardDescription className="text-xs">
							Bifrost caches resolved Doppler secrets in-memory (1 hour TTL) to deliver sub-microsecond latency.
						</CardDescription>
					</CardHeader>
					<CardContent className="flex-1 space-y-4 text-xs">
						<div className="bg-muted/50 rounded-lg border p-3.5 space-y-2">
							<div className="flex items-center justify-between">
								<span className="font-medium text-foreground">Secret Cache Status:</span>
								<Badge variant="secondary" className="font-mono text-xs">
									Active (1h TTL)
								</Badge>
							</div>
							<p className="text-muted-foreground text-xs leading-relaxed">
								When you rotate or update a secret in Doppler Cloud, Bifrost picks it up on next TTL expiration. To apply secret rotations immediately across all workers, click <strong>Flush Cache</strong>.
							</p>
						</div>

						{lastFlushedAt && (
							<div className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
								<CheckCircle2 className="h-3.5 w-3.5" />
								<span>Last flushed at {lastFlushedAt}</span>
							</div>
						)}

						<div className="pt-2">
							<Button
								variant="outline"
								onClick={handleFlushCache}
								disabled={!isEnabled || isFlushing}
								className="w-full cursor-pointer justify-center gap-2"
							>
								<Zap className={`h-4 w-4 ${isFlushing ? "animate-pulse" : ""}`} />
								{isFlushing ? "Flushing In-Memory Cache..." : "Flush In-Memory Cache Now"}
							</Button>
						</div>
					</CardContent>
				</Card>
			</div>

			{/* Secret Reference Guide */}
			<Card>
				<CardHeader>
					<div className="flex items-center gap-2">
						<Info className="text-primary h-4 w-4" />
						<CardTitle className="text-base">How to Use Doppler References in Bifrost</CardTitle>
					</div>
					<CardDescription className="text-xs">
						Any credential field in Bifrost (provider keys, virtual key values, MCP client auth headers) accepts Doppler references.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3 text-xs">
					<div className="grid grid-cols-1 gap-3 md:grid-cols-3">
						<div className="bg-muted/40 rounded-lg border p-3 space-y-1.5">
							<span className="font-semibold text-foreground">1. Default Scope</span>
							<code className="bg-background text-primary block rounded border p-1.5 font-mono text-xs">
								vault.OPENAI_API_KEY
							</code>
							<p className="text-muted-foreground text-[11px]">
								Resolves <code className="font-mono">OPENAI_API_KEY</code> from configured default project & config.
							</p>
						</div>

						<div className="bg-muted/40 rounded-lg border p-3 space-y-1.5">
							<span className="font-semibold text-foreground">2. Explicit Scope</span>
							<code className="bg-background text-primary block rounded border p-1.5 font-mono text-xs">
								vault.my-proj/prd/API_KEY
							</code>
							<p className="text-muted-foreground text-[11px]">
								Resolves secret with explicit project and config slug directly in the reference path.
							</p>
						</div>

						<div className="bg-muted/40 rounded-lg border p-3 space-y-1.5">
							<span className="font-semibold text-foreground">3. JSON Fragment Extraction</span>
							<code className="bg-background text-primary block rounded border p-1.5 font-mono text-xs">
								vault.SHARED_KEYS#openai
							</code>
							<p className="text-muted-foreground text-[11px]">
								Parses JSON payload of <code className="font-mono">SHARED_KEYS</code> and extracts the <code className="font-mono">openai</code> field.
							</p>
						</div>
					</div>

					<div className="text-muted-foreground flex items-center gap-1.5 pt-2 text-xs">
						<span>Official Doppler API Documentation:</span>
						<a
							href="https://docs.doppler.com/reference/secrets-list"
							target="_blank"
							rel="noopener noreferrer"
							className="text-primary hover:underline inline-flex items-center gap-1 font-medium"
						>
							docs.doppler.com/reference/secrets-list
							<ExternalLink className="h-3 w-3" />
						</a>
					</div>
				</CardContent>
			</Card>
		</div>
	);
}
