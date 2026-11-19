import { Link } from "@tanstack/react-router";
import { ShieldWarning } from "@phosphor-icons/react";
import { defaultHomeForRole } from "@/lib/auth-guards";
import type { UserRole } from "@/store/auth-store";
import { BrutalCard } from "@/components/ui/BrutalCard";

interface ForbiddenViewProps {
  requiredRole?: UserRole;
  currentRole: UserRole | null;
  userEmail?: string;
}

export function ForbiddenView({ requiredRole, currentRole, userEmail }: ForbiddenViewProps) {
  const allowedHome = defaultHomeForRole(currentRole);
  const homeLabel = currentRole === "ORGANIZER" ? "Go to Organizer Console" : "Back to Events";

  return (
    <div className="mx-auto max-w-xl">
      <BrutalCard>
        <div className="flex items-center gap-2 font-mono text-xs font-bold uppercase text-status-dead-ink">
          <ShieldWarning size={18} weight="bold" />
          <span>403 FORBIDDEN</span>
        </div>

        <h1 className="mt-2 text-3xl font-bold tracking-tight">Access Restricted</h1>
        <p className="mt-2 text-sm text-ink-muted">
          Your current session does not have permission to open this workspace.
        </p>

        <div className="mt-5 grid gap-3 sm:grid-cols-2">
          <div className="border-2 border-ink bg-canvas p-4">
            <p className="font-mono text-xs font-bold uppercase text-ink-muted">Required Role</p>
            <p className="mt-1 font-mono text-lg font-bold">{requiredRole ?? "AUTHORIZED ROLE"}</p>
          </div>
          <div className="border-2 border-ink bg-canvas p-4">
            <p className="font-mono text-xs font-bold uppercase text-ink-muted">Current Session</p>
            <p className="mt-1 font-mono text-lg font-bold">{currentRole ?? "GUEST"}</p>
            {userEmail && <p className="mt-0.5 truncate font-mono text-xs text-ink-muted">{userEmail}</p>}
          </div>
        </div>

        <div className="mt-6 flex flex-wrap gap-3">
          <Link
            to={allowedHome}
            className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-4 py-2 text-sm font-bold brutal-press"
          >
            {homeLabel}
          </Link>
          {!currentRole && (
            <Link
              to="/login"
              className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-4 py-2 text-sm font-bold brutal-press"
            >
              Sign In
            </Link>
          )}
          <Link
            to="/"
            className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-4 py-2 text-sm font-bold brutal-press"
          >
            Browse Events
          </Link>
        </div>
      </BrutalCard>
    </div>
  );
}
