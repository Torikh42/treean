import { createFileRoute } from "@tanstack/react-router";
import { OrderDetailCard } from "@/components/orders/OrderDetailCard";
import { useOrderDetail } from "@/hooks/useOrderDetail";

export const Route = createFileRoute("/orders/$orderId")({
  component: OrderDetailPage,
});

function OrderDetailPage() {
  const { orderId } = Route.useParams();
  const detail = useOrderDetail(orderId);

  return (
    <OrderDetailCard
      order={detail.order}
      isLoading={detail.isLoading}
      error={detail.error}
      onRetry={() => void detail.refetch()}
      countdown={detail.countdown}
      onPay={detail.payOrder}
      onCancel={detail.cancelOrder}
      isPaying={detail.isPaying}
      isCancelling={detail.isCancelling}
    />
  );
}
