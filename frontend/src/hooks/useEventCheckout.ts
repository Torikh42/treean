import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  createOrderMutation,
  getEventOptions,
  getEventQueryKey,
  listEventsQueryKey,
  listMyOrdersQueryKey,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import { parseApiError, type ParsedApiError } from "@/lib/api/errors";
import { getActiveRole, isAuthenticated, useAuthStore } from "@/store/auth-store";

export const MAX_TICKETS_PER_ORDER = 4;

function generateIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `ord-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

export function useEventCheckout(eventId: string) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const auth = useAuthStore();
  const authenticated = isAuthenticated(auth);
  const activeRole = getActiveRole(auth);
  const [quantities, setQuantities] = useState<Record<string, number>>({});
  const [submitError, setSubmitError] = useState<ParsedApiError | null>(null);
  const idempotencyKeyRef = useRef<string | null>(null);

  const eventQuery = useQuery(getEventOptions({ path: { id: eventId } }));

  const createMut = useMutation({
    ...createOrderMutation(),
    onSuccess: (order) => {
      idempotencyKeyRef.current = null;
      setSubmitError(null);
      setQuantities({});
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getEventQueryKey({ path: { id: eventId } }) });
      void queryClient.invalidateQueries({ queryKey: listMyOrdersQueryKey() });
      toast.success(`Reserved ${order.total_tickets} ticket(s) - 15-minute hold active`);
      void navigate({ to: "/orders/$orderId", params: { orderId: order.id } });
    },
    onError: (err) => {
      const parsed = parseApiError(err);
      setSubmitError(parsed);
      toast.error(`${parsed.code}: ${parsed.detail}`);
    },
  });

  const tiers = eventQuery.data?.ticket_types ?? [];
  const totalSelected = tiers.reduce((sum, t) => sum + (quantities[t.id] ?? 0), 0);
  const totalAmountIDR = tiers.reduce(
    (sum, t) => sum + (quantities[t.id] ?? 0) * t.price_idr,
    0,
  );

  function updateQuantity(tierId: string, delta: number, maxQuota: number) {
    idempotencyKeyRef.current = null;
    setSubmitError(null);
    setQuantities((prev) => {
      const current = prev[tierId] ?? 0;
      const otherSelected = totalSelected - current;
      const maxAllowedForTier = Math.min(maxQuota, MAX_TICKETS_PER_ORDER - otherSelected);
      const next = Math.max(0, Math.min(maxAllowedForTier, current + delta));
      return { ...prev, [tierId]: next };
    });
  }

  function submitCheckout() {
    if (!authenticated) {
      setSubmitError({
        status: 401,
        code: "UNAUTHORIZED",
        detail: "Sign in as a Buyer account before reserving tickets.",
        actionHint: "Use Sign In as Buyer to Reserve and authenticate with a Buyer account.",
      });
      return;
    }

    if (activeRole !== "BUYER") {
      setSubmitError({
        status: 403,
        code: "FORBIDDEN",
        detail: "Ticket checkout requires a Buyer account.",
        actionHint: "Organizer accounts cannot purchase buyer tickets. Sign in with a Buyer account.",
      });
      return;
    }

    const items = tiers
      .map((t) => ({ ticket_type_id: t.id, quantity: quantities[t.id] ?? 0 }))
      .filter((item) => item.quantity > 0);

    if (items.length === 0) {
      setSubmitError({
        status: 422,
        code: "VALIDATION_ERROR",
        detail: "Select at least 1 ticket before submitting.",
        actionHint: "Use + buttons on a tier with available quota.",
      });
      return;
    }

    idempotencyKeyRef.current ??= generateIdempotencyKey();
    createMut.mutate({
      headers: {
        "Idempotency-Key": idempotencyKeyRef.current,
      },
      body: {
        event_id: eventId,
        items,
      },
    });
  }

  return {
    event: eventQuery.data,
    tiers,
    authenticated,
    activeRole,
    isLoading: eventQuery.isLoading,
    error: eventQuery.error,
    refetch: eventQuery.refetch,
    quantities,
    totalSelected,
    totalAmountIDR,
    updateQuantity,
    submitCheckout,
    isSubmitting: createMut.isPending,
    submitError,
  };
}
