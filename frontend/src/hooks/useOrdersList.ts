import { useQuery } from "@tanstack/react-query";
import { listMyOrdersOptions } from "@/lib/api/generated/@tanstack/react-query.gen";
import { isAuthenticated, useAuthStore } from "@/store/auth-store";

export function useOrdersList() {
  const auth = useAuthStore();
  const enabled = isAuthenticated(auth);

  const query = useQuery({
    ...listMyOrdersOptions(),
    enabled,
  });

  return {
    orders: query.data ?? [],
    enabled,
    isLoading: query.isLoading,
    isFetching: query.isFetching,
    error: query.error,
    refetch: query.refetch,
  };
}
