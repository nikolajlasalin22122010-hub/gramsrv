import { Check, Copy, Loader2, X } from "lucide-react";
import type { ReactNode } from "react";
import { useState } from "react";
import { createPortal } from "react-dom";
import { api, errorCode, errorMessage } from "../api";
import { useI18n } from "../i18n";
import type { CommandResult } from "../types";
import { toast } from "./Toast";

type ActionTone = "neutral" | "warn" | "danger" | "primary";

// ActionButton runs one audited command in a single click.
//
// The panel used to open a modal that asked why, ran a dry run and waited for a
// second confirmation. That is three steps for every ban, every refund, every
// balance fix, and operators did it dozens of times a day. The audit trail still
// needs a reason, so one is generated here instead of typed: the command name
// and the acting screen are enough to reconstruct intent, and the dry run is
// kept as a hidden pre-flight so a command that would be rejected is rejected
// before it is committed, not after.
//
// The outcome is reported as a toast -- green on success, red on failure with a
// copyable error code -- so nothing has to be read inside a dialog.
export function ActionButton({
  label,
  path,
  payload,
  icon,
  compact = false,
  tone = "danger",
  disabled = false,
  onDone,
  onError,
  secretField
}: {
  label: string;
  path: string;
  payload: () => Record<string, unknown>;
  icon?: ReactNode;
  compact?: boolean;
  tone?: ActionTone;
  // disabled keeps a form from firing while its own validation is unhappy, so
  // the operator fixes the field instead of reading a backend rejection.
  disabled?: boolean;
  onDone?: () => void;
  // onError lets a page replace the raw backend text with an explanation for a
  // failure the operator cannot fix by editing the form -- an optimistic-locking
  // 409, say.
  onError?: (error: unknown) => string | undefined;
  // A one-time secret (a bot token) cannot be toasted: it has to be readable and
  // copyable until dismissed, so that one result keeps a small dialog.
  secretField?: string;
}) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState("");
  const [secretCopied, setSecretCopied] = useState(false);

  function applied(result: CommandResult) {
    toast.success(result.message || label);
    if (secretField && result.details && typeof result.details[secretField] === "string") {
      setSecretCopied(false);
      setSecret(result.details[secretField] as string);
    }
    onDone?.();
  }

  async function execute() {
    if (busy) return;
    setBusy(true);
    try {
      // One payload snapshot for both calls: a handler that mints a value per
      // call would otherwise commit a different request than it previewed, and
      // the runner refuses a command id reused for a different body.
      const base = payload();
      const reason = `admin panel: ${label}`;
      const dry = await api.action(path, { ...base, reason, confirm: false, command_id: "" });
      if (dry.error) {
        toast.error(dry.message || dry.error, dry.command_id ? `${dry.error} (${dry.command_id})` : dry.error);
        return;
      }
      // A handler that ignores dry_run has already done the work; committing
      // again would apply it twice.
      if (!dry.dry_run) {
        applied(dry);
        return;
      }
      const commit = await api.action(path, { ...base, reason, confirm: true, command_id: dry.command_id });
      if (commit.error) {
        toast.error(commit.message || commit.error, commit.error);
        return;
      }
      applied(commit);
    } catch (err) {
      toast.error(onError?.(err) || errorMessage(err), errorCode(err));
    } finally {
      setBusy(false);
    }
  }

  const triggerClass = `btn ${tone === "danger" ? "danger" : tone === "warn" ? "warn" : tone === "primary" ? "primary" : ""} ${compact ? "compact-btn" : ""}`;

  async function copySecret() {
    try {
      await navigator.clipboard.writeText(secret);
      setSecretCopied(true);
    } catch {
      setSecretCopied(false);
    }
  }

  return (
    <>
      <button
        className={triggerClass}
        type="button"
        disabled={disabled || busy}
        onClick={() => void execute()}
      >
        {busy ? <Loader2 size={15} className="spin" /> : icon}
        {label}
      </button>
      {secret && createPortal(
        <div className="modal-backdrop" role="presentation">
          <section className="modal" role="dialog" aria-modal="true" aria-label={label}>
            <div className="modal-head">
              <div>
                <div className="eyebrow">{label}</div>
                <h2>{t("action.oneTimeSecretTitle")}</h2>
              </div>
              <button className="icon-btn" type="button" onClick={() => setSecret("")} aria-label={t("action.close")}>
                <X size={15} />
              </button>
            </div>
            <div className="command-body">
              <div className="secret-reveal">
                <div className="secret-reveal-label">{t("action.oneTimeSecret")}</div>
                <div className="secret-reveal-row">
                  <code className="secret-reveal-value">{secret}</code>
                  <button className="btn icon-text" type="button" onClick={() => void copySecret()}>
                    {secretCopied ? <Check size={15} /> : <Copy size={15} />}
                    {secretCopied ? t("common.copied") : t("common.copy")}
                  </button>
                </div>
              </div>
            </div>
            <div className="modal-actions">
              <button className="btn" type="button" onClick={() => setSecret("")}>{t("common.close")}</button>
            </div>
          </section>
        </div>,
        document.body
      )}
    </>
  );
}
