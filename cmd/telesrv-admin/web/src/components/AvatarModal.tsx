import { ImagePlus, Loader2, Upload, X } from "lucide-react";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { api, errorCode, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { Alert } from "./ui";
import { toast } from "./Toast";

export function AvatarModal({ kind, id, onClose, onDone }: {
  kind: "user" | "channel";
  id: number;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [previewURL, setPreviewURL] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!file) {
      setPreviewURL("");
      return;
    }
    const url = URL.createObjectURL(file);
    setPreviewURL(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);

  async function submit() {
    if (!file) {
      setError(t("avatar.chooseFirst"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      const form = new FormData();
      const idField = kind === "channel" ? "channel_id" : "user_id";
      // The audit reason is generated rather than typed, like every other
      // command in the panel now.
      const reason = `admin panel: ${t("avatar.change")} ${idField} ${id}`;
      form.set("metadata", JSON.stringify({ command_id: "", reason, confirm: true, [idField]: id }));
      form.set("file", file, file.name);
      const result = kind === "channel" ? await api.setChannelAvatar(form) : await api.setAccountAvatar(form);
      if (result.error) {
        setError(result.error);
        toast.error(result.error, result.error);
        return;
      }
      toast.success(result.message || t("avatar.upload"));
      onDone();
      onClose();
    } catch (err) {
      const message = errorMessage(err);
      setError(message);
      toast.error(message, errorCode(err));
    } finally {
      setBusy(false);
    }
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label={t("avatar.change")}>
        <div className="modal-head">
          <div><div className="eyebrow">{kind === "channel" ? t("common.channel") : t("common.account")}</div><h2>{t("avatar.change")}</h2></div>
          <button className="icon-btn" type="button" onClick={onClose} disabled={busy} aria-label={t("common.close")}><X size={15} /></button>
        </div>
        <div className="command-body">
          <label className={`gift-file-picker ${file ? "has-file" : ""}`}>
            <input type="file" accept="image/png,image/jpeg,image/webp" onChange={(event) => setFile(event.target.files?.[0] ?? null)} />
            {previewURL ? <img className="gift-file-icon" src={previewURL} alt="" style={{ objectFit: "cover" }} /> : <ImagePlus size={22} />}
            <span className="gift-file-copy"><span className="gift-field-label">{t("avatar.new")}</span><strong>{file ? file.name : t("avatar.chooseImage")}</strong></span>
            <span className="gift-file-action">{file ? t("common.changeFile") : t("common.chooseFile")}</span>
          </label>
          {error && <Alert>{error}</Alert>}
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose} disabled={busy}>{t("common.close")}</button>
          <button className="btn primary icon-text" type="button" onClick={() => void submit()} disabled={busy}>
            {busy ? <Loader2 className="spin" size={15} /> : <Upload size={15} />} {t("avatar.upload")}
          </button>
        </div>
      </section>
    </div>,
    document.body
  );
}
