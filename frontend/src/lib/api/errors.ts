import type { ApiError } from "@/lib/api/generated/types.gen";

export interface ParsedApiError {
  status: number;
  code: string;
  detail: string;
  actionHint: string;
}

const ACTION_HINTS: Record<string, string> = {
  QUOTA_EXHAUSTED: "Reduce quantity or pick another available tier.",
  BUYER_LIMIT_EXCEEDED: "Cancel an unpaid reservation or lower ticket count (max 4 per buyer per event).",
  ORDER_EXPIRED: "This 15-minute reservation expired. Create a new order.",
  INVALID_ORDER_STATE: "Order is already finalized or cancelled; refresh order status.",
  UNAUTHORIZED: "Sign in to your account to continue.",
  FORBIDDEN: "This action requires a different account role or event ownership.",
  EVENT_HAS_ORDERS: "Cannot delete an event that already has buyer orders.",
  TIER_HAS_ORDERS: "Cannot remove a ticket tier that already has orders.",
  QUOTA_CONFLICT: "Total quota cannot be lower than tickets already sold or held.",
  EMAIL_ALREADY_EXISTS: "An account with this email already exists. Sign in instead.",
  INVALID_CREDENTIALS: "Check your email and password and try again.",
  UNAUTHORIZED_WEBHOOK: "Ensure signature_key matches SHA512(order_id + status_code + gross_amount + WEBHOOK_SECRET).",
  PAYMENT_AMOUNT_MISMATCH: "Match gross_amount exactly with the order total IDR.",
  RATE_LIMITED: "Too many requests right now. Please wait a moment and try again.",
  VALIDATION_ERROR: "Check highlighted input fields and try again.",
};

function isApiErrorObject(value: unknown): value is ApiError {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  return typeof candidate.detail === "string" && typeof candidate.error_code === "string";
}

export function parseApiError(error: unknown): ParsedApiError {
  if (isApiErrorObject(error)) {
    const code = error.error_code || "API_ERROR";
    return {
      status: typeof error.status === "number" ? error.status : 400,
      code,
      detail: error.detail || "Request failed",
      actionHint: ACTION_HINTS[code] ?? "Inspect inputs or retry the operation.",
    };
  }

  if (error instanceof Error) {
    return {
      status: 0,
      code: "NETWORK_ERROR",
      detail: error.message || "Could not reach backend API",
      actionHint: "Verify the Go API server is running and reachable.",
    };
  }

  return {
    status: 0,
    code: "UNKNOWN_ERROR",
    detail: "Unexpected failure occurred",
    actionHint: "Retry the action or inspect API health.",
  };
}
