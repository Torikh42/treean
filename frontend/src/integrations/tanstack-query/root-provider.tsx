import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { getContext } from "./context";

export function TanStackQueryProvider({ children }: { children: ReactNode }) {
  const { queryClient } = getContext();
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
