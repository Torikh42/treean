import { Link } from "@tanstack/react-router";
import { ArrowLeft, CalendarBlank, MapPin, Minus, Plus } from "@phosphor-icons/react";
import type { Event, TicketType } from "@/lib/api/generated/types.gen";
import { parseApiError, type ParsedApiError } from "@/lib/api/errors";
import { formatDateTime, formatIDR } from "@/lib/format";
import { MAX_TICKETS_PER_ORDER } from "@/hooks/useEventCheckout";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface EventCheckoutPanelProps {
  event: Event | undefined;
  tiers: ReadonlyArray<TicketType>;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  quantities: Record<string, number>;
  totalSelected: number;
  totalAmountIDR: number;
  onUpdateQty: (tierId: string, delta: number, maxQuota: number) => void;
  onSubmit: () => void;
  isSubmitting: boolean;
  submitError: ParsedApiError | null;
}

export function EventCheckoutPanel({
  event,
  tiers,
  isLoading,
  error,
  onRetry,
  quantities,
  totalSelected,
  totalAmountIDR,
  onUpdateQty,
  onSubmit,
  isSubmitting,
  submitError,
}: EventCheckoutPanelProps) {
  if (isLoading) {
    return (
      <StateNotice
        kind="loading"
        title="Loading event details..."
        detail="Checking real-time remaining quotas for every ticket tier."
      />
    );
  }

  if (error || !event) {
    return (
      <StateNotice
        kind="error"
        title="Event unavailable"
        error={parseApiError(error)}
        onRetry={onRetry}
        action={
          <Link
            to="/"
            className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-4 py-2 text-sm font-bold brutal-press"
          >
            Back to Events
          </Link>
        }
      />
    );
  }

  return (
    <div className="space-y-6">
      <Link
        to="/"
        className="inline-flex min-h-[44px] items-center gap-2 border-2 border-ink bg-surface px-3 py-1.5 text-sm font-bold brutal-press"
      >
        <ArrowLeft size={16} weight="bold" />
        Back to Events
      </Link>

      <div className="grid gap-6 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-2">
          <BrutalCard>
            <div className="flex flex-wrap items-center gap-3 font-mono text-xs text-ink-muted">
              <span className="inline-flex items-center gap-1 border border-ink bg-surface-muted px-2 py-0.5 font-bold text-ink">
                <CalendarBlank size={14} weight="bold" />
                {formatDateTime(event.event_date)}
              </span>
              <span className="inline-flex items-center gap-1">
                <MapPin size={14} weight="bold" />
                {event.venue}
              </span>
            </div>
            <h1 className="mt-2 text-3xl font-bold tracking-tight">{event.title}</h1>
            {event.description && <p className="mt-2 text-sm text-ink-muted">{event.description}</p>}
          </BrutalCard>

          <BrutalCard>
            <h2 className="text-xl font-bold">Select Ticket Tiers</h2>
            <p className="mt-1 text-sm text-ink-muted">
              Maximum {MAX_TICKETS_PER_ORDER} cumulative tickets per buyer per event across active and paid orders.
            </p>

            <div className="mt-4 space-y-3">
              {tiers.map((tier) => {
                const selected = quantities[tier.id] ?? 0;
                const soldOut = tier.remaining_quota <= 0;
                const disableInc = soldOut || totalSelected >= MAX_TICKETS_PER_ORDER || selected >= tier.remaining_quota;

                return (
                  <div
                    key={tier.id}
                    className="flex flex-wrap items-center justify-between gap-4 border-2 border-ink bg-canvas p-4"
                  >
                    <div>
                      <div className="flex items-center gap-2">
                        <h3 className="text-lg font-bold">{tier.name}</h3>
                        {soldOut && (
                          <span className="border-2 border-status-dead-ink bg-status-dead-bg px-2 py-0.5 font-mono text-xs font-bold text-status-dead-ink">
                            SOLD OUT
                          </span>
                        )}
                      </div>
                      <p className="font-mono text-sm font-bold">{formatIDR(tier.price_idr)}</p>
                      <p className="mt-0.5 font-mono text-xs text-ink-muted">
                        Remaining: {tier.remaining_quota} / {tier.total_quota}
                      </p>
                    </div>

                    <div className="flex items-center gap-2">
                      <BrutalButton
                        variant="secondary"
                        aria-label={`Decrease ${tier.name}`}
                        disabled={selected <= 0}
                        onClick={() => onUpdateQty(tier.id, -1, tier.remaining_quota)}
                      >
                        <Minus size={16} weight="bold" />
                      </BrutalButton>
                      <span className="min-w-[2.5rem] text-center font-mono text-lg font-bold">
                        {selected}
                      </span>
                      <BrutalButton
                        variant="signal"
                        aria-label={`Increase ${tier.name}`}
                        disabled={disableInc}
                        onClick={() => onUpdateQty(tier.id, 1, tier.remaining_quota)}
                      >
                        <Plus size={16} weight="bold" />
                      </BrutalButton>
                    </div>
                  </div>
                );
              })}
            </div>
          </BrutalCard>
        </div>

        <div className="lg:col-span-1">
          <BrutalCard accent className="sticky top-6">
            <h2 className="border-b-2 border-ink pb-3 text-xl font-bold">Reservation Summary</h2>
            <div className="mt-4 space-y-2 font-mono text-sm">
              <div className="flex justify-between">
                <span>Selected Tickets</span>
                <span className="font-bold">
                  {totalSelected} / {MAX_TICKETS_PER_ORDER}
                </span>
              </div>
              <div className="flex justify-between">
                <span>Hold Window</span>
                <span className="font-bold">15 MINUTES</span>
              </div>
              <div className="flex justify-between border-t-2 border-ink pt-3 text-lg font-bold">
                <span>Total IDR</span>
                <span>{formatIDR(totalAmountIDR)}</span>
              </div>
            </div>

            {submitError && (
              <div className="mt-4">
                <StateNotice kind="error" title="Checkout blocked" error={submitError} />
              </div>
            )}

            <BrutalButton
              variant="secondary"
              className="mt-5 w-full bg-ink text-signal hover:bg-neutral-800 hover:text-signal"
              disabled={isSubmitting || totalSelected === 0}
              onClick={onSubmit}
            >
              {isSubmitting ? "Locking Quota..." : "Reserve Tickets (15m Hold)"}
            </BrutalButton>
          </BrutalCard>
        </div>
      </div>
    </div>
  );
}
