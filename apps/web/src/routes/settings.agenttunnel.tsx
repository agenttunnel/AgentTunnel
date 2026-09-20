import { createFileRoute } from "@tanstack/react-router";

import { ProviderSettingsPanel } from "../components/settings/ProviderSettingsPanel";

function SettingsAgentTunnelRoute() {
  return <ProviderSettingsPanel />;
}

export const Route = createFileRoute("/settings/agenttunnel")({
  component: SettingsAgentTunnelRoute,
});
