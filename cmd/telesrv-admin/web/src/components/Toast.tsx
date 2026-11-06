import { Check, CheckCircle2, CircleAlert, Copy, X } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useI18n } from "../i18n";

// Toasts replace the old "type a reason, run a dry run, press confirm" dance.
// Every action now reports its outcome here: green on success, red on failure,
// and a failure carries a copyable error code so an operator can paste it into
// a bug report instead of re-typing the message.
export type ToastTone = "success" | "error";

export type ToastInput = {
  tone: ToastTone;
  message: string;
  code?: string;
};

type ToastItem = ToastInput & { id: number };

// The queue lives at module scope so a helper that is not a component -- a
// page-level handler, say -- can report without threading a context through.
// The provider only renders; it never owns the list.
let sequence = 0;
const listeners = new Set<(item: ToastItem) => void>();

function emit(input: ToastInput): void {
  const item: ToastItem = { ...input, id: ++sequence };
  for (const listener of listeners) {
    listener(item);
  }
}

export const toast = {
  success(message: string): void {
    emit({ tone: "success", message });
  },
  error(message: string, code?: string): void {
    emit({ tone: "error", message, code: code?.trim() ? code.trim() : undefined });
  }
};

const successTimeout = 4500;
const maxVisible = 4;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);

  useEffect(() => {
    const listener = (item: ToastItem) => {
      setItems((current) => [...current, item].slice(-maxVisible));
      // A success message is disposable: it fades on its own. A failure stays
      // until it is dismissed, because it holds a code the operator may still
      // need to copy.
      if (item.tone === "success") {
        window.setTimeout(() => {
          setItems((current) => current.filter((entry) => entry.id !== item.id));
        }, successTimeout);
      }
    };
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);

  function dismiss(id: number) {
    setItems((current) => current.filter((entry) => entry.id !== id));
  }

  return (
    <>
      {children}
      {items.length > 0 && createPortal(
        <div className="toast-stack" role="status" aria-live="polite">
          {items.map((item) => (
            <ToastCard key={item.id} item={item} onDismiss={() => dismiss(item.id)} />
          ))}
        </div>,
        document.body
      )}
    </>
  );
}

function ToastCard({ item, onDismiss }: { item: ToastItem; onDismiss: () => void }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);

  async function copyCode() {
    if (!item.code) return;
    try {
      await navigator.clipboard.writeText(item.code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className={`toast ${item.tone}`}>
      <div className="toast-icon">
        {item.tone === "success" ? <CheckCircle2 size={18} /> : <CircleAlert size={18} />}
      </div>
      <div className="toast-body">
        <div className="toast-message">{item.message}</div>
        {item.tone === "error" && item.code && (
          <div className="toast-code">
            <span className="toast-code-label">{t("toast.errorCode")}</span>
            <code className="toast-code-value">{item.code}</code>
            <button className="toast-copy" type="button" onClick={() => void copyCode()} title={t("common.copy")}>
              {copied ? <Check size={13} /> : <Copy size={13} />}
            </button>
          </div>
        )}
      </div>
      <button className="toast-close" type="button" onClick={onDismiss} aria-label={t("common.close")}>
        <X size={14} />
      </button>
    </div>
  );
}
