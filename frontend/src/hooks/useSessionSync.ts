import { useQuery } from "@tanstack/react-query";
import { parseApiError } from "@/lib/api/errors";
import { getMeOptions, getMeQueryKey } from "@/lib/api/generated/@tanstack/react-query.gen";
import { useAuthStore } from "@/store/auth-store";

export function useSessionSync() {
  const accessToken = useAuthStore((s) => s.accessToken);
  const updateUser = useAuthStore((s) => s.updateUser);
  const clearSession = useAuthStore((s) => s.clearSession);

  const baseOptions = getMeOptions();

  return useQuery({
    queryKey: [...getMeQueryKey(), accessToken] as const,
    enabled: Boolean(accessToken),
    retry: false,
    staleTime: 60_000,
    queryFn: async (ctx) => {
      try {
        const user = await baseOptions.queryFn!({
          ...ctx,
          queryKey: getMeQueryKey(),
        });
        updateUser(user);
        return user;
      } catch (err) {
        if (parseApiError(err).status === 401) {
          clearSession();
        }
        throw err;
      }
    },
  });
}
