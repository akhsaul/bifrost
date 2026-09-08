import { Button } from "@/components/ui/button";
import { CheckCircle2, Copy, XCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

export default function OAuthCallbackPage() {
	const [code, setCode] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [closeAttempted, setCloseAttempted] = useState(false);

	useEffect(() => {
		const params = new URLSearchParams(window.location.search);
		const authCode = params.get("code");
		const authError = params.get("error");

		if (authCode) {
			setCode(authCode);
			if (window.opener) {
				window.opener.postMessage({ type: "antigravity_oauth_code", code: authCode }, "*");
				setCloseAttempted(true);
				setTimeout(() => {
					window.close();
				}, 500);
			}
		} else if (authError) {
			setError(authError);
			if (window.opener) {
				window.opener.postMessage({ type: "antigravity_oauth_error", error: authError }, "*");
			}
		}
	}, []);

	const copyCode = () => {
		if (code) {
			navigator.clipboard.writeText(code);
			toast.success("Authorization code copied to clipboard");
		}
	};

	return (
		<div className="mx-auto flex min-h-[80vh] w-full max-w-lg items-center justify-center p-6">
			<div className="bg-card w-full space-y-4 rounded-xl border p-8 text-center shadow-md">
				{error ? (
					<>
						<div className="mx-auto flex size-12 items-center justify-center rounded-full bg-red-100 text-red-600 dark:bg-red-950/50">
							<XCircle className="size-6" />
						</div>
						<h1 className="text-xl font-semibold">Authorization Failed</h1>
						<p className="text-destructive text-sm">{error}</p>
					</>
				) : (
					<>
						<div className="mx-auto flex size-12 items-center justify-center rounded-full bg-green-100 text-green-600 dark:bg-green-950/50">
							<CheckCircle2 className="size-6" />
						</div>
						<h1 className="text-xl font-semibold">Authorization Successful</h1>
						<p className="text-muted-foreground text-sm">
							{closeAttempted
								? "You may now close this window and return to Bifrost."
								: "Your Google OAuth authorization code has been generated."}
						</p>
						{code && (
							<div className="mt-4 space-y-2 text-left">
								<p className="text-muted-foreground text-xs font-medium">Authorization Code:</p>
								<div className="bg-muted flex items-center gap-2 rounded-md p-2 font-mono text-xs break-all">
									<span className="flex-1 truncate">{code}</span>
									<Button size="icon" variant="ghost" className="size-6 shrink-0" onClick={copyCode}>
										<Copy className="size-3.5" />
									</Button>
								</div>
							</div>
						)}
					</>
				)}
			</div>
		</div>
	);
}