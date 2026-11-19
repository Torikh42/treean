interface StatusBadgeProps {
  status: string;
}

const STATUS_STYLE: Record<string, string> = {
  PAID: "bg-status-paid-bg text-status-paid-ink border-status-paid-ink",
  PROCESSED_PAID: "bg-status-paid-bg text-status-paid-ink border-status-paid-ink",
  WAITING_PAYMENT: "bg-status-wait-bg text-status-wait-ink border-status-wait-ink",
  IGNORED_DUPLICATE: "bg-surface-muted text-ink border-ink",
  CANCELLED: "bg-status-dead-bg text-status-dead-ink border-status-dead-ink",
  EXPIRED: "bg-status-dead-bg text-status-dead-ink border-status-dead-ink",
  REJECTED_LATE_EXPIRED: "bg-status-dead-bg text-status-dead-ink border-status-dead-ink",
  PROCESSED_FAILED: "bg-status-dead-bg text-status-dead-ink border-status-dead-ink",
  SIGNATURE_INVALID: "bg-status-dead-bg text-status-dead-ink border-status-dead-ink",
  AMOUNT_MISMATCH: "bg-status-dead-bg text-status-dead-ink border-status-dead-ink",
};

export function StatusBadge({ status }: StatusBadgeProps) {
  const cls = STATUS_STYLE[status] ?? "bg-surface-muted text-ink border-ink";
  return (
    <span
      className={`inline-flex items-center border-2 px-2.5 py-0.5 font-mono text-xs font-bold uppercase ${cls}`}
    >
      {status}
    </span>
  );
}
