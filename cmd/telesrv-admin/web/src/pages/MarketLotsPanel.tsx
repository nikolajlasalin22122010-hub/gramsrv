import { RefreshCw, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api, errorMessage } from "../api";
import { Alert, Badge, SectionHead } from "../components/ui";
import type { MarketLotRow } from "../types";

// MarketLotsPanel shows what is currently listed on the marketplace and lets the
// operator take a lot down. It reads the market's own list, so it stays correct
// even when a lot was created through the operator API by hand.
export function MarketLotsPanel({ kind }: { kind: "username" | "number" | "gift" }) {
  const [lots, setLots] = useState<MarketLotRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function load() {
    setBusy(true);
    setError("");
    try {
      const result = await api.marketLots(kind);
      setLots(result.lots ?? []);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, [kind]);

  async function remove(row: MarketLotRow) {
    if (!window.confirm(`Снять с маркета ${label(row)}?`)) return;
    setBusy(true);
    setError("");
    try {
      await api.marketLotDelete(Number(row.id));
      await load();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <section className="section-block">
      <SectionHead title="Лоты на маркете" text="Всё, что сейчас выставлено на продажу." />
      {error && <Alert>{error}</Alert>}
      {lots.length === 0 ? (
        <p className="bot-create-note">Пока пусто.</p>
      ) : (
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>Лот</th>
                <th>Цена, GRAM</th>
                <th>Статус</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {lots.map((row) => (
                <tr key={row.id}>
                  <td className="mono">
                    <strong>{label(row)}</strong>
                  </td>
                  <td className="mono">{row.price}</td>
                  <td>
                    <Badge tone={row.status === "sale" ? "good" : "warn"}>{row.status}</Badge>
                  </td>
                  <td>
                    <button className="row-link" type="button" disabled={busy} onClick={() => remove(row)}>
                      <X size={14} /> Снять
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="toolbar">
        <button className="btn icon-text" type="button" onClick={load} disabled={busy}>
          <RefreshCw size={15} className={busy ? "spin" : ""} /> Обновить
        </button>
      </div>
    </section>
  );
}

function label(row: MarketLotRow): string {
  if (row.kind === "gift" || row.collection) {
    return `${row.collection ?? row.slug} #${row.num ?? ""}`;
  }
  if (row.kind === "number") {
    return `+${row.name ?? row.slug}`;
  }
  return `@${row.name ?? row.slug}`;
}
