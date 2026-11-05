import { Store, X } from "lucide-react";
import { useState } from "react";
import { createPortal } from "react-dom";
import { api, errorMessage } from "../api";
import { Alert } from "../components/ui";

export type MarketGift = { slug: string; collection: string; num: number; gift_id: number };

// MarketLotModal publishes one collectible on the marketplace. The price is
// typed in whole GRAM; the market converts to its own minor units server-side,
// so the panel sends the decimal value unchanged.
//
// Gifts are fixed-price only -- the market refuses an auction for them -- so the
// mode selector is hidden for that kind instead of letting the operator pick
// something that will be rejected.
export function MarketLotModal({
  kind,
  name,
  gift,
  onClose,
  onDone
}: {
  kind: "username" | "number" | "gift";
  name: string;
  gift?: MarketGift;
  onClose: () => void;
  onDone: () => void;
}) {
  const [price, setPrice] = useState("500");
  const [mode, setMode] = useState<"sale" | "auction">("sale");
  const [hours, setHours] = useState("24");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState("");

  const isGift = kind === "gift";
  const priceValue = Number(price.replace(",", "."));
  const hoursValue = Number(hours);
  const valid =
    Number.isFinite(priceValue) &&
    priceValue >= 1 &&
    (!isGift || (gift && gift.num > 0 && gift.gift_id > 0)) &&
    (isGift || mode === "sale" || (Number.isFinite(hoursValue) && hoursValue >= 1 && hoursValue <= 720));

  const title = isGift ? `${gift?.collection ?? name} #${gift?.num ?? ""}` : kind === "username" ? `@${name}` : `+${name}`;

  async function submit() {
    if (!valid || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await api.marketLot({
        kind,
        name: isGift ? undefined : name,
        price: priceValue,
        auction_hours: !isGift && mode === "auction" ? Math.round(hoursValue) : 0,
        gift: isGift ? gift : undefined
      });
      setCreated(result.slug || title);
      onDone();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return createPortal(
    <div className="modal-backdrop" role="presentation">
      <section className="modal command-modal" role="dialog" aria-modal="true" aria-label="Выставить на маркет">
        <div className="modal-head">
          <div>
            <div className="eyebrow">Маркет</div>
            <h2>Выставить на маркет</h2>
            <p className="bot-create-note">{title}</p>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} aria-label="Закрыть">
            <X size={15} />
          </button>
        </div>
        <div className="command-body">
          {created ? (
            <Alert>Лот создан и уже виден на витрине: {created}</Alert>
          ) : (
            <>
              <div className="bot-create-fields">
                <label className="duration-field">
                  <span>Цена, GRAM</span>
                  <input value={price} onChange={(event) => setPrice(event.target.value)} inputMode="decimal" placeholder="500" />
                </label>
                {!isGift && (
                  <label className="duration-field">
                    <span>Режим</span>
                    <select value={mode} onChange={(event) => setMode(event.target.value as "sale" | "auction")}>
                      <option value="sale">Фиксированная цена</option>
                      <option value="auction">Аукцион</option>
                    </select>
                  </label>
                )}
                {!isGift && mode === "auction" && (
                  <label className="duration-field">
                    <span>Длительность, часов</span>
                    <input value={hours} onChange={(event) => setHours(event.target.value)} inputMode="numeric" placeholder="24" />
                  </label>
                )}
              </div>
              <p className="bot-create-note">
                Минимум 1 GRAM. {isGift ? "Подарки продаются только по фиксированной цене." : "Аукцион — от 1 до 720 часов."}
              </p>
              {error && <Alert>{error}</Alert>}
            </>
          )}
        </div>
        <div className="modal-actions">
          <button className="btn" type="button" onClick={onClose}>
            {created ? "Закрыть" : "Отмена"}
          </button>
          {!created && (
            <button className="btn primary" type="button" onClick={submit} disabled={!valid || busy}>
              <Store size={15} /> {busy ? "Выставляем…" : "Выставить"}
            </button>
          )}
        </div>
      </section>
    </div>,
    document.body
  );
}
