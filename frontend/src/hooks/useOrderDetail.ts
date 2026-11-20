import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  cancelOrderMutation,
  getEventQueryKey,
  getOrderOptions,
  getOrderQueryKey,
  listEventsQueryKey,
  listMyOrdersQueryKey,
  payOrderDemoMutation,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import { toastApiError, type ParsedApiError } from "@/lib/api/errors";
import { useCountdown } from "@/hooks/useCountdown";

export function useOrderDetail(orderId: string) {
  const queryClient = useQueryClient();
  const [actionError, setActionError] = useState<ParsedApiError | null>(null);
  const orderQuery = useQuery(getOrderOptions({ path: { id: orderId } }));
  const order = orderQuery.data;
  const isWaiting = order?.status === "WAITING_PAYMENT";
  const countdown = useCountdown(order?.expires_at, Boolean(isWaiting));

  function invalidateOrderScope(eventId?: string) {
    void queryClient.invalidateQueries({ queryKey: getOrderQueryKey({ path: { id: orderId } }) });
    void queryClient.invalidateQueries({ queryKey: listMyOrdersQueryKey() });
    void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
    if (eventId) {
      void queryClient.invalidateQueries({ queryKey: getEventQueryKey({ path: { id: eventId } }) });
    }
  }

  const payMut = useMutation({
    ...payOrderDemoMutation(),
    onSuccess: (res) => {
      setActionError(null);
      invalidateOrderScope(res.order?.event_id ?? order?.event_id);
      toast.success(`Payment settled (${res.outcome}) - tickets issued`);
    },
    onError: (err) => {
      setActionError(toastApiError(err));
      invalidateOrderScope(order?.event_id);
    },
  });

  const cancelMut = useMutation({
    ...cancelOrderMutation(),
    onSuccess: (updated) => {
      setActionError(null);
      invalidateOrderScope(updated.event_id);
      toast.success("Order cancelled and ticket quota restored");
    },
    onError: (err) => {
      setActionError(toastApiError(err));
      invalidateOrderScope(order?.event_id);
    },
  });

  return {
    order,
    isLoading: orderQuery.isLoading,
    isFetching: orderQuery.isFetching,
    error: orderQuery.error,
    actionError,
    refetch: () => {
      setActionError(null);
      return orderQuery.refetch();
    },
    countdown,
    payOrder: () => {
      setActionError(null);
      payMut.mutate({ path: { id: orderId } });
    },
    cancelOrder: () => {
      setActionError(null);
      cancelMut.mutate({ path: { id: orderId } });
    },
    isPaying: payMut.isPending,
    isCancelling: cancelMut.isPending,
  };
}
