import { createFileRoute } from "@tanstack/react-router";
import AgentSetupPage from "./page";

export const Route = createFileRoute("/workspace/agent-setup")({
	component: AgentSetupPage,
});