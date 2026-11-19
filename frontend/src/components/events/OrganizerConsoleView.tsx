import { Link } from "@tanstack/react-router";
import { ArrowsClockwise, CalendarBlank, MapPin, PencilSimple, Plus, Trash } from "@phosphor-icons/react";
import type { useOrganizerConsole } from "@/hooks/useOrganizerConsole";
import { parseApiError } from "@/lib/api/errors";
import { formatDateTime, formatIDR } from "@/lib/format";
import { computeSoldOrHeld } from "@/lib/organizer-event-helpers";
import { OrganizerEventForm } from "@/components/events/OrganizerEventForm";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface OrganizerConsoleViewProps {
  consoleState: ReturnType<typeof useOrganizerConsole>;
}

export function OrganizerConsoleView({ consoleState }: OrganizerConsoleViewProps) {
  const isEditing = Boolean(consoleState.editingEventId);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-4 border-b-3 border-ink pb-4">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Organizer Event Console</h1>
          <p className="mt-1 text-sm text-ink-muted">
            Manage your published events, adjust tier quotas safely, or remove unused releases.
          </p>
        </div>

        <div className="flex flex-wrap gap-2">
          {isEditing && (
            <BrutalButton variant="signal" onClick={consoleState.cancelEdit}>
              <Plus size={16} weight="bold" />
              New Event
            </BrutalButton>
          )}
          <BrutalButton
            variant="secondary"
            disabled={consoleState.isFetchingEvents}
            onClick={consoleState.refetchEvents}
          >
            <ArrowsClockwise size={16} weight="bold" />
            {consoleState.isFetchingEvents ? "Refreshing..." : "Refresh My Events"}
          </BrutalButton>
        </div>
      </div>

      <div className="grid gap-8 lg:grid-cols-12">
        <div className="space-y-4 lg:col-span-5">
          <div className="flex items-center justify-between border-b-2 border-ink pb-2">
            <h2 className="text-xl font-bold">My Events</h2>
            <span className="font-mono text-xs font-bold uppercase text-ink-muted">
              {consoleState.events.length} owned
            </span>
          </div>

          {consoleState.isLoadingEvents && (
            <StateNotice
              kind="loading"
              title="Loading your events..."
              detail="Fetching organizer events and computing live sold/reserved tier counts."
            />
          )}

          {Boolean(consoleState.eventsError) && (
            <StateNotice
              kind="error"
              title="Could not load organizer events"
              error={parseApiError(consoleState.eventsError)}
              onRetry={consoleState.refetchEvents}
            />
          )}

          {!consoleState.isLoadingEvents &&
            !consoleState.eventsError &&
            consoleState.events.length === 0 && (
              <StateNotice
                kind="empty"
                title="No events created yet"
                detail="Use the form on this page to publish your first event with 2 to 10 ticket tiers."
              />
            )}

          <div className="space-y-4">
            {consoleState.events.map((ev) => {
              const tiers = ev.ticket_types ?? [];
              const isSelected = consoleState.editingEventId === ev.id;
              const confirmingDelete = consoleState.deleteConfirmId === ev.id;
              const cardDeleteError =
                consoleState.deleteError?.eventId === ev.id
                  ? consoleState.deleteError.error
                  : null;

              return (
                <BrutalCard key={ev.id} accent={isSelected}>
                  <div className="space-y-3">
                    <div>
                      <div className="flex flex-wrap items-center gap-2 font-mono text-xs text-ink-muted">
                        <span className="inline-flex items-center gap-1 border border-ink bg-surface px-2 py-0.5 font-bold text-ink">
                          <CalendarBlank size={12} weight="bold" />
                          {formatDateTime(ev.event_date)}
                        </span>
                        <span className="inline-flex items-center gap-1 text-ink">
                          <MapPin size={12} weight="bold" />
                          {ev.venue}
                        </span>
                      </div>
                      <h3 className="mt-2 text-lg font-bold tracking-tight">{ev.title}</h3>
                      {ev.description && (
                        <p className="mt-1 text-xs text-ink-muted">{ev.description}</p>
                      )}
                    </div>

                    <div className="space-y-1.5">
                      {tiers.map((tier) => {
                        const soldOrHeld = computeSoldOrHeld(tier);
                        return (
                          <div
                            key={tier.id}
                            className="flex flex-wrap items-center justify-between gap-2 border-2 border-ink bg-canvas px-3 py-2 font-mono text-xs"
                          >
                            <div>
                              <span className="font-bold text-ink">{tier.name}</span>
                              <span className="ml-2 text-ink-muted">
                                {formatIDR(tier.price_idr)}
                              </span>
                            </div>
                            <div className="text-right">
                              <span className="font-bold text-ink">
                                {tier.remaining_quota} / {tier.total_quota} left
                              </span>
                              <span className="ml-2 text-ink-muted">
                                (sold/held: {soldOrHeld})
                              </span>
                            </div>
                          </div>
                        );
                      })}
                    </div>

                    {cardDeleteError && (
                      <StateNotice
                        kind="error"
                        title="Delete blocked"
                        error={cardDeleteError}
                      />
                    )}

                    {confirmingDelete ? (
                      <div className="space-y-2 border-2 border-status-dead-ink bg-status-dead-bg p-3">
                        <p className="text-xs font-bold text-status-dead-ink">
                          Delete &ldquo;{ev.title}&rdquo;? Only events with zero buyer orders can be deleted.
                        </p>
                        <div className="flex flex-wrap gap-2">
                          <BrutalButton
                            variant="danger"
                            disabled={consoleState.isDeleting}
                            onClick={() => consoleState.confirmDelete(ev.id)}
                          >
                            {consoleState.isDeleting ? "Deleting..." : "Confirm Delete"}
                          </BrutalButton>
                          <BrutalButton
                            variant="secondary"
                            disabled={consoleState.isDeleting}
                            onClick={consoleState.cancelDelete}
                          >
                            Cancel
                          </BrutalButton>
                        </div>
                      </div>
                    ) : (
                      <div className="flex flex-wrap items-center gap-2 border-t-2 border-ink pt-3">
                        <BrutalButton
                          variant={isSelected ? "signal" : "secondary"}
                          onClick={() => consoleState.startEdit(ev)}
                        >
                          <PencilSimple size={16} weight="bold" />
                          {isSelected ? "Editing" : "Edit"}
                        </BrutalButton>
                        <Link
                          to="/events/$eventId"
                          params={{ eventId: ev.id }}
                          className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-3 py-2 text-sm font-bold brutal-press"
                        >
                          View Public
                        </Link>
                        <BrutalButton
                          variant="danger"
                          onClick={() => consoleState.requestDelete(ev.id)}
                        >
                          <Trash size={16} weight="bold" />
                          Delete
                        </BrutalButton>
                      </div>
                    )}
                  </div>
                </BrutalCard>
              );
            })}
          </div>
        </div>

        <div className="lg:col-span-7">
          <OrganizerEventForm
            isEditing={isEditing}
            editingEventTitle={consoleState.editingEventTitle}
            title={consoleState.title}
            onTitleChange={consoleState.setTitle}
            venue={consoleState.venue}
            onVenueChange={consoleState.setVenue}
            description={consoleState.description}
            onDescriptionChange={consoleState.setDescription}
            eventDateLocal={consoleState.eventDateLocal}
            onEventDateChange={consoleState.setEventDateLocal}
            tiers={consoleState.tiers}
            onUpdateTier={consoleState.updateTier}
            onAddTier={consoleState.addTier}
            onRemoveTier={consoleState.removeTier}
            onCancelEdit={consoleState.cancelEdit}
            onSubmit={consoleState.handleSubmit}
            isSubmitting={consoleState.isSubmitting}
            submitError={consoleState.submitError}
          />
        </div>
      </div>
    </div>
  );
}
