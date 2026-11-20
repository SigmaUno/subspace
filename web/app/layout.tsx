import type { Metadata } from "next";
import "./styles.css";

export const metadata: Metadata = { title: "Subspace", description: "Subspace owner workspace" };

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return <html lang="en" data-theme="light"><body>{children}</body></html>;
}
