import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { ArrowSquareOut, Lightning, ListBullets, PlusSquare, Ticket, UserSwitch } from "@phosphor-icons/react";
import { useAuthPanel } from "@/hooks/useAuthPanel";
import { useHealthCheck } from "@/hooks/useHealthCheck";
import { AuthPanel } from "@/components/auth/AuthPanel";
import { BrutalButton } from "@/components/ui/BrutalButton";

const NAV_LINK_CLS =
  "inline-flex min-h-[44px] items-center gap-1.5 border-2 border-transparent px-3 py-2 text-sm font-bold tracking-tight text-ink hover:border-ink hover:bg-surface";
const ACTIVE_NAV_CLS =
  "border-ink bg-signal brutal-shadow-sm hover:bg-signal";

export function AppShell({ children }: { children: ReactNode }) {
  const health = useHealthCheck();
  const authPanel = useAuthPanel();

  const activeIdentityLabel =
    authPanel.auth.authMode === "jwt"
      ? authPanel.auth.user
        ? `JWT: ${authPanel.auth.user.name} (${authPanel.auth.user.role})`
        : "JWT: Signed Out"
      : `DEMO: ${authPanel.auth.demoRole} (${authPanel.auth.demoUserId.slice(0, 8)})`;

  const healthOk = health.isSuccess && health.data?.status === "ok";

  return (
    <div className="min-h-screen bg-canvas text-ink">
      <header className="border-b-3 border-ink bg-surface">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-4 px-4 py-3">
          <div className="flex flex-wrap items-center gap-4">
            <Link
              to="/"
              className="inline-flex min-h-[44px] items-center gap-2 border-2 border-ink bg-signal px-3 py-1.5 font-mono text-base font-bold tracking-tight brutal-shadow-sm"
            >
              <Ticket size={20} weight="bold" />
              TREEAN
            </Link>

            <nav className="flex flex-wrap items-center gap-1" aria-label="Main navigation">
              <Link to="/" className={NAV_LINK_CLS} activeProps={{ className: `${NAV_LINK_CLS} ${ACTIVE_NAV_CLS}` }} activeOptions={{ exact: true }}>
                Events
              </Link>
              <Link to="/orders" className={NAV_LINK_CLS} activeProps={{ className: `${NAV_LINK_CLS} ${ACTIVE_NAV_CLS}` }}>
                <ListBullets size={16} weight="bold" />
                My Orders
              </Link>
              <Link to="/organizer" className={NAV_LINK_CLS} activeProps={{ className: `${NAV_LINK_CLS} ${ACTIVE_NAV_CLS}` }}>
                <PlusSquare size={16} weight="bold" />
                Organizer
              </Link>
              <Link to="/flash-sale" className={NAV_LINK_CLS} activeProps={{ className: `${NAV_LINK_CLS} ${ACTIVE_NAV_CLS}` }}>
                <Lightning size={16} weight="bold" />
                Flash Sale Lab
              </Link>
              <a
                href="/docs"
                target="_blank"
                rel="noreferrer"
                className={NAV_LINK_CLS}
              >
                API Docs
                <ArrowSquareOut size={14} weight="bold" />
              </a>
            </nav>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <a
              href="/health"
              target="_blank"
              rel="noreferrer"
              className="inline-flex min-h-[44px] items-center gap-2 border-2 border-ink bg-canvas px-3 py-1.5 font-mono text-xs font-bold"
              title="Live backend /health probe"
            >
              <span
                className={`inline-block size-2.5 border border-ink ${
                  health.isLoading
                    ? "bg-surface-muted"
                    : healthOk
                      ? "bg-status-paid-ink"
                      : "bg-status-dead-ink"
                }`}
              />
              {health.isLoading ? "API ..." : healthOk ? "API OK" : "API DOWN"}
            </a>

            <BrutalButton
              variant={authPanel.panelOpen ? "signal" : "secondary"}
              onClick={() => authPanel.setPanelOpen(!authPanel.panelOpen)}
            >
              <UserSwitch size={16} weight="bold" />
              <span className="font-mono text-xs">{activeIdentityLabel}</span>
            </BrutalButton>
          </div>
        </div>
      </header>

      <AuthPanel state={authPanel} />

      <main className="mx-auto max-w-6xl px-4 py-8">{children}</main>
    </div>
  );
}
