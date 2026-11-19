import { createFileRoute } from "@tanstack/react-router";
import { OrdersHistoryTable } from "@/components/orders/OrdersHistoryTable";
import { useOrdersList } from "@/hooks/useOrdersList";
import { requireRole } from "@/lib/auth-guards";

export const Route = createFileRoute("/orders/")({
  beforeLoad: ({ location }) => {
    requireRole("BUYER", location.href);
  },
  component: OrdersListPage,
});

function OrdersListPage() {
  const ordersList = useOrdersList();

  return (
    <OrdersHistoryTable
      orders={ordersList.orders}
      enabled={ordersList.enabled}
      isLoading={ordersList.isLoading}
      isFetching={ordersList.isFetching}
      error={ordersList.error}
      onRefresh={() => void ordersList.refetch()}
    />
  );
}
