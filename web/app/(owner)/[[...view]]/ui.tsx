"use client";

import { createContext, useCallback, useContext, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";

type ConfirmOptions = { title?: string; message: ReactNode; confirm?: string; danger?: boolean };
type ToastKind = "success" | "error";
type Toast = { id: number; kind: ToastKind; message: string };
type Pending = ConfirmOptions & { resolve: (accepted: boolean) => void };

type Ui = {
  confirm: (options: ConfirmOptions) => Promise<boolean>;
  toast: (message: string, kind?: ToastKind) => void;
};

const Context = createContext<Ui | null>(null);

function useUi() {
  const ui = useContext(Context);
  if (!ui) throw new Error("useUi needs UiProvider");
  return ui;
}

export function useConfirm() {
  return useUi().confirm;
}

export function useToast() {
  return useUi().toast;
}

export function useAction() {
  const toast = useToast();
  const [busy, setBusy] = useState("");
  const run = useCallback(async (key: string, work: () => Promise<unknown>, success: string) => {
    setBusy(key);
    try {
      await work();
      toast(success);
      return true;
    } catch (cause) {
      toast(cause instanceof Error ? cause.message : "Request failed.", "error");
      return false;
    } finally {
      setBusy("");
    }
  }, [toast]);
  return { busy, run };
}

function ConfirmDialog({ pending, answer }: { pending: Pending; answer: (accepted: boolean) => void }) {
  const titleID = useId();
  const bodyID = useId();
  const dialog = useRef<HTMLDivElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const reply = useRef(answer);

  useEffect(() => {
    reply.current = answer;
  });

  useEffect(() => {
    const returnFocus = document.activeElement;
    cancel.current?.focus();
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.stopPropagation();
        reply.current(false);
      } else if (event.key === "Tab" && dialog.current) {
        const controls = dialog.current.querySelectorAll<HTMLElement>("button:not(:disabled)");
        const first = controls[0];
        const last = controls[controls.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    }
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      if (returnFocus instanceof HTMLElement) returnFocus.focus();
    };
  }, []);

  return <div className="dialog-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) answer(false); }}>
    <div role="alertdialog" aria-modal="true" aria-labelledby={titleID} aria-describedby={bodyID} ref={dialog} className="panel stack confirm-dialog">
      <h2 id={titleID}>{pending.title ?? "Are you sure?"}</h2>
      <div id={bodyID} className="preserve">{pending.message}</div>
      <div className="actions">
        <button type="button" ref={cancel} onClick={() => answer(false)}>Cancel</button>
        <button type="button" className={pending.danger === false ? "primary" : "danger"} onClick={() => answer(true)}>{pending.confirm ?? "Confirm"}</button>
      </div>
    </div>
  </div>;
}

function Toasts({ toasts, dismiss }: { toasts: Toast[]; dismiss: (id: number) => void }) {
  if (!toasts.length) return null;
  return <div className="toasts" role="region" aria-label="Notifications">
    {toasts.map((toast) => <div key={toast.id} className={`toast ${toast.kind}`} role={toast.kind === "error" ? "alert" : "status"}>
      <span>{toast.message}</span>
      <button type="button" className="text-button" onClick={() => dismiss(toast.id)} aria-label="Dismiss notification">Dismiss</button>
    </div>)}
  </div>;
}

export function UiProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(0);

  const confirm = useCallback((options: ConfirmOptions) => new Promise<boolean>((resolve) => {
    setPending((current) => {
      current?.resolve(false);
      return { ...options, resolve };
    });
  }), []);

  const dismiss = useCallback((id: number) => setToasts((current) => current.filter((toast) => toast.id !== id)), []);

  const toast = useCallback((message: string, kind: ToastKind = "success") => {
    const id = ++next.current;
    setToasts((current) => [...current.slice(-3), { id, kind, message }]);
    window.setTimeout(() => dismiss(id), kind === "error" ? 10_000 : 5_000);
  }, [dismiss]);

  const value = useMemo(() => ({ confirm, toast }), [confirm, toast]);

  function answer(accepted: boolean) {
    pending?.resolve(accepted);
    setPending(null);
  }

  return <Context.Provider value={value}>
    {children}
    {pending && <ConfirmDialog pending={pending} answer={answer} />}
    <Toasts toasts={toasts} dismiss={dismiss} />
  </Context.Provider>;
}
