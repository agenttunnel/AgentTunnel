import { memo } from "react";
import { PHASE_LABEL, fmtElapsed, phaseLive, type AgentPhase } from "../lib/agentTree";
import { useNow } from "../lib/agentLive";

// Each phase has its own shape, so status never rests on color alone. Only a
// running agent spins.
const GLYPH: Record<AgentPhase, string> = {
  running: "",
  waiting: "?",
  blocked: "⏸",
  queued: "○",
  done: "✓",
  failed: "✕",
  stopped: "■",
};

/** The phase as a glyph, with its name for screen readers and on hover. */
export const PhaseGlyph = memo(function PhaseGlyph({ phase }: { phase: AgentPhase }) {
  return (
    <span className={`ag-glyph ag-${phase}`} title={PHASE_LABEL[phase]} role="img" aria-label={PHASE_LABEL[phase]}>
      {GLYPH[phase]}
    </span>
  );
});

/** The phase as a glyph plus its word: for headers and nodes with room. */
export const PhaseBadge = memo(function PhaseBadge({ phase }: { phase: AgentPhase }) {
  return (
    <span className={`ag-badge ag-${phase}`}>
      <span className={`ag-glyph ag-${phase}`} aria-hidden="true">
        {GLYPH[phase]}
      </span>
      {PHASE_LABEL[phase]}
    </span>
  );
});

/**
 * How long the agent has worked: a live count while it runs, frozen at its end
 * once it settles, nothing before it starts. Ticks on the shared clock.
 */
export const AgentElapsed = memo(function AgentElapsed({
  phase,
  since,
  endedAt,
}: {
  phase: AgentPhase;
  since?: string;
  endedAt?: string;
}) {
  const live = phaseLive(phase);
  const now = useNow(live);
  if (!since || phase === "queued" || phase === "blocked") return null;
  const start = Date.parse(since);
  const end = live ? now : endedAt ? Date.parse(endedAt) : NaN;
  if (Number.isNaN(start) || Number.isNaN(end)) return null;
  return (
    <span className="ag-elapsed" title={live ? "Working for" : "Worked for"}>
      {fmtElapsed((end - start) / 1000)}
    </span>
  );
});
