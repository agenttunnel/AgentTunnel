import type { Rpc } from "./rpc";
import { useStatsRpc } from "./rpcStats";

/** One team across every turn in the environment (stats.teams). */
export type TeamStat = {
  /** "harness/model" pairs that worked the turns together, sorted. */
  members: string[];
  tasks: number;
  resolved: number;
  medianSeconds: number;
  p90Seconds: number;
  medianTokens: number;
};

const NONE: TeamStat[] = [];

/** The team rollup, loaded once and again only on refresh. */
export function useTeamStats(rpc: Rpc | null) {
  const { data, loading, error, refresh } = useStatsRpc<TeamStat[]>(rpc, "stats.teams", NONE);
  return { teams: data, loading, error, refresh };
}
