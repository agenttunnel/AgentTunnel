import {
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  type ClipboardEvent as ReactClipboardEvent,
  type DragEvent as ReactDragEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent,
  type MutableRefObject,
  type RefObject,
} from "react";
import { uploadImage, type Conn, type Rpc } from "../lib/rpc";
import { HOLD_MOD_LABEL } from "../lib/dictation";
import { useRotatingPlaceholder } from "../lib/placeholder";
import { dropMention, mentionAt, MAX_ATTACHMENTS, type FileMatch } from "../lib/mentions";
import { matchCommands, slashQuery, type SlashCommand } from "../lib/commands";
import { useStoreField } from "../lib/store";
import {
  composerStore,
  IMAGE_MIMES,
  MAX_IMAGE_BYTES,
  MAX_TURN_IMAGE_BYTES,
  MAX_TURN_IMAGES,
  pasteTokens,
  setAttachments,
  setCaret,
  setDraftImages,
  setDrafts,
  setMentionActive,
  setMentionDismissed,
  setMentionResults,
  setPastes,
  setRecall,
  setSlashClosed,
  setSlashIdx,
  type DraftImage,
} from "../lib/composerStore";
import { VoiceRecorder } from "./VoiceRecorder";
import { MentionMenu } from "./MentionMenu";
import { SlashMenu } from "./SlashMenu";
import { BtwPanel, type BtwEntry } from "./BtwPanel";

// Paste and image ids outlive a composer remount (switching tabs), like the
// drafts they key, so a new paste never reuses a live token's id.
let pasteSeq = 0;
let imageSeq = 0;

export type ComposerProps = {
  activeId: string | null;
  /** draftKeyOf(projectId, activeId): which draft the composer edits. */
  draftKey: string;
  /** A turn is running, so Enter steers it. */
  agentMode: boolean;
  hidden: boolean;
  taRef: RefObject<HTMLTextAreaElement>;
  /** A caret position to restore after the next text change. */
  pendingCaret: MutableRefObject<number | null>;
  rpcRef: { readonly current: Rpc | null };
  connRef: { readonly current: Conn | null };
  listening: boolean;
  onVoiceStop: () => void;
  onVoiceCancel: () => void;
  btwList: BtwEntry[] | null;
  onCloseBtw: () => void;
  projectName: string | null;
  projectPath: string | null;
  modelsOpen: boolean;
  onToggleModels: () => void;
  // The callbacks below must be stable: the composer is memoized so streamed
  // thread updates do not re-render it.
  onSend: () => void;
  onError: (message: string, threadId?: string) => void;
  slashCommands: () => SlashCommand[];
  onPickSlash: (cmd: SlashCommand, runNow: boolean) => void;
  /** The thread's sent prompts, oldest first, for ArrowUp recall. */
  userPrompts: () => string[];
};

/**
 * The thread composer. Its text, attachments, @ and / menus live in
 * composerStore, so typing re-renders this component and nothing else.
 */
export const Composer = memo(function Composer({
  activeId,
  draftKey,
  agentMode,
  hidden,
  taRef,
  pendingCaret,
  rpcRef,
  connRef,
  listening,
  onVoiceStop,
  onVoiceCancel,
  btwList,
  onCloseBtw,
  projectName,
  projectPath,
  modelsOpen,
  onToggleModels,
  onSend: send,
  onError: setError,
  slashCommands,
  onPickSlash: pickSlash,
  userPrompts,
}: ComposerProps) {
  const drafts = useStoreField(composerStore, "drafts");
  const pastes = useStoreField(composerStore, "pastes");
  const attachments = useStoreField(composerStore, "attachments");
  const draftImages = useStoreField(composerStore, "draftImages");
  const caret = useStoreField(composerStore, "caret");
  const mentionActive = useStoreField(composerStore, "mentionActive");
  const mentionDismissed = useStoreField(composerStore, "mentionDismissed");
  const mentionResults = useStoreField(composerStore, "mentionResults");
  const slashIdx = useStoreField(composerStore, "slashIdx");
  const slashClosed = useStoreField(composerStore, "slashClosed");
  const recall = useStoreField(composerStore, "recall");

  const input = drafts[draftKey] ?? "";
  // Mirrors send(): a leading "!" (after trimming) runs a shell command.
  const commandMode = input.trimStart().startsWith("!");
  const setInput = useCallback(
    (text: string) => {
      if (!activeId) return;
      setDrafts((prev) => ({ ...prev, [draftKey]: text }));
    },
    [activeId, draftKey],
  );

  const slashQ = slashQuery(input);
  const slashMatches: SlashCommand[] =
    slashQ !== null && slashClosed !== input ? matchCommands(slashCommands(), slashQ) : [];
  const slashOpen = slashMatches.length > 0;
  const slashSel = Math.min(slashIdx, slashMatches.length - 1);

  // Grow the composer with its text (CSS max-height caps it), then restore any caret we placed.
  useLayoutEffect(() => {
    const el = taRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
    if (pendingCaret.current !== null) {
      el.setSelectionRange(pendingCaret.current, pendingCaret.current);
      pendingCaret.current = null;
    }
  }, [input]);

  const replacePasteToken = (t: { start: number; end: number; id: number }, text: string) => {
    setInput(input.slice(0, t.start) + text + input.slice(t.end));
    pendingCaret.current = t.start + text.length;
    setPastes((prev) => {
      const rest = { ...prev[draftKey] };
      delete rest[t.id];
      return { ...prev, [draftKey]: rest };
    });
  };
  const onComposerPaste = (e: ReactClipboardEvent<HTMLTextAreaElement>) => {
    // An image on the clipboard becomes a draft chip: uploaded over HTTP as an
    // attachment, never inlined into the composer text.
    const imageFiles = [...e.clipboardData.files].filter((f) => f.type.startsWith("image/"));
    if (imageFiles.length > 0) {
      e.preventDefault();
      attachImages(imageFiles);
      return;
    }
    const text = e.clipboardData.getData("text/plain");
    const lines = text.split("\n").length;
    if (lines <= 6 && text.length <= 500) return;
    e.preventDefault();
    const { selectionStart: start, selectionEnd: end } = e.currentTarget;
    const id = ++pasteSeq;
    const token = `[Pasted text #${id} +${lines} lines]`;
    setInput(input.slice(0, start) + token + input.slice(end));
    pendingCaret.current = start + token.length;
    setPastes((prev) => ({ ...prev, [draftKey]: { ...prev[draftKey], [id]: text } }));
  };
  const onComposerClick = (e: MouseEvent<HTMLTextAreaElement>) => {
    const pos = e.currentTarget.selectionStart;
    const t = pasteTokens(input).find((tok) => pos >= tok.start && pos <= tok.end);
    const text = t && pastes[draftKey]?.[t.id];
    if (t && text !== undefined) replacePasteToken(t, text);
  };
  useRotatingPlaceholder(taRef, {
    rotate: !!activeId && !agentMode && !input,
    idle: agentMode
      ? "Steer the orchestrator — it lands on the next pass…"
      : "Ask the team to build something, or ! to run a shell command…",
  });
  // The @ menu: an @-token ends at the caret, and is closed for commands.
  const draftAttachments = attachments[draftKey] ?? [];
  const draftImageList = draftImages[draftKey] ?? [];
  const mention = commandMode ? null : mentionAt(input, caret);
  const openMention = mention && mention.start !== mentionDismissed ? mention : null;
  const mentionQuery = openMention?.query ?? null;
  const menuMatches =
    openMention && mentionResults?.query === openMention.query ? mentionResults.matches : null;
  useEffect(() => {
    const rpc = rpcRef.current;
    if (mentionQuery === null || !rpc || !activeId) return;
    let stale = false;
    // Debounced, and any keystroke after this one cancels the request.
    const timer = window.setTimeout(() => {
      rpc
        .call<{ matches: FileMatch[] }>("files.search", {
          threadId: activeId,
          query: mentionQuery,
          limit: 12,
        })
        .then((res) => {
          if (!stale) setMentionResults({ query: mentionQuery, matches: res.matches });
        })
        .catch((e) => {
          if (stale) return;
          setMentionResults({ query: mentionQuery, matches: [] });
          setError(e instanceof Error ? e.message : String(e));
        });
    }, 60);
    return () => {
      stale = true;
      window.clearTimeout(timer);
    };
  }, [mentionQuery, activeId]);
  const pickMention = (m: FileMatch) => {
    if (!openMention) return;
    if (draftAttachments.length >= MAX_ATTACHMENTS) {
      setError(`Attach at most ${MAX_ATTACHMENTS} files or folders per turn.`);
      return;
    }
    setInput(dropMention(input, openMention, caret));
    pendingCaret.current = openMention.start;
    setCaret(openMention.start);
    setMentionDismissed(null);
    setAttachments((prev) => {
      const list = prev[draftKey] ?? [];
      if (list.some((a) => a.path === m.path)) return prev;
      return { ...prev, [draftKey]: [...list, { path: m.path, dir: m.dir }] };
    });
  };
  const removeAttachment = (path: string) =>
    setAttachments((prev) => ({
      ...prev,
      [draftKey]: (prev[draftKey] ?? []).filter((a) => a.path !== path),
    }));

  /** Adds pasted or dropped image files as draft chips: a local preview shows
   * at once, the bytes upload over HTTP, and the chip carries the daemon's
   * attachment id once stored. */
  const attachImages = (files: File[]) => {
    const conn = connRef.current;
    const threadId = activeId;
    if (!conn || !threadId || files.length === 0) return;
    const current = draftImages[draftKey] ?? [];
    const accepted: File[] = [];
    // ref().size is 0 until the upload finishes, so in-flight images count by
    // their local bytes: the estimate cannot be beaten by rapid pastes.
    let total = current.reduce((sum, img) => sum + (img.ref?.size ?? img.file.size), 0);
    for (const file of files) {
      if (!IMAGE_MIMES.has(file.type)) {
        setError(`"${file.name}" is not a PNG, JPEG, WebP, or GIF image.`);
        continue;
      }
      if (file.size > MAX_IMAGE_BYTES) {
        setError(`"${file.name}" is larger than the 8 MiB image limit.`);
        continue;
      }
      if (current.length + accepted.length >= MAX_TURN_IMAGES) {
        setError(`Attach at most ${MAX_TURN_IMAGES} images per turn.`);
        break;
      }
      if (total + file.size > MAX_TURN_IMAGE_BYTES) {
        setError("Images would exceed the 32 MiB per-turn limit.");
        break;
      }
      total += file.size;
      accepted.push(file);
    }
    if (accepted.length === 0) return;
    const entries: DraftImage[] = accepted.map((file) => ({
      localId: ++imageSeq,
      name: file.name || "pasted image",
      file,
      previewUrl: URL.createObjectURL(file),
      uploading: true,
    }));
    setDraftImages((prev) => ({ ...prev, [draftKey]: [...(prev[draftKey] ?? []), ...entries] }));
    for (const [file, entry] of accepted.map((f, i) => [f, entries[i]] as const)) {
      uploadImage(conn.httpUrl, conn.token, threadId, file, entry.name)
        .then((ref) => {
          setDraftImages((prev) => ({
            ...prev,
            [draftKey]: (prev[draftKey] ?? []).map((img) =>
              img.localId === entry.localId ? { ...img, uploading: false, ref } : img,
            ),
          }));
        })
        .catch((e) => {
          setDraftImages((prev) => ({
            ...prev,
            [draftKey]: (prev[draftKey] ?? []).map((img) =>
              img.localId === entry.localId
                ? { ...img, uploading: false, error: e instanceof Error ? e.message : String(e) }
                : img,
            ),
          }));
        });
    }
  };
  const removeDraftImage = (localId: number) =>
    setDraftImages((prev) => {
      const list = prev[draftKey] ?? [];
      const gone = list.find((img) => img.localId === localId);
      if (gone) URL.revokeObjectURL(gone.previewUrl);
      return { ...prev, [draftKey]: list.filter((img) => img.localId !== localId) };
    });
  const onComposerDragOver = (e: ReactDragEvent<HTMLDivElement>) => {
    if (e.dataTransfer.types.includes("Files")) e.preventDefault();
  };
  const onComposerDrop = (e: ReactDragEvent<HTMLDivElement>) => {
    const files = [...e.dataTransfer.files];
    if (files.length === 0) return;
    // Swallow every file drop: the webview must not navigate away from the
    // app (and lose drafts) just because a file missed an image filter.
    e.preventDefault();
    attachImages(files.filter((f) => f.type.startsWith("image/")));
  };

  const onComposerKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    // Enter confirms an IME candidate during composition; it must not submit.
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    if (slashOpen) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        const n = slashMatches.length;
        setSlashIdx((slashSel + (e.key === "ArrowDown" ? 1 : -1) + n) % n);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setSlashClosed(input);
        return;
      }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) {
        e.preventDefault();
        pickSlash(slashMatches[slashSel], e.key === "Enter");
        return;
      }
    }
    if (openMention && menuMatches && menuMatches.length > 0) {
      const n = menuMatches.length;
      // Clamp like the render does: a stale index from a longer list would otherwise stall the wrap.
      const cur = Math.min(mentionActive, n - 1);
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        setMentionActive(e.key === "ArrowDown" ? (cur + 1) % n : (cur - 1 + n) % n);
        return;
      }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) {
        e.preventDefault();
        pickMention(menuMatches[cur]);
        return;
      }
    }
    if (openMention && e.key === "Escape") {
      e.preventDefault();
      setMentionDismissed(openMention.start);
      return;
    }
    // ArrowUp on the first line recalls the previous sent prompt; ArrowDown on
    // the last line steps forward and restores the draft. Popups keep their arrows.
    if (
      (e.key === "ArrowUp" || e.key === "ArrowDown") &&
      !slashOpen &&
      !openMention &&
      !e.shiftKey &&
      !e.altKey &&
      !e.ctrlKey &&
      !e.metaKey &&
      e.currentTarget.selectionStart === e.currentTarget.selectionEnd
    ) {
      const { selectionStart: at, value } = e.currentTarget;
      const up = e.key === "ArrowUp";
      const cur = recall && recall.key === draftKey ? recall : null;
      const onEdge = up ? !value.slice(0, at).includes("\n") : !value.slice(at).includes("\n");
      if (onEdge && (up || cur)) {
        e.preventDefault();
        const prompts = userPrompts();
        if (up) {
          const idx = cur ? cur.idx - 1 : prompts.length - 1;
          if (idx >= 0) {
            const next = prompts[idx];
            setRecall({ key: draftKey, idx, draft: cur ? cur.draft : input });
            if (next !== input) pendingCaret.current = next.length;
            setInput(next);
          }
        } else {
          const idx = cur!.idx + 1;
          const next = idx < prompts.length ? prompts[idx] : cur!.draft;
          setRecall(idx < prompts.length ? { key: draftKey, idx, draft: cur!.draft } : null);
          if (next !== input) pendingCaret.current = next.length;
          setInput(next);
        }
        return;
      }
    }
    // A token is one unit: Backspace/Delete at its edge removes it whole.
    const { selectionStart: pos, selectionEnd } = e.currentTarget;
    if ((e.key === "Backspace" || e.key === "Delete") && pos === selectionEnd) {
      const t = pasteTokens(input).find((tok) =>
        e.key === "Backspace" ? tok.end === pos : tok.start === pos,
      );
      if (t) {
        e.preventDefault();
        replacePasteToken(t, "");
        return;
      }
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  return (
    <div
      className={`composer${commandMode ? " command-mode" : agentMode ? " agent-mode" : ""}`}
      onDragOver={onComposerDragOver}
      onDrop={onComposerDrop}
      hidden={hidden}
    >
      {btwList && <BtwPanel entries={btwList} onClose={onCloseBtw} />}
      {slashOpen && (
        <SlashMenu
          items={slashMatches}
          index={slashSel}
          onPick={(cmd) => pickSlash(cmd, true)}
        />
      )}
      {openMention && menuMatches && (
        <MentionMenu
          matches={menuMatches}
          active={Math.min(mentionActive, Math.max(menuMatches.length - 1, 0))}
          onPick={pickMention}
          onActive={setMentionActive}
        />
      )}
      {draftAttachments.length > 0 && (
        <div className="attach-row">
          {draftAttachments.map((a) => (
            <span key={a.path} className="attach-chip" title={a.path}>
              <span className="attach-kind">{a.dir ? "folder" : "file"}</span>
              <span className="attach-name">{a.dir ? `${a.path}/` : a.path}</span>
              <button
                type="button"
                className="attach-remove"
                onClick={() => removeAttachment(a.path)}
                aria-label={`Remove ${a.path}`}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      {draftImageList.length > 0 && (
        <div className="attach-row img-row">
          {draftImageList.map((img) => (
            <span
              key={img.localId}
              className={`img-chip${img.error !== undefined ? " failed" : ""}`}
              title={img.error ?? (img.ref ? `${img.name} · ${img.ref.mime}` : img.name)}
            >
              <img src={img.previewUrl} alt={img.name} />
              <span className="img-chip-meta">
                <span className="img-chip-name">{img.name}</span>
                <span className="img-chip-state">
                  {img.uploading
                    ? "uploading…"
                    : img.error !== undefined
                      ? img.error
                      : `${img.ref?.width ?? "?"}×${img.ref?.height ?? "?"}`}
                </span>
              </span>
              <button
                type="button"
                className="attach-remove"
                onClick={() => removeDraftImage(img.localId)}
                aria-label={`Remove ${img.name}`}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      <div className="box">
        <textarea
          ref={taRef}
          rows={1}
          hidden={listening}
          onPaste={onComposerPaste}
          onClick={onComposerClick}
          value={input}
          onChange={(e) => {
            setInput(e.target.value);
            setRecall(null);
            setSlashIdx(0);
            setCaret(e.target.selectionStart);
            setMentionActive(0);
          }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
          onKeyDown={onComposerKeyDown}
        />
        {listening && (
          <VoiceRecorder
            hint={`${HOLD_MOD_LABEL} + T`}
            onStop={onVoiceStop}
            onCancel={onVoiceCancel}
          />
        )}
      </div>
      <div className="composer-foot">
        {projectName !== null && (
          <span className="ctx-chip" title={projectPath ?? undefined}>
            {projectName}
          </span>
        )}
        <button
          className={`model-btn${modelsOpen ? " open" : ""}`}
          onClick={onToggleModels}
          aria-expanded={modelsOpen}
          title="Pick the harness and model each role runs on"
        >
          <svg
            viewBox="0 0 24 24"
            width="14"
            height="14"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            aria-hidden="true"
          >
            <line x1="4" y1="7" x2="20" y2="7" />
            <circle cx="9" cy="7" r="2.2" fill="var(--panel)" />
            <line x1="4" y1="17" x2="20" y2="17" />
            <circle cx="15" cy="17" r="2.2" fill="var(--panel)" />
          </svg>
          Model config
        </button>
      </div>
    </div>
  );
});
