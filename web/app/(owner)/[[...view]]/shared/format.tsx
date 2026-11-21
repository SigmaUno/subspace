export { formatSize } from "../../../../lib/format";

export function formatTime(iso?: string | null) {
  return iso ? <time dateTime={iso}>{new Date(iso).toLocaleString()}</time> : "—";
}

export const usd = new Intl.NumberFormat("en", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 4 });
export const count = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });

export const stamp = (time?: string | null) => time ? new Date(time).toLocaleString([], { dateStyle: "short", timeStyle: "medium" }) : "—";

export const shortStamp = (time: string) => new Date(time).toLocaleString([], { dateStyle: "short", timeStyle: "short" });

export const utc = (iso: string) => `${new Date(iso).toLocaleString([], { dateStyle: "short", timeStyle: "short", timeZone: "UTC" })} UTC`;
