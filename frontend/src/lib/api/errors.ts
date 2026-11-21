import { toast } from "sonner";
import type { ApiError, ErrorDetail } from "@/lib/api/generated/types.gen";
import { ERROR_COPY_MAP, type ErrorCopy } from "@/lib/api/error-catalog";

export interface ParsedApiError {
  status: number;
  code: string;
  detail: string;
  userTitle: string;
  userMessage: string;
  actionHint: string;
  fieldMessages?: ReadonlyArray<string>;
}

const BUYER_LIMIT_RE = /active\/paid=(\d+),\s*requested=(\d+),\s*max=(\d+)/i;

function isApiErrorObject(value: unknown): value is ApiError {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  return typeof candidate.detail === "string" && typeof candidate.error_code === "string";
}

function formatLocationLabel(location: string | undefined): string {
  if (!location) return "";
  const cleaned = location
    .replace(/^(body|path|query|header)\./i, "")
    .replace(/\[(\d+)\]/g, " #$1")
    .replace(/\./g, " ")
    .replace(/_/g, " ")
    .trim();
  if (!cleaned) return "";
  return cleaned.charAt(0).toUpperCase() + cleaned.slice(1);
}

function extractFieldMessages(details: ReadonlyArray<ErrorDetail> | null | undefined): string[] {
  if (!details || details.length === 0) return [];
  const messages: string[] = [];
  for (const item of details) {
    const msg = item.message?.trim();
    if (!msg) continue;
    const label = formatLocationLabel(item.location);
    messages.push(label ? `${label}: ${msg}` : msg);
  }
  return messages;
}

function buildDynamicCopy(
  code: string,
  rawDetail: string,
  fieldMessages: ReadonlyArray<string>,
): ErrorCopy | null {
  if (code === "BUYER_LIMIT_EXCEEDED") {
    const match = BUYER_LIMIT_RE.exec(rawDetail);
    if (match) {
      const used = Number(match[1]);
      const requested = Number(match[2]);
      const max = Number(match[3]);
      const remaining = Math.max(0, max - used);
      return {
        userTitle: "Ticket limit reached for this event",
        userMessage:
          remaining === 0
            ? `You already hold or purchased all ${max} allowed tickets for this event.`
            : `You currently hold or purchased ${used} of ${max} allowed tickets for this event (you selected ${requested}, only ${remaining} remaining).`,
        actionHint:
          remaining === 0
            ? "View My Orders to pay or cancel an existing reservation."
            : `Select at most ${remaining} ticket(s) or cancel an unpaid reservation in My Orders.`,
      };
    }
  }

  if (code === "VALIDATION_ERROR") {
    if (/invalid\s+event\s+uuid/i.test(rawDetail)) {
      return {
        userTitle: "Invalid event link",
        userMessage: "This event address is malformed or incomplete.",
        actionHint: "Return to Events and select an event from the catalog.",
      };
    }
    if (/invalid\s+order\s+uuid/i.test(rawDetail)) {
      return {
        userTitle: "Invalid order link",
        userMessage: "This order address is malformed or incomplete.",
        actionHint: "Return to My Orders to open a valid order.",
      };
    }
    if (fieldMessages.length > 0) {
      return {
        userTitle: "Please check your inputs",
        userMessage: fieldMessages[0] ?? "Some entered values are invalid.",
        actionHint: "Review the noted input(s) and try again.",
      };
    }
    if (rawDetail.trim() && rawDetail.trim().toLowerCase() !== "validation failed") {
      return {
        userTitle: "Please check your inputs",
        userMessage: rawDetail,
        actionHint: "Review your inputs and try again.",
      };
    }
  }

  return null;
}

export function parseApiError(error: unknown): ParsedApiError {
  if (isApiErrorObject(error)) {
    const code = error.error_code || "API_ERROR";
    const rawDetail = error.detail || "Request failed";
    const fieldMessages = extractFieldMessages(error.errors);
    const copy = buildDynamicCopy(code, rawDetail, fieldMessages) ??
      ERROR_COPY_MAP[code] ?? {
        userTitle: "Something went wrong",
        userMessage: "We could not complete your request right now.",
        actionHint: "Please try again in a moment.",
      };
    return {
      status: typeof error.status === "number" ? error.status : 400,
      code,
      detail: rawDetail,
      ...(fieldMessages.length > 1 ? { fieldMessages } : {}),
      ...copy,
    };
  }

  if (error instanceof Error) {
    if (error.name === "ZodError") {
      return {
        status: 500,
        code: "INTERNAL_ERROR",
        detail: error.message || "Response schema validation failed",
        userTitle: "Response validation error",
        userMessage: "The server returned data in an unexpected format.",
        actionHint: "Refresh the page or try again.",
      };
    }
    return {
      status: 0,
      code: "NETWORK_ERROR",
      detail: error.message || "Could not reach backend API",
      userTitle: "Connection problem",
      userMessage: "We could not reach the Treean server. Please check your connection.",
      actionHint: "Verify the API is running and retry.",
    };
  }

  return {
    status: 0,
    code: "UNKNOWN_ERROR",
    detail: "Unexpected failure occurred",
    userTitle: "Unexpected error",
    userMessage: "An unexpected problem interrupted this action.",
    actionHint: "Please try again.",
  };
}

export function toastApiError(error: unknown): ParsedApiError {
  const parsed = parseApiError(error);
  toast.error(parsed.userTitle, { description: parsed.userMessage });
  return parsed;
}
