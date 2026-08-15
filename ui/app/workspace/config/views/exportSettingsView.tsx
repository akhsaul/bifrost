import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { useGetExportConfigQuery } from "@/lib/store";
import { Check, Copy, Download, ExternalLink, FileCode, RefreshCw } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { toast } from "sonner";

export default function ExportSettingsView() {
	const { data: configData, isLoading, isFetching, isError, refetch } = useGetExportConfigQuery();
	const [hasCopied, setHasCopied] = useState(false);

	const formattedConfig = useMemo(() => {
		if (!configData) return "";
		return JSON.stringify(configData, null, 2);
	}, [configData]);

	const handleCopy = useCallback(async () => {
		if (!formattedConfig) return;
		try {
			await navigator.clipboard.writeText(formattedConfig);
			setHasCopied(true);
			toast.success("Configuration copied to clipboard");
			setTimeout(() => setHasCopied(false), 2000);
		} catch (error) {
			toast.error("Failed to copy configuration to clipboard");
		}
	}, [formattedConfig]);

	const handleDownload = useCallback(() => {
		if (!formattedConfig) return;
		try {
			const blob = new Blob([formattedConfig], { type: "application/json" });
			const url = URL.createObjectURL(blob);
			const link = document.createElement("a");
			link.href = url;
			link.download = "config.json";
			document.body.appendChild(link);
			link.click();
			document.body.removeChild(link);
			URL.revokeObjectURL(url);
			toast.success("Downloaded config.json");
		} catch (error) {
			toast.error("Failed to download configuration");
		}
	}, [formattedConfig]);

	return (
		<div className="flex flex-col gap-6 p-6">
			<div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">Export Settings</h2>
					<p className="text-muted-foreground text-sm">
						Export the current active Bifrost configuration conforming to{" "}
						<a
							href="https://www.getbifrost.ai/schema"
							target="_blank"
							rel="noreferrer"
							className="text-primary inline-flex items-center gap-1 font-medium underline underline-offset-4 hover:opacity-80"
						>
							https://www.getbifrost.ai/schema
							<ExternalLink className="h-3 w-3" />
						</a>
					</p>
				</div>
				<div className="flex items-center gap-2">
					<Button variant="outline" size="sm" onClick={() => refetch()} disabled={isLoading || isFetching} title="Refresh configuration">
						<RefreshCw className={`mr-1.5 h-4 w-4 ${isFetching ? "animate-spin" : ""}`} />
						Refresh
					</Button>
					<Button variant="outline" size="sm" onClick={handleCopy} disabled={!formattedConfig || isLoading} title="Copy to clipboard">
						{hasCopied ? (
							<>
								<Check className="mr-1.5 h-4 w-4 text-green-500" />
								Copied
							</>
						) : (
							<>
								<Copy className="mr-1.5 h-4 w-4" />
								Copy
							</>
						)}
					</Button>
					<Button size="sm" onClick={handleDownload} disabled={!formattedConfig || isLoading} title="Download config.json">
						<Download className="mr-1.5 h-4 w-4" />
						Download config.json
					</Button>
				</div>
			</div>

			<div className="bg-card text-card-foreground rounded-lg border shadow-sm">
				<div className="bg-muted/40 flex items-center justify-between border-b px-4 py-2.5">
					<div className="text-muted-foreground flex items-center gap-2 font-mono text-xs">
						<FileCode className="h-4 w-4" />
						<span>config.json</span>
					</div>
					<div className="text-muted-foreground text-xs">
						{isLoading ? "Loading configuration..." : `${(new Blob([formattedConfig]).size / 1024).toFixed(1)} KB`}
					</div>
				</div>

				<div className="p-0">
					{isLoading ? (
						<div className="text-muted-foreground flex h-[600px] w-full items-center justify-center text-sm">
							<RefreshCw className="mr-2 h-5 w-5 animate-spin" />
							Generating configuration export...
						</div>
					) : isError ? (
						<div className="text-destructive flex h-[600px] w-full flex-col items-center justify-center gap-3 text-sm">
							<p>Failed to load configuration export.</p>
							<Button variant="outline" size="sm" onClick={() => refetch()}>
								Retry
							</Button>
						</div>
					) : (
						<CodeEditor
							className="w-full font-mono text-sm"
							code={formattedConfig}
							lang="json"
							readonly={true}
							wrap={true}
							height={650}
							options={{
								collapsibleBlocks: true,
								lineNumbers: "on",
								scrollBeyondLastLine: false,
							}}
						/>
					)}
				</div>
			</div>
		</div>
	);
}