import { Link } from "@tanstack/react-router";
import type { ParsedApiError } from "@/lib/api/errors";
import { formatIDR } from "@/lib/format";
import { MAX_TICKETS_PER_ORDER } from "@/hooks/useEventCheckout";
import type { UserRole } from "@/store/auth-store";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface ReservationSummaryCardProps {
  eventId: string;
  authenticated: boolean;
  activeRole: UserRole | null;
  existingEventTickets: number;
  remainingAllowance: number;
  totalSelected: number;
  totalAmountIDR: number;
  isSubmitting: boolean;
  submitError: ParsedApiError | null;
  onSubmit: () => void;
}

export function ReservationSummaryCard({
  eventId,
  authenticated,
  activeRole,
  existingEventTickets,
  remainingAllowance,
  totalSelected,
  totalAmountIDR,
  isSubmitting,
  submitError,
  onSubmit,
}: ReservationSummaryCardProps) {
  return (
    <BrutalCard accent className="sticky top-6">
      <h2 className="border-b-2 border-ink pb-3 text-xl font-bold">Reservation Summary</h2>
      <div className="mt-4 space-y-2 font-mono text-sm">
        <div className="flex justify-between">
          <span>Selected Tickets</span>
          <span className="font-bold">
            {totalSelected} / {remainingAllowance}
          </span>
        </div>
        {authenticated && activeRole === "BUYER" && existingEventTickets > 0 && (
          <div className="flex justify-between text-xs text-ink-muted">
            <span>Used in Your Orders</span>
            <span className="font-bold">
              {existingEventTickets} / {MAX_TICKETS_PER_ORDER} ({remainingAllowance} left)
            </span>
          </div>
        )}
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
          <StateNotice
            kind="error"
            title="Checkout blocked"
            error={submitError}
            action={
              submitError.code === "BUYER_LIMIT_EXCEEDED" ? (
                <Link
                  to="/orders"
                  className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-3 py-1.5 text-xs font-bold text-ink brutal-press"
                >
                  View My Orders
                </Link>
              ) : undefined
            }
          />
        </div>
      )}

      {authenticated && activeRole === "BUYER" && remainingAllowance === 0 && !submitError && (
        <div className="mt-4 space-y-2 border-2 border-ink bg-surface p-3" role="status">
          <p className="font-mono text-xs font-bold uppercase">Event Ticket Limit Reached</p>
          <p className="text-sm text-ink-muted">
            You already hold or purchased {MAX_TICKETS_PER_ORDER} tickets for this event. Pay or cancel an unpaid order to free up slots.
          </p>
          <Link
            to="/orders"
            className="inline-flex min-h-[44px] w-full items-center justify-center border-2 border-ink bg-signal px-3 py-2 text-xs font-bold text-ink brutal-press"
          >
            Manage My Orders
          </Link>
        </div>
      )}

      {!authenticated ? (
        <div className="mt-4 space-y-3 border-2 border-ink bg-surface p-3">
          <p className="font-mono text-xs font-bold uppercase">Buyer Sign-In Required</p>
          <p className="text-sm text-ink-muted">
            Sign in as a Buyer to reserve tickets under a 15-minute payment hold.
          </p>
          <div className="flex flex-col gap-2">
            <Link
              to="/login"
              search={{ redirect: `/events/${eventId}` }}
              className="inline-flex min-h-[44px] w-full items-center justify-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold text-ink brutal-press"
            >
              Sign In as Buyer to Reserve
            </Link>
            <Link
              to="/register"
              search={{ redirect: `/events/${eventId}` }}
              className="inline-flex min-h-[44px] w-full items-center justify-center border-2 border-ink bg-canvas px-4 py-2 text-sm font-bold text-ink brutal-press"
            >
              Register Buyer Account
            </Link>
          </div>
        </div>
      ) : activeRole === "ORGANIZER" ? (
        <>
          <div className="mt-4 border-2 border-ink bg-surface p-3" role="status">
            <p className="font-mono text-xs font-bold uppercase">Buyer Account Required</p>
            <p className="mt-1 text-sm text-ink-muted">
              Ticket checkout requires a Buyer account. Organizer accounts cannot purchase tickets.
            </p>
          </div>
          <BrutalButton
            variant="ink"
            className="mt-4 w-full"
            disabled
          >
            Reserve Tickets (Buyer Only)
          </BrutalButton>
        </>
      ) : (
        <BrutalButton
          variant="ink"
          className="mt-5 w-full"
          disabled={isSubmitting || totalSelected === 0 || remainingAllowance === 0 || totalSelected > remainingAllowance}
          onClick={onSubmit}
        >
          {isSubmitting ? "Locking Quota..." : "Reserve Tickets (15m Hold)"}
        </BrutalButton>
      )}
    </BrutalCard>
  );
}
