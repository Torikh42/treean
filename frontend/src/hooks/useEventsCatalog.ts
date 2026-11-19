import { useQuery } from "@tanstack/react-query";
import { listEventsOptions } from "@/lib/api/generated/@tanstack/react-query.gen";

export function useEventsCatalog() {
  const query = useQuery(listEventsOptions());
  return {
    events: query.data ?? [],
    isLoading: query.isLoading,
    isFetching: query.isFetching,
    error: query.error,
    refetch: query.refetch,
  };
}
