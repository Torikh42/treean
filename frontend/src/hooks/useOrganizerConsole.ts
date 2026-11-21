import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  createEventMutation,
  deleteEventMutation,
  getEventQueryKey,
  listEventsQueryKey,
  listOrganizerEventsOptions,
  listOrganizerEventsQueryKey,
  updateEventMutation,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import type { Event } from "@/lib/api/generated/types.gen";
import { toastApiError, type ParsedApiError } from "@/lib/api/errors";
import {
  buildCreateEventBody,
  buildUpdateEventBody,
  DEFAULT_TIER_DRAFTS,
  MAX_EVENT_TIERS,
  MIN_EVENT_TIERS,
  mapEventToTierDrafts,
  toLocalDatetimeValue,
  type TierDraft,
} from "@/lib/organizer-event-helpers";
import { getActiveRole, isAuthenticated, useAuthStore } from "@/store/auth-store";

export function useOrganizerConsole() {
  const queryClient = useQueryClient();
  const auth = useAuthStore();
  const activeRole = getActiveRole(auth);
  const enabled = isAuthenticated(auth) && activeRole === "ORGANIZER";

  const eventsQuery = useQuery({
    ...listOrganizerEventsOptions(),
    enabled,
  });

  const [editingEventId, setEditingEventId] = useState<string | null>(null);
  const [editingEventTitle, setEditingEventTitle] = useState<string | null>(null);
  const [title, setTitle] = useState("");
  const [venue, setVenue] = useState("");
  const [description, setDescription] = useState("");
  const [eventDateLocal, setEventDateLocal] = useState(() => toLocalDatetimeValue());
  const [tiers, setTiers] = useState<TierDraft[]>(() => DEFAULT_TIER_DRAFTS.map((t) => ({ ...t })));
  const [submitError, setSubmitError] = useState<ParsedApiError | null>(null);

  const [deleteConfirmId, setDeleteConfirmId] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<{
    eventId: string;
    error: ParsedApiError;
  } | null>(null);

  function resetToCreateMode() {
    setEditingEventId(null);
    setEditingEventTitle(null);
    setTitle("");
    setVenue("");
    setDescription("");
    setEventDateLocal(toLocalDatetimeValue());
    setTiers(DEFAULT_TIER_DRAFTS.map((t) => ({ ...t })));
    setSubmitError(null);
  }

  function startEdit(ev: Event) {
    setEditingEventId(ev.id);
    setEditingEventTitle(ev.title);
    setTitle(ev.title);
    setVenue(ev.venue);
    setDescription(ev.description ?? "");
    setEventDateLocal(toLocalDatetimeValue(ev.event_date));
    const mappedTiers = mapEventToTierDrafts(ev.ticket_types);
    setTiers(
      mappedTiers.length >= MIN_EVENT_TIERS
        ? mappedTiers
        : DEFAULT_TIER_DRAFTS.map((t) => ({ ...t })),
    );
    setSubmitError(null);
    setDeleteConfirmId(null);
  }

  const createMut = useMutation({
    ...createEventMutation(),
    onSuccess: (created) => {
      void queryClient.invalidateQueries({ queryKey: listOrganizerEventsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
      resetToCreateMode();
      toast.success(`Published "${created.title}"`);
    },
    onError: (err) => {
      setSubmitError(toastApiError(err));
    },
  });

  const updateMut = useMutation({
    ...updateEventMutation(),
    onSuccess: (updated) => {
      void queryClient.invalidateQueries({ queryKey: listOrganizerEventsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
      void queryClient.invalidateQueries({
        queryKey: getEventQueryKey({ path: { id: updated.id } }),
      });
      resetToCreateMode();
      toast.success(`Updated "${updated.title}"`);
    },
    onError: (err) => {
      setSubmitError(toastApiError(err));
    },
  });

  const deleteMut = useMutation({
    ...deleteEventMutation(),
    onSuccess: (_data, variables) => {
      const deletedId = variables.path.id;
      void queryClient.invalidateQueries({ queryKey: listOrganizerEventsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
      void queryClient.invalidateQueries({
        queryKey: getEventQueryKey({ path: { id: deletedId } }),
      });
      setDeleteConfirmId(null);
      setDeleteError(null);
      if (editingEventId === deletedId) {
        resetToCreateMode();
      }
      toast.success("Deleted unused event");
    },
    onError: (err, variables) => {
      const parsed = toastApiError(err);
      setDeleteConfirmId(null);
      setDeleteError({
        eventId: variables.path.id,
        error: parsed,
      });
    },
  });

  function updateTier(index: number, patch: Partial<TierDraft>) {
    setSubmitError(null);
    setTiers((prev) => prev.map((tier, i) => (i === index ? { ...tier, ...patch } : tier)));
  }

  function addTier() {
    if (tiers.length >= MAX_EVENT_TIERS) return;
    setSubmitError(null);
    setTiers((prev) => [
      ...prev,
      { name: "", price_idr: 250000, total_quota: 25, sold_or_held: 0 },
    ]);
  }

  function removeTier(index: number) {
    if (tiers.length <= MIN_EVENT_TIERS) return;
    if ((tiers[index]?.sold_or_held ?? 0) > 0) return;
    setSubmitError(null);
    setTiers((prev) => {
      if (prev.length <= MIN_EVENT_TIERS || (prev[index]?.sold_or_held ?? 0) > 0) return prev;
      return prev.filter((_, i) => i !== index);
    });
  }

  function handleSubmit() {
    setSubmitError(null);
    const input = {
      title,
      venue,
      description,
      eventDateLocal,
      tiers,
    };

    if (editingEventId) {
      const built = buildUpdateEventBody(input);
      if (!built.ok) {
        setSubmitError(built.error);
        return;
      }
      updateMut.mutate({
        path: { id: editingEventId },
        body: built.body,
      });
      return;
    }

    const built = buildCreateEventBody(input);
    if (!built.ok) {
      setSubmitError(built.error);
      return;
    }
    createMut.mutate({ body: built.body });
  }

  function requestDelete(eventId: string) {
    setDeleteError(null);
    setDeleteConfirmId(eventId);
  }

  function cancelDelete() {
    setDeleteConfirmId(null);
  }

  function confirmDelete(eventId: string) {
    setDeleteError(null);
    deleteMut.mutate({ path: { id: eventId } });
  }

  return {
    events: eventsQuery.data ?? [],
    isLoadingEvents: eventsQuery.isLoading,
    isFetchingEvents: eventsQuery.isFetching,
    eventsError: eventsQuery.error,
    refetchEvents: () => void eventsQuery.refetch(),
    editingEventId,
    editingEventTitle,
    title,
    setTitle,
    venue,
    setVenue,
    description,
    setDescription,
    eventDateLocal,
    setEventDateLocal,
    tiers,
    updateTier,
    addTier,
    removeTier,
    handleSubmit,
    startEdit,
    cancelEdit: resetToCreateMode,
    isSubmitting: createMut.isPending || updateMut.isPending,
    submitError,
    deleteConfirmId,
    deleteError,
    isDeleting: deleteMut.isPending,
    requestDelete,
    cancelDelete,
    confirmDelete,
  };
}
