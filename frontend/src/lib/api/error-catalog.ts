export interface ErrorCopy {
  userTitle: string;
  userMessage: string;
  actionHint: string;
}

export const ERROR_COPY_MAP: Readonly<Record<string, ErrorCopy>> = {
  QUOTA_EXHAUSTED: {
    userTitle: "Not enough tickets left",
    userMessage: "The remaining quota for one or more selected tiers cannot fulfill your request.",
    actionHint: "Reduce your quantity or pick another available ticket tier.",
  },
  BUYER_LIMIT_EXCEEDED: {
    userTitle: "Ticket limit reached for this event",
    userMessage: "Each buyer can hold or purchase at most 4 tickets per event.",
    actionHint: "Lower your ticket count or cancel an unpaid order in My Orders.",
  },
  INVALID_QUANTITY: {
    userTitle: "Invalid ticket quantity",
    userMessage: "You can reserve between 1 and 4 tickets per order.",
    actionHint: "Adjust your selected ticket quantities and try again.",
  },
  ORDER_EXPIRED: {
    userTitle: "Reservation window expired",
    userMessage: "The 15-minute payment hold on this order has already ended.",
    actionHint: "Place a fresh order if tickets are still available.",
  },
  INVALID_ORDER_STATE: {
    userTitle: "Order status has already changed",
    userMessage: "This order was already paid, cancelled, or expired.",
    actionHint: "Refresh the page to see its latest status.",
  },
  UNAUTHORIZED: {
    userTitle: "Sign in required",
    userMessage: "Your session is missing or has expired.",
    actionHint: "Sign in again to continue.",
  },
  INVALID_CREDENTIALS: {
    userTitle: "Incorrect email or password",
    userMessage: "We could not match that email and password combination.",
    actionHint: "Double-check your credentials and try again.",
  },
  EMAIL_ALREADY_EXISTS: {
    userTitle: "Email already registered",
    userMessage: "An account using this email address already exists.",
    actionHint: "Sign in with your existing account or use a different email.",
  },
  FORBIDDEN: {
    userTitle: "Access not permitted",
    userMessage: "Your current account role does not have permission for this action.",
    actionHint: "Switch to an account with the required role.",
  },
  EVENT_NOT_FOUND: {
    userTitle: "Event not found",
    userMessage: "This event no longer exists or could not be located.",
    actionHint: "Return to the event catalog to browse active events.",
  },
  TICKET_TYPE_NOT_FOUND: {
    userTitle: "Ticket tier unavailable",
    userMessage: "One of the requested ticket tiers could not be found.",
    actionHint: "Refresh the event details and re-select your tiers.",
  },
  ORDER_NOT_FOUND: {
    userTitle: "Order not found",
    userMessage: "We could not find an order matching this ID for your account.",
    actionHint: "Check your order list in My Orders.",
  },
  NOT_FOUND: {
    userTitle: "Resource not found",
    userMessage: "The requested item could not be found.",
    actionHint: "Return to the previous page or refresh.",
  },
  EVENT_HAS_ORDERS: {
    userTitle: "Event has existing orders",
    userMessage: "Events with active, paid, or historical orders cannot be deleted.",
    actionHint: "Edit the event details instead of deleting it.",
  },
  TIER_HAS_ORDERS: {
    userTitle: "Ticket tier is in use",
    userMessage: "Tiers that already have buyer orders cannot be removed.",
    actionHint: "Keep this tier and update its name, price, or quota if needed.",
  },
  QUOTA_CONFLICT: {
    userTitle: "Quota cannot be lowered below sold tickets",
    userMessage: "New total quota must be at least the number of tickets already held or paid.",
    actionHint: "Increase the total quota to match or exceed the sold/reserved count.",
  },
  UNAUTHORIZED_WEBHOOK: {
    userTitle: "Invalid webhook signature",
    userMessage: "The payment webhook signature did not match the configured secret.",
    actionHint: "Recompute SHA512(order_id + status_code + gross_amount + WEBHOOK_SECRET).",
  },
  PAYMENT_AMOUNT_MISMATCH: {
    userTitle: "Payment amount mismatch",
    userMessage: "The submitted gross amount does not match the order total.",
    actionHint: "Match gross_amount exactly with the order total in IDR.",
  },
  RATE_LIMITED: {
    userTitle: "Too many requests",
    userMessage: "You are sending requests too quickly right now.",
    actionHint: "Please wait a few seconds before trying again.",
  },
  VALIDATION_ERROR: {
    userTitle: "Please check your inputs",
    userMessage: "Some entered values are missing or outside the allowed format.",
    actionHint: "Review the highlighted details and try again.",
  },
  INTERNAL_ERROR: {
    userTitle: "Server error",
    userMessage: "Something went wrong on the server while processing your request.",
    actionHint: "Please try again in a moment.",
  },
  HTTP_ERROR: {
    userTitle: "Request could not be completed",
    userMessage: "The server rejected this request.",
    actionHint: "Refresh and try again.",
  },
};
