const units = ["B", "KB", "MB", "GB", "TB"];

export function formatSize(bytes: number) {
  let size = bytes;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  return `${unit ? size.toFixed(1) : size} ${units[unit]}`;
}

export function formatTime(iso?: string | null) {
  return iso ? <time dateTime={iso}>{new Date(iso).toLocaleString()}</time> : "—";
}

export const usd = new Intl.NumberFormat("en", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 4 });
export const count = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });

export const stamp = (time?: string | null) => time ? new Date(time).toLocaleString([], { dateStyle: "short", timeStyle: "medium" }) : "—";

export const utc = (iso: string) => `${new Date(iso).toLocaleString([], { dateStyle: "short", timeStyle: "short", timeZone: "UTC" })} UTC`;
