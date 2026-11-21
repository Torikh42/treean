import { useEffect, useState } from "react";
import { formatCountdown } from "@/lib/format";

export function useCountdown(expiresAtIso: string | undefined, active: boolean) {
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    if (!active || !expiresAtIso) return;
    const targetMs = new Date(expiresAtIso).getTime();
    if (Number.isNaN(targetMs)) return;
    const immediateId = window.setTimeout(() => {
      setNowMs(Date.now());
    }, 0);
    if (Date.now() >= targetMs) {
      return () => window.clearTimeout(immediateId);
    }
    const id = window.setInterval(() => {
      const current = Date.now();
      setNowMs(current);
      if (current >= targetMs) {
        window.clearInterval(id);
      }
    }, 1000);
    return () => {
      window.clearTimeout(immediateId);
      window.clearInterval(id);
    };
  }, [active, expiresAtIso]);

  if (!expiresAtIso) {
    return { remainingMs: 0, formatted: "00:00", isExpired: false };
  }
  const targetMs = new Date(expiresAtIso).getTime();
  if (Number.isNaN(targetMs)) {
    return { remainingMs: 0, formatted: "00:00", isExpired: false };
  }
  const remainingMs = Math.max(0, targetMs - nowMs);
  return {
    remainingMs,
    formatted: formatCountdown(remainingMs),
    isExpired: remainingMs === 0,
  };
}
