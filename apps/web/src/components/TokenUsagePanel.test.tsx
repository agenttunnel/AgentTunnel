import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vite-plus/test";

import { formatShortTimestamp } from "../timestampFormat";
import { SubagentTurnsTable } from "./TokenUsagePanel";
import { deriveSubagentUsageStats } from "./TokenUsagePanel.logic";

describe("SubagentTurnsTable", () => {
  it("gives each step's row the time the adapter logged it", () => {
    // Local-time instants plus a pinned clock, so the expected label holds in
    // any test timezone.
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date(2026, 7, 14, 12, 0));
      const at = new Date(2026, 7, 14, 9, 5, 30).toISOString();
      const stats = deriveSubagentUsageStats({
        entries: [{ kind: "usage", totalTokens: 1_200, inputTokens: 1_000, at }],
        timestampFormat: "12-hour",
      });
      const timeLabel = formatShortTimestamp(at, "12-hour");

      const markup = renderToStaticMarkup(<SubagentTurnsTable turns={stats.turns} />);

      expect(markup).toContain(">When</th>");
      expect(markup).toContain(`>${timeLabel}</td>`);
      expect(markup).toContain(">#1</td>");
      expect(markup).toContain(">1.2k</td>");
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a dash for steps logged before the adapter recorded their time", () => {
    const stats = deriveSubagentUsageStats({
      entries: [{ kind: "usage", totalTokens: 1_200 }],
      timestampFormat: "12-hour",
    });

    const markup = renderToStaticMarkup(<SubagentTurnsTable turns={stats.turns} />);

    expect(markup).toContain(">—</td>");
    expect(markup).toContain(">1.2k</td>");
  });
});
