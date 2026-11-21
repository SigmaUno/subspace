import type { MetadataRoute } from "next";
import { theme, themes } from "../lib/theme";

export default function manifest(): MetadataRoute.Manifest {
  return {
    id: "/",
    name: "Subspace",
    short_name: "Subspace",
    description: "Subspace owner workspace",
    start_url: "/",
    scope: "/",
    display: "standalone",
    orientation: "any",
    background_color: themes[theme].background,
    theme_color: themes[theme].background,
    icons: [
      { src: "/icons/icon-192.png", sizes: "192x192", type: "image/png", purpose: "any" },
      { src: "/icons/icon-512.png", sizes: "512x512", type: "image/png", purpose: "any" },
      { src: "/icons/maskable-512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
    ],
  };
}
