import { ImageResponse } from "next/og";
import { theme, themes } from "../../../lib/theme";

export const dynamic = "force-static";
export const dynamicParams = false;

const icons: Record<string, { size: number; inset: number }> = {
  "icon-192.png": { size: 192, inset: 0.28 },
  "icon-512.png": { size: 512, inset: 0.28 },
  "apple-touch-icon.png": { size: 180, inset: 0.28 },
  "maskable-512.png": { size: 512, inset: 0.36 },
};

export function generateStaticParams() {
  return Object.keys(icons).map((name) => ({ name }));
}

export async function GET(_request: Request, { params }: RouteContext<"/icons/[name]">) {
  const { name } = await params;
  const icon = icons[name];
  if (!icon) return new Response(null, { status: 404 });
  const side = Math.round(icon.size * (1 - 2 * icon.inset));
  const stroke = Math.round(side * 0.22);
  return new ImageResponse(
    <div style={{ display: "flex", alignItems: "center", justifyContent: "center", width: "100%", height: "100%", background: themes[theme].background }}>
      <div style={{ width: side, height: side, border: `${stroke}px solid ${themes[theme].accent}`, borderRadius: Math.round(side * 0.1), transform: "rotate(45deg)" }} />
    </div>,
    { width: icon.size, height: icon.size },
  );
}
