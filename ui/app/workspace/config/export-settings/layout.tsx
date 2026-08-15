import { createFileRoute } from "@tanstack/react-router";
import ExportSettingsPage from "./page";

export const Route = createFileRoute("/workspace/config/export-settings")({
	component: ExportSettingsPage,
});