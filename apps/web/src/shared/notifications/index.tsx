"use client";

import { useLayoutEffect, useRef, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import toast, {
  ToastBar,
  useToaster,
  type Toast,
  type DefaultToastOptions,
} from "react-hot-toast";
import { Icon } from "@/shared/ui";

const dialogs: HTMLDialogElement[] = [];
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((listener) => listener());

export function registerNotificationDialog(dialog: HTMLDialogElement) {
  dialogs.push(dialog);
  emit();
  const unregister = () => {
    const index = dialogs.lastIndexOf(dialog);
    if (index !== -1) dialogs.splice(index, 1);
    emit();
  };
  // A close event queued before StrictMode reopens the modal is stale.
  const closed = () => {
    if (!dialog.open) unregister();
  };
  dialog.addEventListener("close", closed);
  return () => {
    dialog.removeEventListener("close", closed);
    unregister();
  };
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
function host() {
  return (
    dialogs.findLast((dialog) => dialog.isConnected && dialog.open) ??
    document.body
  );
}
const serverHost = () => null;

// Independent actions receive independent IDs. An explicit ID belongs to one action/object.
// Only event handlers call this API; rendering and polling do not announce actions.
export const notify = {
  success: (message: string, id?: string) =>
    toast.success(message, { id, duration: 4000 }),
  error: (message: string, id?: string) =>
    toast.error(message, { id, duration: 6000 }),
};

function NotificationLayer({
  target,
  children,
}: {
  target: HTMLElement;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    // A popover escapes a transformed/scrolling dialog and is raised after it.
    // Remaining a descendant of the modal keeps its close button interactive.
    if (typeof element.showPopover === "function") {
      element.showPopover();
      return () => {
        if (element.matches(":popover-open")) element.hidePopover();
      };
    }
    target.classList.add("notification-fallback-host");
    return () => target.classList.remove("notification-fallback-host");
  }, [target]);
  return (
    <div ref={ref} popover="manual" className="notification-layer">
      {children}
    </div>
  );
}

const options: DefaultToastOptions = {
  className: "notification-toast",
  success: {
    duration: 4000,
    ariaProps: { role: "status", "aria-live": "polite" },
  },
  error: {
    duration: 6000,
    ariaProps: { role: "alert", "aria-live": "assertive" },
  },
};

function NotificationItem({
  item,
  offset,
  updateHeight,
}: {
  item: Toast;
  offset: number;
  updateHeight: (id: string, height: number) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const measure = () =>
      updateHeight(item.id, element.getBoundingClientRect().height);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [item.id, updateHeight]);
  return (
    <div
      ref={ref}
      className="notification-item"
      style={{ transform: `translateY(${offset}px)` }}
    >
      <ToastBar
        toast={item}
        position="top-center"
        style={{ pointerEvents: item.visible ? "auto" : "none" }}
      >
        {({ icon, message }) => (
          <>
            {icon}
            {message}
            <button
              type="button"
              className="notification-close"
              aria-label="Закрыть уведомление"
              onClick={() => toast.dismiss(item.id)}
            >
              <Icon name="close" size={18} />
            </button>
          </>
        )}
      </ToastBar>
    </div>
  );
}

export function NotificationToaster() {
  const target = useSyncExternalStore(subscribe, host, serverHost);
  // One stable owner retains timers while only the visual portal changes hosts.
  const {
    toasts,
    handlers: {
      startPause,
      endPause: finishPause,
      updateHeight,
      calculateOffset,
    },
  } = useToaster(options);
  const previousTarget = useRef<HTMLElement | null>(null);
  const endPause = useRef(finishPause);
  useLayoutEffect(() => {
    endPause.current = finishPause;
    if (previousTarget.current !== target) {
      previousTarget.current = target;
      finishPause();
    }
  }, [target, finishPause]);
  useLayoutEffect(() => () => endPause.current(), []);
  if (!target) return null;
  return createPortal(
    <NotificationLayer target={target}>
      <div
        className="notification-toaster"
        onMouseEnter={startPause}
        onMouseLeave={finishPause}
      >
        {toasts.map((item) => (
          <NotificationItem
            key={item.id}
            item={item}
            updateHeight={updateHeight}
            offset={calculateOffset(item, {
              gutter: 8,
              defaultPosition: "top-center",
            })}
          />
        ))}
      </div>
    </NotificationLayer>,
    target,
  );
}
