import { QueryClient } from "@tanstack/react-query";
import { configureApiClient } from "@/lib/api/api-client";

let appContext: { queryClient: QueryClient } | undefined;

function isTransportError(cause: unknown): boolean {
  return cause instanceof TypeError && !("response" in cause && cause.response);
}

export function getContext(): { queryClient: QueryClient } {
  configureApiClient();
  if (appContext) {
    return appContext;
  }

  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 10_000,
        gcTime: 5 * 60_000,
        retry: (failureCount, error) => {
          if (isTransportError(error)) return failureCount < 2;
          return false;
        },
        retryDelay: 500,
        refetchOnWindowFocus: false,
      },
    },
  });

  appContext = { queryClient };
  return appContext;
}
