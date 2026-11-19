import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { AppShell } from "@/components/layout/AppShell";
import { TanStackQueryProvider } from "@/integrations/tanstack-query/root-provider";
import "../index.css";

interface RouterContext {
  queryClient: QueryClient;
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: RootLayout,
  notFoundComponent: NotFoundView,
});

function RootLayout() {
  return (
    <TanStackQueryProvider>
      <AppShell>
        <Outlet />
      </AppShell>
      <Toaster
        position="bottom-right"
        toastOptions={{
          style: {
            border: "2px solid #111111",
            borderRadius: "0px",
            boxShadow: "4px 4px 0 #111111",
            background: "#FFFFFF",
            color: "#111111",
            fontFamily: "var(--font-sans)",
            fontWeight: 700,
          },
        }}
      />
    </TanStackQueryProvider>
  );
}

function NotFoundView() {
  return (
    <div className="border-2 border-ink bg-surface p-8 brutal-shadow">
      <p className="font-mono text-xs font-bold uppercase text-ink-muted">404 NOT FOUND</p>
      <h1 className="mt-2 text-2xl font-bold">Page Not Found</h1>
      <p className="mt-1 text-sm text-ink-muted">The requested route does not exist in TREEAN.</p>
      <Link
        to="/"
        className="mt-5 inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold brutal-press"
      >
        Back to Events
      </Link>
    </div>
  );
}
