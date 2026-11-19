import type { ReactNode } from "react";
import type { ParsedApiError } from "@/lib/api/errors";
import { BrutalButton } from "./BrutalButton";

export function StateNotice({
  kind,
  title,
  detail,
  error,
  onRetry,
  action,
}: {
  kind: "loading" | "empty" | "error";
  title: string;
  detail?: string;
  error?: ParsedApiError | null;
  onRetry?: () => void;
  action?: ReactNode;
}) {
  const borderBg =
    kind === "error"
      ? "border-status-dead-ink bg-status-dead-bg text-status-dead-ink"
      : "border-ink bg-surface text-ink";

  return (
    <div className={`border-2 p-5 brutal-shadow-sm ${borderBg}`} role={kind === "error" ? "alert" : "status"}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="font-mono text-xs font-bold uppercase">
            {error ? `${error.code} (HTTP ${error.status || "NET"})` : kind.toUpperCase()}
          </p>
          <h3 className="mt-1 text-lg font-bold">{error ? error.detail : title}</h3>
          {(error?.actionHint || detail) && (
            <p className="mt-1 text-sm">{error ? error.actionHint : detail}</p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {onRetry && (
            <BrutalButton variant="secondary" onClick={onRetry}>
              Retry
            </BrutalButton>
          )}
          {action}
        </div>
      </div>
    </div>
  );
}
