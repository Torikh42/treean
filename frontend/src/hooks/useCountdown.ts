import { useEffect, useState } from "react";
import { formatCountdown } from "@/lib/format";

export function useCountdown(expiresAtIso: string | undefined, active: boolean) {
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    if (!active || !expiresAtIso) return;
    const id = window.setInterval(() => {
      setNowMs(Date.now());
    }, 1000);
    return () => window.clearInterval(id);
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
