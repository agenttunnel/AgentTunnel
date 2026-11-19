import { memo } from "react";
import type { Rpc } from "../lib/rpc";
import { LimitsMeter } from "./LimitsMeter";
import { Popover } from "./Popover";
import { RemoteSection } from "./RemoteSection";

/**
 * The sidebar's quiet footer: subscription usage and remote access open as
 * popovers, so the thread list keeps the column. Each panel mounts (and polls)
 * only while it is open.
 */
export const SidebarFooter = memo(function SidebarFooter({ rpc }: { rpc: Rpc | null }) {
  return (
    <div className="sidebar-foot nav-foot">
      <Popover
        className="foot-pop"
        placement="above"
        align="start"
        title="Subscription usage limits"
        label="Usage limits"
        trigger={
          <>
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
              <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />
            </svg>
            <span>Usage</span>
          </>
        }
      >
        <div className="limits-pop">
          <LimitsMeter rpc={rpc} />
          <p className="hint">No harness reports subscription limits.</p>
        </div>
      </Popover>
      <Popover
        className="foot-pop"
        placement="above"
        align="start"
        title="Reach this daemon from your phone or WhatsApp"
        label="Remote access"
        trigger={
          <>
            <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
              <rect x="7" y="2" width="10" height="20" rx="2" />
              <path d="M11 18h2" />
            </svg>
            <span>Remote</span>
          </>
        }
      >
        <div className="remote-pop">
          <RemoteSection rpc={rpc} />
        </div>
      </Popover>
    </div>
  );
});
