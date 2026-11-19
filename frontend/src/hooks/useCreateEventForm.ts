import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { z } from "zod";
import {
  createEventMutation,
  listEventsQueryKey,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import { parseApiError, type ParsedApiError } from "@/lib/api/errors";
import { getActiveRole, useAuthStore } from "@/store/auth-store";

export interface TierDraft {
  name: string;
  price_idr: number;
  total_quota: number;
}

const createEventSchema = z.object({
  title: z.string().trim().min(3, "Event title must be at least 3 characters").max(150),
  venue: z.string().trim().min(2, "Venue must be at least 2 characters").max(150),
  description: z.string().max(1000).optional(),
  event_date: z.string().min(1, "Event date/time is required"),
  ticket_types: z
    .array(
      z.object({
        name: z.string().trim().min(1, "Tier name required").max(60),
        price_idr: z.number().int().min(0, "Price must be >= 0"),
        total_quota: z.number().int().min(1, "Quota must be >= 1").max(100000),
      }),
    )
    .min(2, "At least 2 ticket tiers are required"),
});

function defaultLocalDatetimeValue(): string {
  const future = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${future.getFullYear()}-${pad(future.getMonth() + 1)}-${pad(future.getDate())}T19:00`;
}

export function useCreateEventForm() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const auth = useAuthStore();

  const [title, setTitle] = useState("");
  const [venue, setVenue] = useState("");
  const [description, setDescription] = useState("");
  const [eventDateLocal, setEventDateLocal] = useState(defaultLocalDatetimeValue);
  const [tiers, setTiers] = useState<TierDraft[]>([
    { name: "Reguler", price_idr: 150000, total_quota: 80 },
    { name: "VIP", price_idr: 350000, total_quota: 20 },
  ]);
  const [submitError, setSubmitError] = useState<ParsedApiError | null>(null);

  const createMut = useMutation({
    ...createEventMutation(),
    onSuccess: (created) => {
      setSubmitError(null);
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
      toast.success(`Created event "${created.title}"`);
      void navigate({ to: "/events/$eventId", params: { eventId: created.id } });
    },
    onError: (err) => {
      const parsed = parseApiError(err);
      setSubmitError(parsed);
      toast.error(`${parsed.code}: ${parsed.detail}`);
    },
  });

  function updateTier(index: number, patch: Partial<TierDraft>) {
    setTiers((prev) => prev.map((tier, i) => (i === index ? { ...tier, ...patch } : tier)));
  }

  function addTier() {
    if (tiers.length >= 10) return;
    setTiers((prev) => [...prev, { name: "", price_idr: 250000, total_quota: 25 }]);
  }

  function removeTier(index: number) {
    if (tiers.length <= 2) return;
    setTiers((prev) => prev.filter((_, i) => i !== index));
  }

  function handleSubmit() {
    setSubmitError(null);
    const parsedDate = new Date(eventDateLocal);
    if (Number.isNaN(parsedDate.getTime())) {
      setSubmitError({
        status: 422,
        code: "VALIDATION_ERROR",
        detail: "Invalid event date/time format",
        actionHint: "Pick a valid future date and time.",
      });
      return;
    }

    const result = createEventSchema.safeParse({
      title,
      venue,
      description,
      event_date: parsedDate.toISOString(),
      ticket_types: tiers,
    });

    if (!result.success) {
      setSubmitError({
        status: 422,
        code: "VALIDATION_ERROR",
        detail: result.error.issues[0]?.message ?? "Check event inputs",
        actionHint: "Ensure title, venue, future date, and at least 2 valid tiers.",
      });
      return;
    }

    createMut.mutate({ body: result.data });
  }

  return {
    activeRole: getActiveRole(auth),
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
    isSubmitting: createMut.isPending,
    submitError,
  };
}
