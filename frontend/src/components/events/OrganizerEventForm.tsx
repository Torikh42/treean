import { Plus, Trash, X } from "@phosphor-icons/react";
import type { ParsedApiError } from "@/lib/api/errors";
import {
  MAX_EVENT_TIERS,
  MIN_EVENT_TIERS,
  type TierDraft,
} from "@/lib/organizer-event-helpers";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface OrganizerEventFormProps {
  isEditing: boolean;
  editingEventTitle: string | null;
  title: string;
  onTitleChange: (val: string) => void;
  venue: string;
  onVenueChange: (val: string) => void;
  description: string;
  onDescriptionChange: (val: string) => void;
  eventDateLocal: string;
  onEventDateChange: (val: string) => void;
  tiers: ReadonlyArray<TierDraft>;
  onUpdateTier: (index: number, patch: Partial<TierDraft>) => void;
  onAddTier: () => void;
  onRemoveTier: (index: number) => void;
  onCancelEdit: () => void;
  onSubmit: () => void;
  isSubmitting: boolean;
  submitError: ParsedApiError | null;
}

export function OrganizerEventForm({
  isEditing,
  editingEventTitle,
  title,
  onTitleChange,
  venue,
  onVenueChange,
  description,
  onDescriptionChange,
  eventDateLocal,
  onEventDateChange,
  tiers,
  onUpdateTier,
  onAddTier,
  onRemoveTier,
  onCancelEdit,
  onSubmit,
  isSubmitting,
  submitError,
}: OrganizerEventFormProps) {
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3 border-2 border-ink bg-surface p-4 brutal-shadow-sm">
        <div>
          <p className="font-mono text-xs font-bold uppercase text-ink-muted">
            {isEditing ? "Edit Mode" : "Create Mode"}
          </p>
          <h2 className="text-xl font-bold tracking-tight">
            {isEditing ? `Editing: ${editingEventTitle ?? "Event"}` : "Publish New Event"}
          </h2>
        </div>

        {isEditing && (
          <BrutalButton variant="secondary" onClick={onCancelEdit}>
            <X size={16} weight="bold" />
            Cancel Edit
          </BrutalButton>
        )}
      </div>

      {submitError && (
        <StateNotice
          kind="error"
          title={isEditing ? "Could not update event" : "Could not create event"}
          error={submitError}
        />
      )}

      <form
        className="space-y-5"
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
      >
        <BrutalCard className="grid gap-4 sm:grid-cols-2">
          <label className="block text-xs font-bold uppercase sm:col-span-2">
            Event Title
            <input
              type="text"
              value={title}
              onChange={(e) => onTitleChange(e.target.value)}
              placeholder="Indie Music Fest 2026"
              className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 text-sm font-normal normal-case"
            />
          </label>

          <label className="block text-xs font-bold uppercase">
            Venue
            <input
              type="text"
              value={venue}
              onChange={(e) => onVenueChange(e.target.value)}
              placeholder="Senayan Hall"
              className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 text-sm font-normal normal-case"
            />
          </label>

          <label className="block text-xs font-bold uppercase">
            Event Date & Time
            <input
              type="datetime-local"
              value={eventDateLocal}
              onChange={(e) => onEventDateChange(e.target.value)}
              className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 font-mono text-sm font-normal"
            />
          </label>

          <label className="block text-xs font-bold uppercase sm:col-span-2">
            Description (optional)
            <textarea
              rows={2}
              value={description}
              onChange={(e) => onDescriptionChange(e.target.value)}
              placeholder="Ticket release rules or venue notes..."
              className="mt-1 w-full border-2 border-ink bg-canvas p-3 text-sm font-normal normal-case"
            />
          </label>
        </BrutalCard>

        <BrutalCard>
          <div className="flex flex-wrap items-center justify-between gap-3 border-b-2 border-ink pb-3">
            <div>
              <h3 className="text-lg font-bold">
                Ticket Tiers ({MIN_EVENT_TIERS} to {MAX_EVENT_TIERS})
              </h3>
              <p className="text-xs text-ink-muted">
                Each tier tracks independent quota and IDR pricing.
              </p>
            </div>
            <BrutalButton
              variant="secondary"
              disabled={tiers.length >= MAX_EVENT_TIERS}
              onClick={onAddTier}
            >
              <Plus size={16} weight="bold" />
              Add Tier
            </BrutalButton>
          </div>

          <div className="mt-4 space-y-3">
            {tiers.map((tier, idx) => {
              const soldOrHeld = tier.sold_or_held ?? 0;
              const minQuota = Math.max(1, soldOrHeld);

              return (
                <div
                  key={tier.id ?? `new-tier-${idx}`}
                  className="space-y-2 border-2 border-ink bg-canvas p-3"
                >
                  <div className="grid items-end gap-3 sm:grid-cols-4">
                    <label className="block text-xs font-bold uppercase sm:col-span-2">
                      Tier Name
                      <input
                        type="text"
                        value={tier.name}
                        onChange={(e) => onUpdateTier(idx, { name: e.target.value })}
                        placeholder="Reguler"
                        className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm font-normal normal-case"
                      />
                    </label>
                    <label className="block text-xs font-bold uppercase">
                      Price (IDR)
                      <input
                        type="number"
                        min={0}
                        value={tier.price_idr}
                        onChange={(e) => onUpdateTier(idx, { price_idr: Number(e.target.value) })}
                        className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-sm font-normal"
                      />
                    </label>
                    <div className="flex items-end gap-2">
                      <label className="block flex-1 text-xs font-bold uppercase">
                        Total Quota
                        <input
                          type="number"
                          min={minQuota}
                          max={100000}
                          value={tier.total_quota}
                          onChange={(e) =>
                            onUpdateTier(idx, { total_quota: Number(e.target.value) })
                          }
                          className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-sm font-normal"
                        />
                      </label>
                      <BrutalButton
                        variant="danger"
                        disabled={tiers.length <= MIN_EVENT_TIERS}
                        aria-label={`Remove tier ${tier.name || idx + 1}`}
                        onClick={() => onRemoveTier(idx)}
                      >
                        <Trash size={16} weight="bold" />
                      </BrutalButton>
                    </div>
                  </div>

                  {isEditing && (
                    <div className="flex flex-wrap items-center justify-between gap-2 font-mono text-xs text-ink-muted">
                      <span>Min quota: {soldOrHeld} (sold/reserved)</span>
                      {soldOrHeld > 0 && (
                        <span className="font-bold text-status-wait-ink">
                          Has active/paid orders (cannot remove tier)
                        </span>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </BrutalCard>

        <div className="flex flex-wrap gap-3">
          <BrutalButton type="submit" variant="signal" disabled={isSubmitting}>
            {isSubmitting
              ? isEditing
                ? "Saving Changes..."
                : "Publishing Event..."
              : isEditing
                ? "Save Changes"
                : "Publish Event"}
          </BrutalButton>
          {isEditing && (
            <BrutalButton variant="secondary" disabled={isSubmitting} onClick={onCancelEdit}>
              Discard Changes
            </BrutalButton>
          )}
        </div>
      </form>
    </div>
  );
}
