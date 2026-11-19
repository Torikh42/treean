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
import { parseApiError } from "@/lib/api/errors";
import { useCountdown } from "@/hooks/useCountdown";

export function useOrderDetail(orderId: string) {
  const queryClient = useQueryClient();
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
      invalidateOrderScope(res.order?.event_id ?? order?.event_id);
      toast.success(`Payment settled (${res.outcome}) - tickets issued`);
    },
    onError: (err) => {
      const parsed = parseApiError(err);
      toast.error(`${parsed.code}: ${parsed.detail}`);
    },
  });

  const cancelMut = useMutation({
    ...cancelOrderMutation(),
    onSuccess: (updated) => {
      invalidateOrderScope(updated.event_id);
      toast.success("Order cancelled and ticket quota restored");
    },
    onError: (err) => {
      const parsed = parseApiError(err);
      toast.error(`${parsed.code}: ${parsed.detail}`);
    },
  });

  return {
    order,
    isLoading: orderQuery.isLoading,
    error: orderQuery.error,
    refetch: orderQuery.refetch,
    countdown,
    payOrder: () => payMut.mutate({ path: { id: orderId } }),
    cancelOrder: () => cancelMut.mutate({ path: { id: orderId } }),
    isPaying: payMut.isPending,
    isCancelling: cancelMut.isPending,
  };
}
