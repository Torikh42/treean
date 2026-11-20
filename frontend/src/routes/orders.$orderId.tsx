import { createFileRoute } from "@tanstack/react-router";
import { OrderDetailCard } from "@/components/orders/OrderDetailCard";
import { useOrderDetail } from "@/hooks/useOrderDetail";
import { requireRole } from "@/lib/auth-guards";

export const Route = createFileRoute("/orders/$orderId")({
  beforeLoad: ({ location }) => {
    requireRole("BUYER", location.href);
  },
  component: OrderDetailPage,
});

function OrderDetailPage() {
  const { orderId } = Route.useParams();
  const detail = useOrderDetail(orderId);

  return (
    <OrderDetailCard
      order={detail.order}
      isLoading={detail.isLoading}
      isFetching={detail.isFetching}
      error={detail.error}
      actionError={detail.actionError}
      onRetry={() => void detail.refetch()}
      countdown={detail.countdown}
      onPay={detail.payOrder}
      onCancel={detail.cancelOrder}
      isPaying={detail.isPaying}
      isCancelling={detail.isCancelling}
    />
  );
}
