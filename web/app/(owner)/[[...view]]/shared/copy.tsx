"use client";

import { useCallback } from "react";
import { useToast } from "./ui";

export function useCopy() {
  const toast = useToast();
  return useCallback(async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast(`Copied ${what}.`);
    } catch {
      toast("Select and copy the text.", "error");
    }
  }, [toast]);
}

export function CopyBlock({ label, text }: { label: string; text: string }) {
  const copy = useCopy();
  return <div className="secret">
    <div className="heading"><strong>{label}</strong>
      <button type="button" onClick={() => copy(text, label)} aria-label={`Copy ${label}`}>Copy</button>
    </div>
    <pre>{text}</pre>
  </div>;
}
