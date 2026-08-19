import { createFileRoute } from "@tanstack/react-router";
import DopplerVaultPage from "./page";

export const Route = createFileRoute("/workspace/vault/doppler")({
	component: DopplerVaultPage,
});
