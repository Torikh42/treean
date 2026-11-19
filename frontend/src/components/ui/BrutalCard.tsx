import type { ReactNode } from "react";

interface BrutalCardProps {
  children: ReactNode;
  className?: string;
  accent?: boolean;
  muted?: boolean;
}

export function BrutalCard({
  children,
  className = "",
  accent = false,
  muted = false,
}: BrutalCardProps) {
  const bg = accent ? "bg-signal" : muted ? "bg-surface-muted" : "bg-surface";
  return (
    <section
      className={`border-2 border-ink p-5 brutal-shadow ${bg} ${className}`}
    >
      {children}
    </section>
  );
}
