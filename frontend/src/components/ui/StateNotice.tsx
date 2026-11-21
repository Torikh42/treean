import type { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import type { ParsedApiError } from "@/lib/api/errors";
import { BrutalButton } from "./BrutalButton";

export function StateNotice({
  kind,
  title,
  detail,
  error,
  showTechnicalDetails = false,
  headingLevel = "h2",
  onRetry,
  action,
}: {
  kind: "loading" | "empty" | "error";
  title: string;
  detail?: string;
  error?: ParsedApiError | null;
  showTechnicalDetails?: boolean;
  headingLevel?: "h1" | "h2" | "h3";
  onRetry?: () => void;
  action?: ReactNode;
}) {
  const borderBg =
    kind === "error"
      ? "border-status-dead-ink bg-status-dead-bg text-status-dead-ink"
      : "border-ink bg-surface text-ink";

  const eyebrow = error
    ? showTechnicalDetails
      ? `${error.code} (HTTP ${error.status || "NET"})`
      : "Notice"
    : kind === "loading"
      ? "Loading"
      : kind === "empty"
        ? "Empty"
        : "Notice";

  const heading = error ? error.userTitle : title;
  const message = error ? error.userMessage : detail;
  const hint = error?.actionHint;
  const fieldMessages = error?.fieldMessages;

  const resolvedAction =
    action ??
    (error?.code === "UNAUTHORIZED" ? (
      <Link
        to="/login"
        className="inline-flex min-h-[44px] items-center border-2 border-ink bg-signal px-3 py-1.5 text-xs font-bold text-ink brutal-press"
      >
        Sign In Again
      </Link>
    ) : null);

  const HeadingTag = headingLevel;

  return (
    <div className={`border-2 p-5 brutal-shadow-sm ${borderBg}`} role={kind === "error" ? "alert" : "status"}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0 flex-1 space-y-1">
          <p className="font-mono text-xs font-bold uppercase">{eyebrow}</p>
          <HeadingTag className="text-lg font-bold">{heading}</HeadingTag>
          {message && <p className="text-sm">{message}</p>}
          {fieldMessages && fieldMessages.length > 0 && (
            <ul className="list-inside list-disc space-y-0.5 pt-1 text-xs">
              {fieldMessages.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          )}
          {hint && <p className="text-xs font-bold">{hint}</p>}
          {error && showTechnicalDetails && error.detail && (
            <p className="pt-1 font-mono text-xs opacity-80">{error.detail}</p>
          )}
        </div>
        {(onRetry || resolvedAction) && (
          <div className="flex flex-wrap items-center gap-2">
            {onRetry && (
              <BrutalButton variant="secondary" onClick={onRetry}>
                Retry
              </BrutalButton>
            )}
            {resolvedAction}
          </div>
        )}
      </div>
    </div>
  );
}
