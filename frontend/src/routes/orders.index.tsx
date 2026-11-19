import { createFileRoute } from "@tanstack/react-router";
import { OrdersHistoryTable } from "@/components/orders/OrdersHistoryTable";
import { useOrdersList } from "@/hooks/useOrdersList";

export const Route = createFileRoute("/orders/")({
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
