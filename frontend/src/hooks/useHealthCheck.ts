import { useQuery } from "@tanstack/react-query";
import { fetchApiHealth } from "@/lib/api/api-client";

export function useHealthCheck() {
  return useQuery({
    queryKey: ["system", "health"],
    queryFn: fetchApiHealth,
    refetchInterval: 30_000,
    retry: false,
  });
}
