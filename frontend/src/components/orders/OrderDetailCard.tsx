import { Link } from "@tanstack/react-router";
import { ArrowLeft, CreditCard, Ticket, XCircle } from "@phosphor-icons/react";
import type { Order } from "@/lib/api/generated/types.gen";
import type { ParsedApiError } from "@/lib/api/errors";
import { parseApiError } from "@/lib/api/errors";
import { formatDateTime, formatIDR } from "@/lib/format";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";
import { StatusBadge } from "@/components/ui/StatusBadge";

interface OrderDetailCardProps {
  order: Order | undefined;
  isLoading: boolean;
  isFetching?: boolean;
  error: unknown;
  actionError?: ParsedApiError | null;
  onRetry: () => void;
  countdown: { formatted: string; isExpired: boolean };
  onPay: () => void;
  onCancel: () => void;
  isPaying: boolean;
  isCancelling: boolean;
}

export function OrderDetailCard({
  order,
  isLoading,
  isFetching = false,
  error,
  actionError = null,
  onRetry,
  countdown,
  onPay,
  onCancel,
  isPaying,
  isCancelling,
}: OrderDetailCardProps) {
  if (isLoading) {
    return <StateNotice kind="loading" title="Loading order details..." />;
  }

  if (error || !order) {
    return (
      <StateNotice
        kind="error"
        title="Could not load order"
        error={parseApiError(error)}
        onRetry={onRetry}
        action={
          <Link
            to="/orders"
            className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-4 py-2 text-sm font-bold brutal-press"
          >
            Back to Orders
          </Link>
        }
      />
    );
  }

  const isWaiting = order.status === "WAITING_PAYMENT";
  const items = order.items ?? [];
  const tickets = order.tickets ?? [];
  const logs = order.logs ?? [];

  return (
    <div className="space-y-6">
      <Link
        to="/orders"
        className="inline-flex min-h-[44px] items-center gap-2 border-2 border-ink bg-surface px-3 py-1.5 text-sm font-bold brutal-press"
      >
        <ArrowLeft size={16} weight="bold" />
        Back to Orders
      </Link>

      <BrutalCard>
        <div className="flex flex-wrap items-start justify-between gap-4 border-b-2 border-ink pb-4">
          <div>
            <div className="flex flex-wrap items-center gap-3">
              <h1 className="font-mono text-2xl font-bold">{order.invoice_number}</h1>
              <StatusBadge status={order.status} />
            </div>
            <p className="mt-1 font-mono text-xs text-ink-muted">Order UUID: {order.id}</p>
            <p className="font-mono text-xs text-ink-muted">Expires At: {formatDateTime(order.expires_at)}</p>
          </div>

          <div className="text-right font-mono">
            {isWaiting && (
              <div className="mb-2 inline-block border-2 border-ink bg-signal px-3 py-1">
                <span className="text-xs font-bold uppercase">Hold Window: </span>
                <span className="text-lg font-bold">
                  {countdown.isExpired ? "DUE FOR EXPIRY" : countdown.formatted}
                </span>
              </div>
            )}
            <p className="text-2xl font-bold">{formatIDR(order.total_amount_idr)}</p>
          </div>
        </div>

        <div className="mt-4 space-y-2">
          {items.map((item) => (
            <div key={item.id} className="flex items-center justify-between border-2 border-ink bg-canvas px-3 py-2 text-sm">
              <span className="font-bold">
                {item.ticket_type_name || item.ticket_type_id} x {item.quantity}
              </span>
              <span className="font-mono font-bold">{formatIDR(item.subtotal_idr)}</span>
            </div>
          ))}
        </div>

        {isWaiting && countdown.isExpired && (
          <div className="mt-4">
            <StateNotice
              kind="empty"
              title="Reservation window ended"
              detail="This 15-minute payment hold has passed. Refresh to sync final expired status and released ticket quota."
              action={
                <BrutalButton variant="signal" disabled={isFetching} onClick={onRetry}>
                  {isFetching ? "Refreshing..." : "Refresh Order Status"}
                </BrutalButton>
              }
            />
          </div>
        )}

        {actionError && (
          <div className="mt-4">
            <StateNotice kind="error" title="Order action failed" error={actionError} onRetry={onRetry} />
          </div>
        )}

        {isWaiting && (
          <div className="mt-5 flex flex-wrap gap-3">
            <BrutalButton
              variant="signal"
              disabled={countdown.isExpired || isPaying || isCancelling}
              onClick={onPay}
            >
              <CreditCard size={16} weight="bold" />
              {isPaying ? "Signing & Settling..." : "Pay Now (Signed Demo Webhook)"}
            </BrutalButton>
            <BrutalButton
              variant="danger"
              disabled={countdown.isExpired || isPaying || isCancelling}
              onClick={onCancel}
            >
              <XCircle size={16} weight="bold" />
              {isCancelling ? "Cancelling..." : "Cancel Order & Restore Quota"}
            </BrutalButton>
            <Link
              to="/flash-sale"
              search={{ orderId: order.id, grossAmount: order.total_amount_idr }}
              className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-4 py-2 text-sm font-bold brutal-press"
            >
              Test in Webhook Lab
            </Link>
          </div>
        )}
      </BrutalCard>

      {tickets.length > 0 && (
        <BrutalCard accent>
          <h2 className="flex items-center gap-2 text-xl font-bold">
            <Ticket size={20} weight="bold" />
            Issued Ticket Passes ({tickets.length})
          </h2>
          <div className="mt-4 grid gap-3 sm:grid-cols-2">
            {tickets.map((tkt) => (
              <div key={tkt.id} className="border-2 border-ink bg-surface p-3 font-mono">
                <p className="text-xs font-bold uppercase text-ink-muted">Seat #{tkt.seat_sequence}</p>
                <p className="mt-1 text-base font-bold tracking-wide">{tkt.ticket_code}</p>
                <p className="mt-1 text-[11px] text-ink-muted">Issued: {formatDateTime(tkt.issued_at)}</p>
              </div>
            ))}
          </div>
        </BrutalCard>
      )}

      {logs.length > 0 && (
        <BrutalCard muted>
          <h2 className="text-lg font-bold">State Transition Audit Trail</h2>
          <div className="mt-3 space-y-2 font-mono text-xs">
            {logs.map((log) => (
              <div key={log.id} className="flex flex-wrap items-center justify-between gap-2 border border-ink bg-surface px-3 py-2">
                <span>
                  {log.from_status ?? "INIT"} {"->"} <strong>{log.to_status}</strong> ({log.actor_type}): {log.note}
                </span>
                <span className="text-ink-muted">{formatDateTime(log.created_at)}</span>
              </div>
            ))}
          </div>
        </BrutalCard>
      )}
    </div>
  );
}
