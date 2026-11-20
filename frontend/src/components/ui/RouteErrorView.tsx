import { Link, useRouter, type ErrorComponentProps } from "@tanstack/react-router";
import { BrutalButton } from "./BrutalButton";
import { BrutalCard } from "./BrutalCard";

export function RouteErrorView({ error, reset }: ErrorComponentProps) {
  const router = useRouter();

  function handleRetry() {
    reset();
    void router.invalidate();
  }

  return (
    <div className="mx-auto max-w-xl py-8">
      <BrutalCard>
        <p className="font-mono text-xs font-bold uppercase text-status-dead-ink">
          Page Interrupted
        </p>
        <h1 className="mt-2 text-2xl font-bold tracking-tight">
          Something interrupted this view
        </h1>
        <p className="mt-1 text-sm text-ink-muted">
          Your session and order data remain safe. Try reloading this page or return to the event catalog.
        </p>

        <div className="mt-5 flex flex-wrap gap-3">
          <BrutalButton variant="signal" onClick={handleRetry}>
            Try Again
          </BrutalButton>
          <Link
            to="/"
            className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-4 py-2 text-sm font-bold text-ink brutal-press"
          >
            Back to Events
          </Link>
        </div>

        {error instanceof Error && error.message && (
          <details className="mt-5 border-2 border-ink bg-canvas p-3 text-xs">
            <summary className="cursor-pointer font-mono font-bold uppercase text-ink-muted">
              Technical detail
            </summary>
            <p className="mt-2 font-mono text-ink-muted break-words">{error.message}</p>
          </details>
        )}
      </BrutalCard>
    </div>
  );
}
