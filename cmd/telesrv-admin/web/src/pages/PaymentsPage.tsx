import { useEffect, useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { api } from "../api";

type Invoice = {
  id: number;
  bot_id: number;
  amount: number;
  currency: string;
  status: string;
  title: string;
  created_at: string;
  paid_at?: string | null;
};

// Every colour here comes from the admin theme tokens (01-foundation.css) so
// the form reads the same in light and dark mode. Hardcoding white overlays --
// the previous version did that -- makes the inputs disappear on the light
// background, which is exactly what the payment credentials used to do.
const panel: React.CSSProperties = {
  border: "1px solid var(--line)",
  background: "var(--panel)",
  borderRadius: 14,
  padding: 20,
  marginBottom: 18,
  boxShadow: "var(--shadow-sm)",
};

const hint: React.CSSProperties = {
  color: "var(--muted)",
  fontSize: 13,
  margin: "2px 0 14px",
  lineHeight: 1.5,
};

const secretRow: React.CSSProperties = { display: "flex", gap: 8, alignItems: "center" };

const eyeButton: React.CSSProperties = {
  flex: "0 0 auto",
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
  height: 34,
  padding: "0 12px",
  borderRadius: "var(--radius-sm)",
  border: "1px solid var(--line-strong)",
  background: "var(--btn-bg)",
  color: "var(--btn-text)",
  fontSize: 13,
  fontWeight: 600,
  cursor: "pointer",
};

export function PaymentsPage() {
  const [wallet, setWallet] = useState("");
  const [secret, setSecret] = useState("");
  const [secretSet, setSecretSet] = useState(false);
  const [showSecret, setShowSecret] = useState(false);
  const [plategaMerchant, setPlategaMerchant] = useState("");
  const [plategaSecret, setPlategaSecret] = useState("");
  const [plategaSecretSet, setPlategaSecretSet] = useState(false);
  const [showPlategaSecret, setShowPlategaSecret] = useState(false);
  const [invoices, setInvoices] = useState<Invoice[]>([]);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);

  async function load() {
    setError("");
    try {
      const settings = await api.paymentSettings();
      setWallet(settings.wallet || "");
      setSecretSet(Boolean(settings.secret_set));
      setPlategaMerchant(settings.platega_merchant || "");
      setPlategaSecretSet(Boolean(settings.platega_secret_set));
      const data = await api.paymentInvoices();
      setInvoices(data.invoices || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function save() {
    setBusy(true);
    setError("");
    setSaved("");
    try {
      await api.setPaymentSettings({
        wallet,
        secret,
        platega_merchant: plategaMerchant,
        platega_secret: plategaSecret
      });
      setSecret("");
      setPlategaSecret("");
      setSaved("Сохранено");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  return (
    <div>
      <h1 style={{ marginTop: 0 }}>Платежи</h1>
      <p style={{ color: "var(--muted)", marginTop: -6 }}>
        ЮMoney и Platega: реквизиты провайдеров и последние инвойсы.
      </p>

      {error && (
        <div style={{ ...panel, borderColor: "var(--danger-border)", background: "var(--danger-tint)", color: "var(--danger-text)" }}>
          {error}
        </div>
      )}
      {saved && (
        <div style={{ ...panel, borderColor: "var(--good-border)", background: "var(--good-tint)", color: "var(--good)" }}>
          {saved}
        </div>
      )}

      <div style={panel}>
        <h2 style={{ marginTop: 0, fontSize: 17 }}>ЮMoney</h2>
        <p style={hint}>
          Кошелёк принимает переводы по кнопке, секрет проверяет подпись HTTP-уведомлений.
        </p>
        <div className="form-stack">
          <label>
            <span>Кошелёк</span>
            <input
              type="text"
              value={wallet}
              placeholder="4100118920936988"
              spellCheck={false}
              autoComplete="off"
              onChange={(e) => setWallet(e.target.value)}
            />
          </label>
          <label>
            <span>{secretSet ? "Секрет уведомлений (сохранён)" : "Секрет уведомлений"}</span>
            <div style={secretRow}>
              <input
                style={{ flex: 1, width: "auto" }}
                type={showSecret ? "text" : "password"}
                value={secret}
                placeholder={secretSet ? "••••••••" : "секрет из ЮMoney"}
                spellCheck={false}
                autoComplete="new-password"
                onChange={(e) => setSecret(e.target.value)}
              />
              <button
                style={eyeButton}
                type="button"
                aria-label={showSecret ? "Скрыть секрет" : "Показать секрет"}
                onClick={() => setShowSecret((v) => !v)}
              >
                {showSecret ? <EyeOff size={15} /> : <Eye size={15} />}
                <span>{showSecret ? "Скрыть" : "Показать"}</span>
              </button>
            </div>
          </label>
        </div>
      </div>

      <div style={panel}>
        <h2 style={{ marginTop: 0, fontSize: 17 }}>Platega</h2>
        <p style={hint}>merchantId и секрет из личного кабинета Platega.</p>
        <div className="form-stack">
          <label>
            <span>merchantId</span>
            <input
              type="text"
              value={plategaMerchant}
              placeholder="merchant id"
              spellCheck={false}
              autoComplete="off"
              onChange={(e) => setPlategaMerchant(e.target.value)}
            />
          </label>
          <label>
            <span>{plategaSecretSet ? "Секрет (сохранён)" : "Секрет"}</span>
            <div style={secretRow}>
              <input
                style={{ flex: 1, width: "auto" }}
                type={showPlategaSecret ? "text" : "password"}
                value={plategaSecret}
                placeholder={plategaSecretSet ? "••••••••" : "secret"}
                spellCheck={false}
                autoComplete="new-password"
                onChange={(e) => setPlategaSecret(e.target.value)}
              />
              <button
                style={eyeButton}
                type="button"
                aria-label={showPlategaSecret ? "Скрыть секрет" : "Показать секрет"}
                onClick={() => setShowPlategaSecret((v) => !v)}
              >
                {showPlategaSecret ? <EyeOff size={15} /> : <Eye size={15} />}
                <span>{showPlategaSecret ? "Скрыть" : "Показать"}</span>
              </button>
            </div>
          </label>
        </div>
      </div>

      <button className="btn primary" type="button" disabled={busy} onClick={() => void save()}>
        {busy ? "Сохраняю..." : "Сохранить"}
      </button>

      <h2 style={{ fontSize: 17, marginTop: 30 }}>Последние инвойсы</h2>
      <div style={{ overflowX: "auto", ...panel, padding: 0 }}>
        <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 14 }}>
          <thead>
            <tr style={{ textAlign: "left", color: "var(--muted)" }}>
              <th style={{ padding: "10px 12px", fontWeight: 700 }}>ID</th>
              <th style={{ padding: "10px 12px", fontWeight: 700 }}>Бот</th>
              <th style={{ padding: "10px 12px", fontWeight: 700 }}>Сумма</th>
              <th style={{ padding: "10px 12px", fontWeight: 700 }}>Статус</th>
              <th style={{ padding: "10px 12px", fontWeight: 700 }}>Создан</th>
              <th style={{ padding: "10px 12px", fontWeight: 700 }}>Оплачен</th>
            </tr>
          </thead>
          <tbody>
            {invoices.map((invoice) => (
              <tr key={invoice.id} style={{ borderTop: "1px solid var(--line)" }}>
                <td style={{ padding: "10px 12px" }}>{invoice.id}</td>
                <td style={{ padding: "10px 12px" }}>{invoice.bot_id}</td>
                <td style={{ padding: "10px 12px" }}>{(invoice.amount / 100).toFixed(2)} {invoice.currency}</td>
                <td style={{ padding: "10px 12px" }}>{invoice.status}</td>
                <td style={{ padding: "10px 12px" }}>{invoice.created_at ? new Date(invoice.created_at).toLocaleString() : "—"}</td>
                <td style={{ padding: "10px 12px" }}>{invoice.paid_at ? new Date(invoice.paid_at).toLocaleString() : "—"}</td>
              </tr>
            ))}
            {invoices.length === 0 && (
              <tr>
                <td colSpan={6} style={{ padding: "16px 12px", color: "var(--muted)" }}>Платежей пока нет</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
