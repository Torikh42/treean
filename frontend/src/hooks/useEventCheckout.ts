import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  createOrderMutation,
  getEventOptions,
  getEventQueryKey,
  listEventsQueryKey,
  listMyOrdersOptions,
  listMyOrdersQueryKey,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import { computeExistingEventTickets, computeRemainingBuyerAllowance } from "@/lib/allowance";
import { toastApiError, type ParsedApiError } from "@/lib/api/errors";
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
  const isBuyer = authenticated && activeRole === "BUYER";

  const [scopedState, setScopedState] = useState<{
    eventId: string;
    quantities: Record<string, number>;
    submitError: ParsedApiError | null;
  }>({
    eventId,
    quantities: {},
    submitError: null,
  });
  const idempotencyKeyRef = useRef<{ eventId: string; key: string } | null>(null);

  const quantities = scopedState.eventId === eventId ? scopedState.quantities : {};
  const submitError = scopedState.eventId === eventId ? scopedState.submitError : null;

  function setSubmitError(error: ParsedApiError | null) {
    setScopedState((prev) => ({
      eventId,
      quantities: prev.eventId === eventId ? prev.quantities : {},
      submitError: error,
    }));
  }

  function resetQuantitiesAndError() {
    setScopedState({
      eventId,
      quantities: {},
      submitError: null,
    });
  }

  const eventQuery = useQuery(getEventOptions({ path: { id: eventId } }));
  const myOrdersQuery = useQuery({
    ...listMyOrdersOptions(),
    enabled: isBuyer,
  });

  const existingEventTickets = isBuyer
    ? computeExistingEventTickets(myOrdersQuery.data, eventId)
    : 0;
  const remainingAllowance = isBuyer
    ? computeRemainingBuyerAllowance(myOrdersQuery.data, eventId, MAX_TICKETS_PER_ORDER)
    : MAX_TICKETS_PER_ORDER;

  const createMut = useMutation({
    ...createOrderMutation(),
    onSuccess: (order) => {
      idempotencyKeyRef.current = null;
      resetQuantitiesAndError();
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey(), refetchType: "none" });
      void queryClient.invalidateQueries({
        queryKey: getEventQueryKey({ path: { id: eventId } }),
        refetchType: "none",
      });
      void queryClient.invalidateQueries({ queryKey: listMyOrdersQueryKey(), refetchType: "none" });
      toast.success(`Reserved ${order.total_tickets} ticket(s) - 15-minute hold active`);
      void navigate({ to: "/orders/$orderId", params: { orderId: order.id } });
    },
    onError: (err) => {
      const parsed = toastApiError(err);
      setSubmitError(parsed);
      void queryClient.invalidateQueries({ queryKey: listMyOrdersQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getEventQueryKey({ path: { id: eventId } }) });
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
    setScopedState((prev) => {
      const baseQuantities = prev.eventId === eventId ? prev.quantities : {};
      const current = baseQuantities[tierId] ?? 0;
      const prevTotal = Object.values(baseQuantities).reduce((sum, q) => sum + q, 0);
      const otherSelected = prevTotal - current;
      const maxAllowedForTier = Math.min(maxQuota, remainingAllowance - otherSelected);
      const next = Math.max(0, Math.min(maxAllowedForTier, current + delta));
      return {
        eventId,
        quantities: { ...baseQuantities, [tierId]: next },
        submitError: null,
      };
    });
  }

  function submitCheckout() {
    if (createMut.isPending) return;
    if (!authenticated) {
      setSubmitError({
        status: 401,
        code: "UNAUTHORIZED",
        detail: "Sign in as a Buyer account before reserving tickets.",
        userTitle: "Buyer sign in required",
        userMessage: "Sign in with a Buyer account before reserving tickets.",
        actionHint: "Use Sign In as Buyer to Reserve below.",
      });
      return;
    }

    if (activeRole !== "BUYER") {
      setSubmitError({
        status: 403,
        code: "FORBIDDEN",
        detail: "Ticket checkout requires a Buyer account.",
        userTitle: "Buyer account required",
        userMessage: "Organizer accounts cannot reserve or purchase tickets.",
        actionHint: "Sign out and sign in with a Buyer account.",
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
        userTitle: "No tickets selected",
        userMessage: "Select at least 1 ticket before reserving.",
        actionHint: "Use + on a tier with available quota.",
      });
      return;
    }

    if (totalSelected > remainingAllowance) {
      setSubmitError({
        status: 409,
        code: "BUYER_LIMIT_EXCEEDED",
        detail: `active/paid=${existingEventTickets}, requested=${totalSelected}, max=${MAX_TICKETS_PER_ORDER}`,
        userTitle: "Ticket limit reached for this event",
        userMessage:
          remainingAllowance === 0
            ? `You already hold or purchased all ${MAX_TICKETS_PER_ORDER} allowed tickets for this event.`
            : `You can only reserve ${remainingAllowance} more ticket(s) for this event (currently using ${existingEventTickets} of ${MAX_TICKETS_PER_ORDER}).`,
        actionHint: "Lower your selected quantity or cancel an unpaid order in My Orders.",
      });
      return;
    }

    if (!idempotencyKeyRef.current || idempotencyKeyRef.current.eventId !== eventId) {
      idempotencyKeyRef.current = { eventId, key: generateIdempotencyKey() };
    }
    createMut.mutate({
      headers: {
        "Idempotency-Key": idempotencyKeyRef.current.key,
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
    existingEventTickets,
    remainingAllowance,
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
