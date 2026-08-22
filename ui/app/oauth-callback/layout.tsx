import { createFileRoute } from "@tanstack/react-router";
import OAuthCallbackPage from "./page";

function RouteComponent() {
	return <OAuthCallbackPage />;
}

export const Route = createFileRoute("/oauth-callback")({
	component: RouteComponent,
});
