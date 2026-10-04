import {
  type ProviderInstanceId,
  type ProviderDriverKind,
  type ResolvedKeybindingsConfig,
} from "@t3tools/contracts";
import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { createPortal } from "react-dom";
import { XIcon } from "lucide-react";
import type { VariantProps } from "class-variance-authority";
import { Badge } from "../ui/badge";
import { buttonVariants } from "../ui/button";
import { Popover, PopoverPopup, PopoverTrigger } from "../ui/popover";
import { Tooltip, TooltipPopup, TooltipTrigger } from "../ui/tooltip";
import { cn } from "~/lib/utils";
import { ModelPickerContent } from "./ModelPickerContent";
import { ProviderInstanceIcon } from "./ProviderInstanceIcon";
import {
  ModelEsque,
  getTriggerDisplayModelLabel,
  getTriggerDisplayModelName,
} from "./providerIconUtils";
import { shouldShowInstanceBadge, type ProviderInstanceEntry } from "../../providerInstances";
import { keepsUnavailableModelSelection } from "../../providerModels";
import { ComposerControl } from "./ComposerControl";

const PANEL_MARGIN = 8;

/** Clamp a floating panel's top-left so it stays reachable on screen. */
function clampPanelPosition(element: HTMLElement | null, x: number, y: number) {
  const width = element?.offsetWidth ?? 0;
  const height = element?.offsetHeight ?? 0;
  const maxX = Math.max(PANEL_MARGIN, window.innerWidth - width - PANEL_MARGIN);
  const maxY = Math.max(PANEL_MARGIN, window.innerHeight - height - PANEL_MARGIN);
  return {
    x: Math.min(Math.max(x, PANEL_MARGIN), maxX),
    y: Math.min(Math.max(y, PANEL_MARGIN), maxY),
  };
}

export const ProviderModelPicker = memo(function ProviderModelPicker(props: {
  /**
   * The instance currently selected in the composer. Drives the trigger
   * icon, label and the default-highlighted combobox row.
   */
  activeInstanceId: ProviderInstanceId;
  model: string;
  lockedProvider: ProviderDriverKind | null;
  lockedContinuationGroupKey?: string | null;
  /** Instance entries rendered in the sidebar + used to resolve display name. */
  instanceEntries: ReadonlyArray<ProviderInstanceEntry>;
  keybindings?: ResolvedKeybindingsConfig;
  modelOptionsByInstance: ReadonlyMap<ProviderInstanceId, ReadonlyArray<ModelEsque>>;
  activeProviderIconClassName?: string;
  compact?: boolean;
  disabled?: boolean;
  terminalOpen?: boolean;
  open?: boolean;
  triggerVariant?: VariantProps<typeof buttonVariants>["variant"];
  triggerClassName?: string;
  triggerAriaLabel?: string;
  onOpenChange?: (open: boolean) => void;
  getModelDisabledReason?: (instanceId: ProviderInstanceId, model: string) => string | null;
  onInstanceModelChange: (instanceId: ProviderInstanceId, model: string) => void;
  /** Per-role model assignment for workflow providers (AgentTunnel). */
  workflowRoles?: ReadonlyArray<{
    readonly id: string;
    readonly label: string;
    readonly model: string | null;
    readonly harness: string;
    readonly variant: string | null;
    readonly auto: boolean;
  }>;
  activeWorkflowRoleId?: string | null;
  onWorkflowRoleChange?: (roleId: string | null) => void;
  onWorkflowRoleSelect?: (roleId: string, instanceId: ProviderInstanceId, model: string) => void;
  workflowHarnesses?: ReadonlyArray<{ readonly value: string; readonly label: string }>;
  onWorkflowRoleHarnessSelect?: (roleId: string, harness: string) => void;
  /** Global workflow auto mode for AgentTunnel. */
  workflowAutoMode?: boolean;
  onWorkflowAutoModeChange?: (autoMode: boolean) => void;
  /** Re-snapshots the thread's model config from the environment defaults. */
  onWorkflowModelConfigReset?: () => void;
  workflowReasoningOptions?: ReadonlyArray<{ readonly value: string; readonly label: string }>;
  onWorkflowRoleReasoningSelect?: (roleId: string, variant: string) => void;
  /** Composer uses the modal-sized picker. */
  size?: "default" | "large";
}) {
  const [uncontrolledIsMenuOpen, setUncontrolledIsMenuOpen] = useState(false);
  const isMenuOpen = props.open ?? uncontrolledIsMenuOpen;
  const isLarge = props.size === "large";

  // Resolve the active instance entry by exact routing key. The composer
  // resolves fallbacks before rendering this component; if the selected
  // instance disappears, do not infer a replacement from its driver kind.
  const activeEntry = useMemo(() => {
    return (
      props.instanceEntries.find((entry) => entry.instanceId === props.activeInstanceId) ?? null
    );
  }, [props.activeInstanceId, props.instanceEntries]);

  const activeInstanceId = props.activeInstanceId;
  const selectedInstanceOptions = props.modelOptionsByInstance.get(activeInstanceId) ?? [];
  // OpenCode can keep a model through a transient catalog refresh. Other
  // providers keep the active instance's first option as their normal fallback.
  const selectedModel =
    selectedInstanceOptions.find((option) => option.slug === props.model) ??
    (activeEntry && keepsUnavailableModelSelection(activeEntry.driverKind)
      ? undefined
      : selectedInstanceOptions[0]);
  const triggerTitle =
    activeEntry?.driverKind === "agenttunnel"
      ? "Model config"
      : selectedModel
        ? getTriggerDisplayModelName(selectedModel)
        : props.model;
  const triggerLabel =
    activeEntry?.driverKind === "agenttunnel"
      ? "Model config"
      : selectedModel
        ? `${getTriggerDisplayModelLabel(selectedModel)}${selectedModel.isUnavailable ? " (Unavailable)" : ""}`
        : props.model;
  const showInstanceBadge =
    activeEntry !== null && shouldShowInstanceBadge(activeEntry, props.instanceEntries);

  const setIsMenuOpen = (open: boolean) => {
    props.onOpenChange?.(open);
    if (props.open === undefined) {
      setUncontrolledIsMenuOpen(open);
    }
  };

  useEffect(() => {
    if (!isMenuOpen) {
      return;
    }

    const { documentElement, body } = document;
    const previousDocumentOverscrollBehavior = documentElement.style.overscrollBehavior;
    const previousBodyOverflow = body.style.overflow;
    const previousBodyPaddingRight = body.style.paddingRight;
    const scrollbarWidth = window.innerWidth - documentElement.clientWidth;

    documentElement.style.overscrollBehavior = "contain";
    body.style.overflow = "hidden";
    if (scrollbarWidth > 0) {
      body.style.paddingRight = `${scrollbarWidth}px`;
    }

    const shouldAllowOverlayScroll = (target: EventTarget | null) => {
      return target instanceof Element && target.closest("[data-model-picker-content]");
    };
    const preventBackgroundWheel = (event: WheelEvent) => {
      if (shouldAllowOverlayScroll(event.target)) {
        return;
      }
      event.preventDefault();
    };
    const preventBackgroundTouchMove = (event: TouchEvent) => {
      if (shouldAllowOverlayScroll(event.target)) {
        return;
      }
      event.preventDefault();
    };

    document.addEventListener("wheel", preventBackgroundWheel, { capture: true, passive: false });
    document.addEventListener("touchmove", preventBackgroundTouchMove, {
      capture: true,
      passive: false,
    });

    return () => {
      document.removeEventListener("wheel", preventBackgroundWheel, { capture: true });
      document.removeEventListener("touchmove", preventBackgroundTouchMove, { capture: true });
      documentElement.style.overscrollBehavior = previousDocumentOverscrollBehavior;
      body.style.overflow = previousBodyOverflow;
      body.style.paddingRight = previousBodyPaddingRight;
    };
  }, [isMenuOpen]);

  const handleInstanceModelChange = (instanceId: ProviderInstanceId, model: string) => {
    if (props.disabled) return;
    props.onInstanceModelChange(instanceId, model);
    setIsMenuOpen(false);
  };

  // Large (composer) presentation is a centered, mouse-draggable panel rather
  // than an anchored popover. Position is null until the user drags, which
  // keeps it centered on every fresh open.
  const panelRef = useRef<HTMLDivElement>(null);
  const dragOffsetRef = useRef<{ dx: number; dy: number } | null>(null);
  const [panelPosition, setPanelPosition] = useState<{ x: number; y: number } | null>(null);

  useEffect(() => {
    if (!isMenuOpen) {
      setPanelPosition(null);
    }
  }, [isMenuOpen]);

  useEffect(() => {
    if (!isLarge || !isMenuOpen) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setIsMenuOpen(false);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isLarge, isMenuOpen]);

  const handlePanelPointerDown = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if ((event.target as HTMLElement).closest("button, input, a")) return;
    const rect = panelRef.current?.getBoundingClientRect();
    if (!rect) return;
    dragOffsetRef.current = { dx: event.clientX - rect.x, dy: event.clientY - rect.y };
    event.currentTarget.setPointerCapture(event.pointerId);
  }, []);

  const handlePanelPointerMove = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    const offset = dragOffsetRef.current;
    if (!offset) return;
    setPanelPosition(
      clampPanelPosition(panelRef.current, event.clientX - offset.dx, event.clientY - offset.dy),
    );
  }, []);

  const endPanelDrag = useCallback(() => {
    dragOffsetRef.current = null;
  }, []);

  const triggerChildren = (
    <span className="flex min-w-0 flex-1 items-center gap-1.5">
      {activeEntry ? (
        <ProviderInstanceIcon
          driverKind={activeEntry.driverKind}
          displayName={activeEntry.displayName}
          accentColor={activeEntry.accentColor}
          showBadge={showInstanceBadge}
          className="size-4"
          iconClassName={cn("size-4", props.activeProviderIconClassName)}
          indicatorBackground="var(--contrast-input)"
          badgeClassName={cn(
            "right-[-0.125rem] bottom-[-0.125rem] h-3 min-w-3",
            "px-0.5 text-[.4375rem]",
          )}
        />
      ) : null}
      <Tooltip>
        <TooltipTrigger render={<span className="min-w-0 flex-1 overflow-hidden truncate" />}>
          {triggerTitle}
        </TooltipTrigger>
        <TooltipPopup side="top">{triggerLabel}</TooltipPopup>
      </Tooltip>
      {selectedModel?.isUnavailable ? (
        <Badge variant="outline" size="sm">
          Unavailable
        </Badge>
      ) : null}
    </span>
  );

  const triggerClassName = cn(
    "min-w-0 justify-between whitespace-nowrap",
    props.compact ? "max-w-42 shrink-0" : "max-w-48 shrink sm:max-w-56",
    props.triggerClassName,
  );

  const pickerContent = (
    <ModelPickerContent
      activeInstanceId={activeInstanceId}
      model={props.model}
      lockedProvider={props.lockedProvider}
      lockedContinuationGroupKey={props.lockedContinuationGroupKey ?? null}
      instanceEntries={props.instanceEntries}
      {...(props.keybindings ? { keybindings: props.keybindings } : {})}
      modelOptionsByInstance={props.modelOptionsByInstance}
      terminalOpen={props.terminalOpen ?? false}
      onRequestClose={() => setIsMenuOpen(false)}
      {...(props.getModelDisabledReason
        ? { getModelDisabledReason: props.getModelDisabledReason }
        : {})}
      {...(props.workflowRoles ? { workflowRoles: props.workflowRoles } : {})}
      {...(props.activeWorkflowRoleId != null
        ? { activeWorkflowRoleId: props.activeWorkflowRoleId }
        : {})}
      {...(props.onWorkflowRoleChange ? { onWorkflowRoleChange: props.onWorkflowRoleChange } : {})}
      {...(props.onWorkflowRoleSelect
        ? { onWorkflowRoleModelSelect: props.onWorkflowRoleSelect }
        : {})}
      {...(props.workflowHarnesses ? { workflowHarnesses: props.workflowHarnesses } : {})}
      {...(props.onWorkflowRoleHarnessSelect
        ? { onWorkflowRoleHarnessSelect: props.onWorkflowRoleHarnessSelect }
        : {})}
      {...(props.workflowAutoMode !== undefined
        ? { workflowAutoMode: props.workflowAutoMode }
        : {})}
      {...(props.onWorkflowAutoModeChange
        ? { onWorkflowAutoModeChange: props.onWorkflowAutoModeChange }
        : {})}
      {...(props.onWorkflowModelConfigReset
        ? { onWorkflowModelConfigReset: props.onWorkflowModelConfigReset }
        : {})}
      {...(props.workflowReasoningOptions
        ? { workflowReasoningOptions: props.workflowReasoningOptions }
        : {})}
      {...(props.onWorkflowRoleReasoningSelect
        ? { onWorkflowRoleReasoningSelect: props.onWorkflowRoleReasoningSelect }
        : {})}
      {...(props.size ? { size: props.size } : {})}
      onInstanceModelChange={handleInstanceModelChange}
    />
  );

  if (isLarge) {
    return (
      <>
        <ComposerControl
          aria-label={props.triggerAriaLabel}
          aria-expanded={isMenuOpen}
          variant={props.triggerVariant ?? "ghost"}
          data-chat-provider-model-picker="true"
          className={triggerClassName}
          disabled={props.disabled}
          onClick={() => setIsMenuOpen(!isMenuOpen)}
        >
          {triggerChildren}
        </ComposerControl>
        {isMenuOpen
          ? createPortal(
              <>
                <div
                  className="fixed inset-0 z-[139] bg-black/25"
                  onMouseDown={() => setIsMenuOpen(false)}
                  aria-hidden
                />
                <div
                  ref={panelRef}
                  role="dialog"
                  aria-label="Model config"
                  data-model-picker-panel="true"
                  className={cn(
                    "dropdown-glass fixed z-[140] flex max-h-[100dvh] flex-col overflow-hidden rounded-xl border text-popover-foreground shadow-[var(--elev-overlay)]",
                    panelPosition === null && "left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2",
                  )}
                  style={
                    panelPosition ? { left: panelPosition.x, top: panelPosition.y } : undefined
                  }
                >
                  <div
                    className="flex h-8 shrink-0 cursor-grab touch-none select-none items-center gap-2 border-b border-border/70 px-3 active:cursor-grabbing"
                    onPointerCancel={endPanelDrag}
                    onPointerDown={handlePanelPointerDown}
                    onPointerMove={handlePanelPointerMove}
                    onPointerUp={endPanelDrag}
                  >
                    <span className="text-xs font-medium text-muted-foreground">Model config</span>
                    <span className="text-[.625rem] text-muted-foreground/50">drag to move</span>
                    <button
                      type="button"
                      className="ms-auto inline-flex size-5 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent/50 hover:text-foreground"
                      onClick={() => setIsMenuOpen(false)}
                      aria-label="Close model config"
                    >
                      <XIcon className="size-3.5" />
                    </button>
                  </div>
                  {pickerContent}
                </div>
              </>,
              document.body,
            )
          : null}
      </>
    );
  }

  return (
    <Popover
      open={isMenuOpen}
      modal
      onOpenChange={(open) => {
        if (props.disabled) {
          setIsMenuOpen(false);
          return;
        }
        setIsMenuOpen(open);
      }}
    >
      <PopoverTrigger
        render={
          <ComposerControl
            aria-label={props.triggerAriaLabel}
            variant={props.triggerVariant ?? "ghost"}
            data-chat-provider-model-picker="true"
            className={triggerClassName}
            disabled={props.disabled}
          />
        }
      >
        {triggerChildren}
      </PopoverTrigger>
      <PopoverPopup
        align="start"
        className="before:hidden [--viewport-inline-padding:0]"
        viewportClassName="!overflow-hidden rounded-[calc(var(--radius-lg)-1px)] p-0 [clip-path:inset(0_round_calc(var(--radius-lg)-1px))]"
      >
        {pickerContent}
      </PopoverPopup>
    </Popover>
  );
});
