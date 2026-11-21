import type { ButtonHTMLAttributes, ReactNode } from "react";

type Variant = "signal" | "secondary" | "danger" | "ghost" | "ink";

interface BrutalButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  children: ReactNode;
}

const VARIANT_CLASSES: Record<Variant, string> = {
  signal: "bg-signal text-ink hover:bg-signal-hover",
  secondary: "bg-surface text-ink hover:bg-surface-muted",
  danger: "bg-status-dead-bg text-status-dead-ink hover:bg-red-200",
  ghost: "bg-transparent text-ink shadow-none border-transparent hover:border-ink hover:bg-surface-muted",
  ink: "bg-ink text-signal hover:bg-neutral-800",
};

export function BrutalButton({
  variant = "secondary",
  className = "",
  type = "button",
  children,
  ...props
}: BrutalButtonProps) {
  const pressClass = variant === "ghost" ? "" : "brutal-press";
  return (
    <button
      type={type}
      className={`inline-flex min-h-[44px] cursor-pointer items-center justify-center gap-2 border-2 border-ink px-4 py-2 text-sm font-bold tracking-tight disabled:cursor-not-allowed disabled:opacity-50 ${pressClass} ${VARIANT_CLASSES[variant]} ${className}`}
      {...props}
    >
      {children}
    </button>
  );
}
