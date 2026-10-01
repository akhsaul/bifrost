import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { createFileRoute } from "@tanstack/react-router";
import ExtraDetectionPage from "./page";

function RouteComponent() {
	const hasRoutingRulesAccess = useRbac(RbacResource.RoutingRules, RbacOperation.View);
	if (!hasRoutingRulesAccess) {
		return <NoPermissionView entity="extra detection" />;
	}
	return <ExtraDetectionPage />;
}

export const Route = createFileRoute("/workspace/extra-detection")({
	component: RouteComponent,
});