import { memo, useEffect, useMemo, useRef } from "react";
import { harnessTint, type MailboxMessage } from "../lib/graph";
import { switchProps } from "../lib/switchProps";
import { Markdown } from "./Markdown";

/**
 * PeerChat renders the thread's agent-to-agent mailbox as one group chat:
 * every message any agent sent to a peer streams past in order, grouped the
 * way chat apps group — a divider per day, and one sender header per run of
 * consecutive messages. Data is ThreadState.mailbox, refreshed by
 * mailbox.posted through the existing subscribe loop, so this view is pure
 * render and never talks to the daemon itself.
 */

/** The lead's two names: "orchestrator" is how peers address it (and how it
 * may appear as a raw `from`), "agent-orchestrator" is its state id. */
const LEAD_IDS = new Set(["orchestrator", "agent-orchestrator"]);
/** Recipients that name everyone rather than one agent. The daemon's `to`
 * is always a resolved agent id, but an empty or wildcard value renders as
 * a broadcast instead of a blank recipient. */
const BROADCAST_TO = new Set(["", "*", "all", "everyone", "team"]);
/** Consecutive messages to one recipient share a sender header while they
 * arrive inside this window, the way group chats collapse runs. */
const GROUP_GAP_MS = 5 * 60 * 1000;
/** Auto-scroll sticks only while the user is this close to the bottom. */
const NEAR_BOTTOM_PX = 48;

type PeerAgent = {
  id: string;
  name?: string;
  role: string;
  harness?: string;
};

/** Short local time, with the full date on hover (usage-table convention). */
const fmtTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
};
const fullTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString();
};
const ms = (iso: string) => {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 0 : t;
};
/** Local calendar day, so a divider lands where the user's day turned. */
const dayKey = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
};
const fmtDay = (iso: string) => {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date();
  const opts: Intl.DateTimeFormatOptions = { month: "short", day: "numeric" };
  if (d.getFullYear() !== now.getFullYear()) opts.year = "numeric";
  return d.toLocaleDateString([], opts);
};

/** One rendered row: a day divider, or a message carrying everything the
 * row needs as primitives (so memoized rows skip re-render when a refresh
 * replaces the mailbox array but a message's content is unchanged). */
type PeerRow =
  | { kind: "day"; label: string }
  | {
      kind: "msg";
      body: string;
      time: string;
      fullAt: string;
      /** Header on the first message of a run; later ones show only time on hover. */
      first: boolean;
      from: string;
      initial: string;
      tint: string;
      lead: boolean;
      role: string;
      /** Recipient display name, or "" for a broadcast. */
      to: string;
      /** Kind chip, only for kinds worth flagging (notes stay quiet). */
      tag: string;
    };

const MsgRow = memo(function MsgRow(r: Extract<PeerRow, { kind: "msg" }>) {
  if (!r.first) {
    return (
      <div className="pc-msg" title={r.fullAt}>
        <div className="pc-bubble">
          <Markdown text={r.body} />
        </div>
      </div>
    );
  }
  return (
    <div className="pc-msg first">
      <div className="pc-head">
        <span className={`pc-ava ${r.tint}`} aria-hidden="true">
          {r.initial}
        </span>
        <span className="pc-name">
          {r.from}
          {r.lead && <span className="pc-lead">lead</span>}
        </span>
        {r.role && <span className="pc-role">{r.role}</span>}
        <span className="pc-to">{r.to ? `to ${r.to}` : "to everyone"}</span>
        {r.tag && <span className="pc-kind">{r.tag}</span>}
        {r.time && (
          <span className="pc-time" title={r.fullAt}>
            {r.time}
          </span>
        )}
      </div>
      <div className="pc-bubble">
        <Markdown text={r.body} />
      </div>
    </div>
  );
});

export function PeerChat({
  agents,
  roleNames,
  mailbox,
  peerMailbox,
  onTogglePeerMailbox,
}: {
  agents: PeerAgent[];
  roleNames: Record<string, string>;
  mailbox: MailboxMessage[] | null;
  peerMailbox: boolean;
  onTogglePeerMailbox: () => void;
}) {
  const byId = useMemo(() => {
    const map = new Map<string, PeerAgent>();
    for (const a of agents) if (a?.id) map.set(a.id, a);
    return map;
  }, [agents]);

  const nameOf = (id: string): string => {
    if (LEAD_IDS.has(id)) {
      const lead = byId.get("agent-orchestrator");
      return lead?.name || roleNames[lead?.role ?? ""] || "Lead";
    }
    const a = byId.get(id);
    return a?.name || roleNames[a?.role ?? ""] || id;
  };

  // Sorted defensively (event order is chronological, but timestamps are
  // what the grouping reads) and null-guarded: a daemon reply or replayed
  // thread may carry no mailbox at all.
  const messages = useMemo(
    () =>
      (mailbox ?? [])
        .filter((m): m is MailboxMessage => !!m && typeof m.body === "string")
        .slice()
        .sort((a, b) => ms(a.at) - ms(b.at)),
    [mailbox],
  );

  const rows = useMemo<PeerRow[]>(() => {
    const out: PeerRow[] = [];
    let prev: { from: string; to: string; day: string; at: number } | null = null;
    for (const m of messages) {
      const day = dayKey(m.at);
      if (!prev || day !== prev.day) out.push({ kind: "day", label: fmtDay(m.at) });
      const first =
        !prev ||
        day !== prev.day ||
        prev.from !== m.from ||
        prev.to !== m.to ||
        ms(m.at) - prev.at > GROUP_GAP_MS;
      const agent = byId.get(m.from);
      const lead = LEAD_IDS.has(m.from);
      const name = nameOf(m.from);
      out.push({
        kind: "msg",
        body: m.body,
        time: fmtTime(m.at),
        fullAt: fullTime(m.at),
        first,
        from: name,
        initial: (name.slice(0, 1) || "?").toUpperCase(),
        tint: harnessTint(agent?.harness ?? ""),
        lead,
        role: lead ? "" : roleNames[agent?.role ?? ""] ?? "",
        to: BROADCAST_TO.has(m.to ?? "") ? "" : nameOf(m.to),
        tag: m.kind && m.kind !== "note" ? m.kind : "",
      });
      prev = { from: m.from, to: m.to, day, at: ms(m.at) };
    }
    return out;
  }, [messages, byId, roleNames]);

  // Auto-scroll only while the user sits near the bottom (48px), so reading
  // history is never yanked around by a live exchange.
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const stick = useRef(true);
  const onScroll = () => {
    const el = scrollRef.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
  };
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [rows.length]);

  if (!peerMailbox) {
    return (
      <div className="stream pc-off">
        <div className="pc-off-inner">
          <div className="pc-off-title">Peer chat is off</div>
          <p className="pc-off-sub">
            Agents in this thread cannot message each other; the lead relays everything. Turn
            peer chat on to let subagents talk directly — their exchanges show up here.
          </p>
          <div
            className="toggle"
            {...switchProps(false, onTogglePeerMailbox)}
            aria-label="Let subagents message each other"
          >
            <span className="sw" /> Peer chat
          </div>
        </div>
      </div>
    );
  }

  if (messages.length === 0) {
    return (
      <div className="stream pc-stream">
        <p className="graph-hint">
          No messages between agents yet. Subagents can write to each other with the message
          tool, e.g.{" "}
          <code>$AT_TOOL tool message --to researcher-1 --text "parser is ready"</code>.
        </p>
      </div>
    );
  }

  return (
    <div className="stream pc-stream" ref={scrollRef} onScroll={onScroll}>
      {rows.map((r, i) =>
        r.kind === "day" ? (
          <div className="pc-day" key={`d${i}`}>
            {r.label}
          </div>
        ) : (
          <MsgRow key={`m${i}`} {...r} />
        ),
      )}
    </div>
  );
}
