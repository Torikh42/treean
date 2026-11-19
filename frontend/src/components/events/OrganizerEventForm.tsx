import { Plus, Trash } from "@phosphor-icons/react";
import type { TierDraft } from "@/hooks/useCreateEventForm";
import type { ParsedApiError } from "@/lib/api/errors";
import type { DemoRole } from "@/store/auth-store";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface OrganizerEventFormProps {
  activeRole: DemoRole | null;
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
  onSubmit: () => void;
  isSubmitting: boolean;
  submitError: ParsedApiError | null;
}

export function OrganizerEventForm({
  activeRole,
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
  onSubmit,
  isSubmitting,
  submitError,
}: OrganizerEventFormProps) {
  return (
    <div className="space-y-6">
      <div className="border-b-3 border-ink pb-4">
        <h1 className="text-3xl font-bold tracking-tight">Organizer Event Publisher</h1>
        <p className="mt-1 text-sm text-ink-muted">
          Create an event with at least 2 distinct ticket tiers (requires ORGANIZER role).
        </p>
      </div>

      {activeRole !== "ORGANIZER" && (
        <StateNotice
          kind="error"
          title={`Current role is ${activeRole ?? "UNAUTHENTICATED"}`}
          detail="Creating an event requires ORGANIZER authorization. Open the top-right identity switcher and select Demo Organizer."
        />
      )}

      {submitError && <StateNotice kind="error" title="Could not create event" error={submitError} />}

      <form
        className="space-y-6"
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
              className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 text-sm"
            />
          </label>

          <label className="block text-xs font-bold uppercase">
            Venue
            <input
              type="text"
              value={venue}
              onChange={(e) => onVenueChange(e.target.value)}
              placeholder="Senayan Hall"
              className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 text-sm"
            />
          </label>

          <label className="block text-xs font-bold uppercase">
            Event Date & Time
            <input
              type="datetime-local"
              value={eventDateLocal}
              onChange={(e) => onEventDateChange(e.target.value)}
              className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 font-mono text-sm"
            />
          </label>

          <label className="block text-xs font-bold uppercase sm:col-span-2">
            Description (optional)
            <textarea
              rows={2}
              value={description}
              onChange={(e) => onDescriptionChange(e.target.value)}
              placeholder="Ticket release rules or venue notes..."
              className="mt-1 w-full border-2 border-ink bg-canvas p-3 text-sm"
            />
          </label>
        </BrutalCard>

        <BrutalCard>
          <div className="flex flex-wrap items-center justify-between gap-3 border-b-2 border-ink pb-3">
            <div>
              <h2 className="text-xl font-bold">Ticket Tiers (Min 2)</h2>
              <p className="text-xs text-ink-muted">Each tier tracks independent remaining quota and IDR pricing.</p>
            </div>
            <BrutalButton variant="secondary" disabled={tiers.length >= 10} onClick={onAddTier}>
              <Plus size={16} weight="bold" />
              Add Tier
            </BrutalButton>
          </div>

          <div className="mt-4 space-y-3">
            {tiers.map((tier, idx) => (
              <div key={idx} className="grid items-end gap-3 border-2 border-ink bg-canvas p-3 sm:grid-cols-4">
                <label className="block text-xs font-bold uppercase sm:col-span-2">
                  Tier Name
                  <input
                    type="text"
                    value={tier.name}
                    onChange={(e) => onUpdateTier(idx, { name: e.target.value })}
                    placeholder="Reguler"
                    className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm"
                  />
                </label>
                <label className="block text-xs font-bold uppercase">
                  Price (IDR)
                  <input
                    type="number"
                    min={0}
                    value={tier.price_idr}
                    onChange={(e) => onUpdateTier(idx, { price_idr: Number(e.target.value) })}
                    className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-sm"
                  />
                </label>
                <div className="flex items-end gap-2">
                  <label className="block flex-1 text-xs font-bold uppercase">
                    Quota
                    <input
                      type="number"
                      min={1}
                      max={100000}
                      value={tier.total_quota}
                      onChange={(e) => onUpdateTier(idx, { total_quota: Number(e.target.value) })}
                      className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-sm"
                    />
                  </label>
                  <BrutalButton
                    variant="danger"
                    disabled={tiers.length <= 2}
                    aria-label={`Remove tier ${idx + 1}`}
                    onClick={() => onRemoveTier(idx)}
                  >
                    <Trash size={16} weight="bold" />
                  </BrutalButton>
                </div>
              </div>
            ))}
          </div>
        </BrutalCard>

        <BrutalButton type="submit" variant="signal" disabled={isSubmitting}>
          {isSubmitting ? "Publishing Event..." : "Publish Event"}
        </BrutalButton>
      </form>
    </div>
  );
}
