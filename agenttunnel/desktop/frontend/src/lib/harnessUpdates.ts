// What the daemon reports about provider CLIs (harness.updates / harness.update).
export type HarnessUpdate = {
  harness: string;
  binary?: string;
  installed?: string;
  latest?: string;
  updateAvailable: boolean;
  updatable: boolean;
  reason?: string;
  command?: string;
};

export type HarnessUpdateReport = {
  checkedAt: string;
  harnesses: HarnessUpdate[];
};

export type HarnessUpdateResult = {
  updated: string[];
  failed?: { harness: string; error: string }[];
  report: HarnessUpdateReport;
};

// pendingUpdates is the set the titlebar button offers: behind and upgradable.
export function pendingUpdates(report: HarnessUpdateReport | null): HarnessUpdate[] {
  if (!report) return [];
  return report.harnesses.filter((h) => h.updateAvailable && h.updatable);
}
