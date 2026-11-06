import { CircleAlert, Loader2, Play, X } from "lucide-react";
import { type ReactNode, useState } from "react";
import { createPortal } from "react-dom";
import { APIError, api, errorCode, errorMessage } from "../api";
import { useI18n } from "../i18n";
import type { CommandResult } from "../types";
import { toast } from "./Toast";

// CommandFlow is the shell for commands that need a *form* -- an uploaded .zip,
// a wallet binding -- as opposed to a single button.
//
// It used to be the reason -> dry run -> confirm wizard, which meant every form
// command cost the operator three extra interactions before anything happened.
// The reason is now generated ("admin panel: <label>"), the dry run is an
// invisible pre-flight, and the outcome is reported as a toast. What is left is
// the form itself plus, for commands that return a structured report (the gift
// pack importer returns a per-gift table), a result block.
export function CommandFlow({
  path,
  label,
  eyebrow,
  children,
  payload,
  onApplied,
  onResult,
  onClose,
  disabled = false,
  extra,
  form
}: {
  path: string;
  label: string;
  eyebrow?: string;
  children?: ReactNode;
  // The request body for the JSON path. A form command builds its body from
  // `form` instead and never calls this.
  payload: () => Record<string, unknown>;
  onApplied?: () => void;
  onResult?: (result: CommandResult) => void;
  onClose: () => void;
  disabled?: boolean;
  extra?: ReactNode;
  // Form commands (an uploaded .zip, a directory of animation frames) post
  // multipart rather than JSON.
  form?: (fields: { reason: string; confirm: boolean; command_id: string }) => FormData;
}) {
  const { t } = useI18n();
  const [result, setResult] = useState<CommandResult | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function send(fields: { reason: string; confirm: boolean; command_id: string }) {
    const body: FormData | Record<string, unknown> = form
      ? form(fields)
      : { ...payload(), ...fields };
    return api.action(path, body);
  }

  function applied(commandResult: CommandResult) {
    onResult?.(commandResult);
    setResult(commandResult);
    toast.success(commandResult.message || label);
    onApplied?.();
  }

  async function execute() {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const reason = `admin panel: ${label}`;
      const dry = await send({ reason, confirm: false, command_id: "" });
      if (dry.error) {
        setError(dry.message || dry.error);
        toast.error(dry.message || dry.error, dry.error);
        return;
      }
      if (!dry.dry_run) {
        applied(dry);
        return;
      }
      const commit = await send({ reason, confirm: true, command_id: dry.command_id });
      if (commit.error) {
        setError(commit.message || commit.error);
        toast.error(commit.message || commit.error, commit.error);
        return;
      }
      applied(commit);
    } catch (err) {
      const message = errorMessage(err);
      setError(message);
      toast.error(message, errorCode(err));
      // A command that reports a structured outcome can also fail as a
      // transport error carrying that outcome in its body -- the gift pack
      // importer does exactly this when some of the archive's gifts are bad.
      if (err instanceof APIError && err.result && typeof err.result === "object") {
        const carried = err.result as CommandResult;
        if (typeof carried.message === "string" || carried.details) {
          setResult(carried);
          onResult?.(carried);
        }
      }
    } finally {
      setBusy(false);
    }
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={label}>
        <div className="modal-head">
          <div>
            <div className="eyebrow">{eyebrow ?? t("action.flow")}</div>
            <h2>{label}</h2>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("action.close")}>
            <X size={15} />
          </button>
        </div>
        <div className="command-body">
          {children}
          {extra}
          {error && (
            <div className="alert">
              <CircleAlert size={16} /> <span>{error}</span>
            </div>
          )}
          {result?.details && Object.keys(result.details).length > 0 && (
            <pre className="json-block">{JSON.stringify(result.details, null, 2)}</pre>
          )}
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
          <button className="btn primary icon-text" type="button" onClick={() => void execute()} disabled={busy || disabled}>
            {busy ? <Loader2 size={15} className="spin" /> : <Play size={15} />}
            {busy ? t("action.running") : t("action.run")}
          </button>
        </div>
      </section>
    </div>,
    document.body
  );
}
