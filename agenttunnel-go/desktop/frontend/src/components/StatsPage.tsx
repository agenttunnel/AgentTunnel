import type { Rpc } from "../lib/rpc";
import { useModelStats } from "../lib/modelStats";
import { useTeamStats } from "../lib/teamStats";
import { HarnessStatsView } from "./HarnessStatsPanel";
import { ModelLeaderboard } from "./ModelLeaderboard";
import { TaskTimeChart } from "./TaskTimeChart";
import { TeamLeaderboard } from "./TeamLeaderboard";

/**
 * StatsPage is the environment-wide view: which harnesses and models run, what
 * they cost in tokens, how long their tasks take, and which teams resolve work
 * fastest on the fewest tokens. One fetch of the model rollup feeds both the
 * leaderboard and the timing chart.
 */
export function StatsPage({ rpc }: { rpc: Rpc | null }) {
  const models = useModelStats(rpc);
  const teams = useTeamStats(rpc);
  return (
    <>
      <div className="section stats-section">
        <HarnessStatsView rpc={rpc} threadId={null} projectId={null} />
      </div>
      <div className="section stats-section">
        <ModelLeaderboard
          stats={models.stats}
          loading={models.loading}
          error={models.error}
          onRefresh={models.refresh}
        />
      </div>
      <div className="section stats-section">
        <TaskTimeChart stats={models.stats} />
      </div>
      <div className="section stats-section">
        <TeamLeaderboard
          teams={teams.teams}
          loading={teams.loading}
          error={teams.error}
          onRefresh={teams.refresh}
        />
      </div>
    </>
  );
}
