import type { Metadata, Viewport } from "next";
import { theme, themes } from "../lib/theme";
import "./styles.css";

export const metadata: Metadata = {
  title: "Subspace",
  description: "Subspace owner workspace",
  applicationName: "Subspace",
  appleWebApp: { capable: true, title: "Subspace", statusBarStyle: "default" },
  formatDetection: { telephone: false },
  icons: {
    icon: [{ url: "/icons/icon-192.png", sizes: "192x192", type: "image/png" }, { url: "/icons/icon-512.png", sizes: "512x512", type: "image/png" }],
    apple: [{ url: "/icons/apple-touch-icon.png", sizes: "180x180", type: "image/png" }],
  },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  interactiveWidget: "resizes-content",
  colorScheme: theme,
  themeColor: themes[theme].background,
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="en" data-theme={theme}><body>{children}</body></html>;
}
