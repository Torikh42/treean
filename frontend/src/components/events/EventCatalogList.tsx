import { Link } from "@tanstack/react-router";
import { ArrowsClockwise, CalendarBlank, MagnifyingGlass, MapPin, X } from "@phosphor-icons/react";
import type { Event } from "@/lib/api/generated/types.gen";
import type { UserRole } from "@/store/auth-store";
import { formatDateTime, formatIDR } from "@/lib/format";
import { parseApiError } from "@/lib/api/errors";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface EventCatalogListProps {
  events: ReadonlyArray<Event>;
  totalCount: number;
  searchQuery: string;
  onSearchChange: (val: string) => void;
  onClearSearch: () => void;
  authenticated: boolean;
  activeRole: UserRole | null;
  isLoading: boolean;
  isFetching: boolean;
  error: unknown;
  onRefresh: () => void;
}

export function EventCatalogList({
  events,
  totalCount,
  searchQuery,
  onSearchChange,
  onClearSearch,
  authenticated,
  activeRole,
  isLoading,
  isFetching,
  error,
  onRefresh,
}: EventCatalogListProps) {
  const hasActiveSearch = searchQuery.trim().length > 0;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4 border-b-3 border-ink pb-4">
        <div>
          <h1 className="text-3xl font-bold tracking-tight sm:text-4xl">
            Live Event Box Office
          </h1>
          <p className="mt-1 text-sm text-ink-muted">
            Real-time tier quotas backed by PostgreSQL row locks and 15-minute reservation holds.
          </p>
        </div>

        <div className="flex flex-wrap gap-2">
          <BrutalButton variant="secondary" disabled={isFetching} onClick={onRefresh}>
            <ArrowsClockwise size={16} weight="bold" />
            {isFetching ? "Refreshing..." : "Refresh Quotas"}
          </BrutalButton>
          <Link
            to="/flash-sale"
            className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold text-ink brutal-press"
          >
            Run 200x Flash Sale Test
          </Link>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <label className="relative flex min-w-0 flex-1 items-center sm:max-w-md">
          <span className="sr-only">Search events by title or venue</span>
          <MagnifyingGlass
            size={18}
            weight="bold"
            aria-hidden="true"
            className="pointer-events-none absolute left-3 text-ink-muted"
          />
          <input
            type="search"
            value={searchQuery}
            onChange={(e) => onSearchChange(e.target.value)}
            placeholder="Search by event title or venue..."
            className="min-h-[44px] w-full border-2 border-ink bg-surface pl-10 pr-3 text-sm font-medium text-ink placeholder:text-ink-muted"
          />
        </label>

        {hasActiveSearch && (
          <BrutalButton variant="secondary" onClick={onClearSearch}>
            <X size={16} weight="bold" />
            Clear Filter
          </BrutalButton>
        )}

        {!isLoading && !error && totalCount > 0 && (
          <span className="font-mono text-xs font-bold uppercase text-ink-muted">
            Showing {events.length} of {totalCount} event(s)
          </span>
        )}
      </div>

      {isLoading && (
        <StateNotice
          kind="loading"
          title="Loading event catalog..."
          detail="Fetching events and releasing expired unpaid holds before returning live quotas."
        />
      )}

      {Boolean(error) && (
        <StateNotice
          kind="error"
          title="Could not load events"
          error={parseApiError(error)}
          onRetry={onRefresh}
        />
      )}

      {!isLoading && !error && totalCount === 0 && (
        <StateNotice
          kind="empty"
          title="No events published yet"
          detail={
            activeRole === "ORGANIZER"
              ? "Publish your first event with at least 2 ticket tiers from the Organizer Console."
              : activeRole === "BUYER"
                ? "No ticketed events are live yet. Refresh shortly or test concurrency in the Flash Sale Lab."
                : "Sign in or register as an Organizer to publish an event with at least 2 ticket tiers."
          }
          action={
            activeRole === "ORGANIZER" ? (
              <Link
                to="/organizer"
                className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold brutal-press"
              >
                Create Event
              </Link>
            ) : !authenticated ? (
              <div className="flex flex-wrap gap-2">
                <Link
                  to="/register"
                  className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold brutal-press"
                >
                  Register as Organizer
                </Link>
                <Link
                  to="/login"
                  className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-4 py-2 text-sm font-bold brutal-press"
                >
                  Sign In
                </Link>
              </div>
            ) : undefined
          }
        />
      )}

      {!isLoading && !error && totalCount > 0 && events.length === 0 && (
        <StateNotice
          kind="empty"
          title="No matching events"
          detail={`No published events match "${searchQuery.trim()}".`}
          action={
            <BrutalButton variant="signal" onClick={onClearSearch}>
              Reset Search
            </BrutalButton>
          }
        />
      )}

      <div className="grid gap-6 md:grid-cols-2">
        {events.map((ev, idx) => {
          const tiers = ev.ticket_types ?? [];
          const totalRemaining = tiers.reduce((sum, t) => sum + t.remaining_quota, 0);
          const totalCap = tiers.reduce((sum, t) => sum + t.total_quota, 0);
          const isFeatured = idx === 0 && !hasActiveSearch;

          return (
            <BrutalCard key={ev.id} className={isFeatured ? "md:col-span-2" : ""}>
              <div className="flex flex-wrap items-start justify-between gap-4">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 font-mono text-xs text-ink-muted">
                    <span className="inline-flex items-center gap-1 border border-ink bg-surface-muted px-2 py-0.5 font-bold text-ink">
                      <CalendarBlank size={13} weight="bold" />
                      {formatDateTime(ev.event_date)}
                    </span>
                    <span className="inline-flex items-center gap-1">
                      <MapPin size={13} weight="bold" />
                      {ev.venue}
                    </span>
                  </div>
                  <h2 className="mt-2 text-2xl font-bold tracking-tight">{ev.title}</h2>
                  {ev.description && (
                    <p className="mt-1 text-sm text-ink-muted">{ev.description}</p>
                  )}
                </div>

                <div className="border-2 border-ink bg-signal px-3 py-2 text-right font-mono">
                  <p className="text-xs font-bold uppercase">Live Remaining</p>
                  <p className="text-xl font-bold">
                    {totalRemaining} / {totalCap}
                  </p>
                </div>
              </div>

              <div className="mt-4 grid gap-2 sm:grid-cols-2">
                {tiers.map((tier) => (
                  <div
                    key={tier.id}
                    className="flex items-center justify-between border-2 border-ink bg-canvas px-3 py-2.5"
                  >
                    <div>
                      <p className="text-sm font-bold">{tier.name}</p>
                      <p className="font-mono text-xs font-bold text-ink-muted">
                        {formatIDR(tier.price_idr)}
                      </p>
                    </div>
                    <div className="text-right font-mono">
                      <p className="text-sm font-bold">
                        {tier.remaining_quota} / {tier.total_quota}
                      </p>
                      <p className="text-[11px] uppercase text-ink-muted">left</p>
                    </div>
                  </div>
                ))}
              </div>

              <div className="mt-4 flex flex-wrap items-center justify-between gap-3 border-t-2 border-ink pt-4">
                <span className="font-mono text-xs text-ink-muted">ID: {ev.id}</span>
                <Link
                  to="/events/$eventId"
                  params={{ eventId: ev.id }}
                  className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-5 py-2 text-sm font-bold brutal-press"
                >
                  {activeRole === "ORGANIZER" ? "View Event & Quotas" : "Select Tickets (Max 4)"}
                </Link>
              </div>
            </BrutalCard>
          );
        })}
      </div>
    </div>
  );
}
