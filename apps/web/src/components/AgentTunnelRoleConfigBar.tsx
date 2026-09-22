import type { AgentTunnelRoleOption } from "@t3tools/client-runtime/state/agentTunnelRoles";

const shortName = (value: string | null, fallback: string): string =>
  value === null ? fallback : (value.split(/[\\/]/).pop() ?? value);

/** Always-visible summary of each AgentTunnel role's model configuration. */
export function AgentTunnelRoleConfigBar({
  roles,
}: {
  roles: ReadonlyArray<AgentTunnelRoleOption>;
}) {
  if (roles.length === 0) {
    return null;
  }
  return (
    <div
      data-agenttunnel-role-config="true"
      className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-0.5 px-3 pb-1 font-mono text-[.625rem] leading-4 text-muted-foreground sm:px-4"
    >
      <span className="uppercase tracking-wider text-muted-foreground/60">Model config</span>
      {roles.map((role) => (
        <span key={role.id} className="inline-flex min-w-0 items-center gap-1">
          <span className="text-muted-foreground/80">{role.label}</span>
          <span className="truncate text-foreground/80">
            {role.auto ? "auto" : shortName(role.model, "default")}
          </span>
          <span className="text-muted-foreground/50">
            {shortName(role.harness, "opencode")}
            {role.variant ? ` · ${role.variant}` : ""}
          </span>
        </span>
      ))}
    </div>
  );
}
