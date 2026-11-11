import type { Rpc } from "../lib/rpc";
import { useModelStats } from "../lib/modelStats";
import { HarnessStatsView } from "./HarnessStatsPanel";
import { ModelLeaderboard } from "./ModelLeaderboard";
import { TaskTimeChart } from "./TaskTimeChart";

/**
 * StatsPage is the environment-wide view: which harnesses and models run, what
 * they cost in tokens, and how long their tasks take. One fetch of the model
 * rollup feeds both the leaderboard and the timing chart.
 */
export function StatsPage({ rpc }: { rpc: Rpc | null }) {
  const models = useModelStats(rpc);
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
    </>
  );
}
