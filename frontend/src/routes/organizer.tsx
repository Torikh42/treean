import { createFileRoute } from "@tanstack/react-router";
import { OrganizerEventForm } from "@/components/events/OrganizerEventForm";
import { useCreateEventForm } from "@/hooks/useCreateEventForm";

export const Route = createFileRoute("/organizer")({
  component: OrganizerPage,
});

function OrganizerPage() {
  const form = useCreateEventForm();

  return (
    <OrganizerEventForm
      activeRole={form.activeRole}
      title={form.title}
      onTitleChange={form.setTitle}
      venue={form.venue}
      onVenueChange={form.setVenue}
      description={form.description}
      onDescriptionChange={form.setDescription}
      eventDateLocal={form.eventDateLocal}
      onEventDateChange={form.setEventDateLocal}
      tiers={form.tiers}
      onUpdateTier={form.updateTier}
      onAddTier={form.addTier}
      onRemoveTier={form.removeTier}
      onSubmit={form.handleSubmit}
      isSubmitting={form.isSubmitting}
      submitError={form.submitError}
    />
  );
}
