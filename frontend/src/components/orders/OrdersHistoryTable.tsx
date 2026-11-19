import { Link } from "@tanstack/react-router";
import { ArrowsClockwise } from "@phosphor-icons/react";
import type { Order } from "@/lib/api/generated/types.gen";
import { parseApiError } from "@/lib/api/errors";
import { formatDateTime, formatIDR } from "@/lib/format";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";
import { StatusBadge } from "@/components/ui/StatusBadge";

interface OrdersHistoryTableProps {
  orders: ReadonlyArray<Order>;
  enabled: boolean;
  isLoading: boolean;
  isFetching: boolean;
  error: unknown;
  onRefresh: () => void;
}

export function OrdersHistoryTable({
  orders,
  enabled,
  isLoading,
  isFetching,
  error,
  onRefresh,
}: OrdersHistoryTableProps) {
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4 border-b-3 border-ink pb-4">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">My Orders & Tickets</h1>
          <p className="mt-1 text-sm text-ink-muted">
            Inspect reservation status, complete simulated payment within 15 minutes, or view issued ticket codes.
          </p>
        </div>
        <BrutalButton variant="secondary" disabled={!enabled || isFetching} onClick={onRefresh}>
          <ArrowsClockwise size={16} weight="bold" />
          {isFetching ? "Refreshing..." : "Refresh Orders"}
        </BrutalButton>
      </div>

      {!enabled && (
        <StateNotice
          kind="empty"
          title="No active buyer identity"
          detail="Open the identity panel in the top right to sign in or select a demo buyer."
        />
      )}

      {enabled && isLoading && (
        <StateNotice kind="loading" title="Loading order history..." detail="Resolving expired reservations first." />
      )}

      {enabled && Boolean(error) && (
        <StateNotice kind="error" title="Could not load orders" error={parseApiError(error)} onRetry={onRefresh} />
      )}

      {enabled && !isLoading && !error && orders.length === 0 && (
        <StateNotice
          kind="empty"
          title="No orders placed for this account yet"
          detail="Browse live events and reserve 1 to 4 tickets to inspect payment & expiry flows."
          action={
            <Link
              to="/"
              className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold brutal-press"
            >
              Browse Events
            </Link>
          }
        />
      )}

      <div className="grid gap-4">
        {orders.map((ord) => (
          <BrutalCard key={ord.id}>
            <div className="flex flex-wrap items-center justify-between gap-4">
              <div>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-sm font-bold">{ord.invoice_number}</span>
                  <StatusBadge status={ord.status} />
                </div>
                <p className="mt-1 font-mono text-xs text-ink-muted">
                  Order ID: {ord.id} | Created: {formatDateTime(ord.created_at)}
                </p>
              </div>

              <div className="flex flex-wrap items-center gap-4">
                <div className="text-right font-mono">
                  <p className="text-xs text-ink-muted">{ord.total_tickets} ticket(s)</p>
                  <p className="text-base font-bold">{formatIDR(ord.total_amount_idr)}</p>
                </div>

                <Link
                  to="/orders/$orderId"
                  params={{ orderId: ord.id }}
                  className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold brutal-press"
                >
                  Open Order
                </Link>
              </div>
            </div>
          </BrutalCard>
        ))}
      </div>
    </div>
  );
}
