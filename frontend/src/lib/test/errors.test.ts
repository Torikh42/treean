import { describe, expect, it } from "vitest";
import { parseApiError } from "@/lib/api/errors";

describe("parseApiError", () => {
  it("extracts domain error codes and friendly user copy", () => {
    const parsed = parseApiError({
      title: "Conflict",
      status: 409,
      detail: "insufficient remaining quota",
      error_code: "QUOTA_EXHAUSTED",
    });

    expect(parsed.status).toBe(409);
    expect(parsed.code).toBe("QUOTA_EXHAUSTED");
    expect(parsed.userTitle).toBe("Not enough tickets left");
    expect(parsed.userMessage).toContain("remaining quota");
    expect(parsed.actionHint).toContain("tier");
  });

  it("formats BUYER_LIMIT_EXCEEDED counters into natural guidance", () => {
    const parsed = parseApiError({
      title: "Conflict",
      status: 409,
      detail: "buyer ticket limit exceeded: active/paid=2, requested=3, max=4",
      error_code: "BUYER_LIMIT_EXCEEDED",
    });

    expect(parsed.userTitle).toBe("Ticket limit reached for this event");
    expect(parsed.userMessage).toContain("2 of 4");
    expect(parsed.userMessage).toContain("only 2 remaining");
    expect(parsed.actionHint).toContain("at most 2");
  });

  it("explains reached 4-of-4 allowance clearly", () => {
    const parsed = parseApiError({
      title: "Conflict",
      status: 409,
      detail: "buyer ticket limit exceeded: active/paid=4, requested=1, max=4",
      error_code: "BUYER_LIMIT_EXCEEDED",
    });

    expect(parsed.userMessage).toContain("all 4 allowed tickets");
    expect(parsed.actionHint).toContain("My Orders");
  });

  it("extracts Huma validation field errors cleanly", () => {
    const parsed = parseApiError({
      title: "Unprocessable Entity",
      status: 422,
      detail: "validation failed",
      error_code: "VALIDATION_ERROR",
      errors: [
        { location: "body.title", message: "expected length >= 3" },
        { location: "body.ticket_types[1].total_quota", message: "expected number >= 1" },
      ],
    });

    expect(parsed.userTitle).toBe("Please check your inputs");
    expect(parsed.userMessage).toBe("Title: expected length >= 3");
    expect(parsed.fieldMessages).toEqual([
      "Title: expected length >= 3",
      "Ticket types #1 total quota: expected number >= 1",
    ]);
  });

  it("formats invalid UUID path errors and server/not-found errors gracefully", () => {
    const badEvent = parseApiError({
      title: "Bad Request",
      status: 400,
      detail: "invalid event UUID",
      error_code: "VALIDATION_ERROR",
    });
    expect(badEvent.userTitle).toBe("Invalid event link");

    const srv = parseApiError({
      title: "Internal Server Error",
      status: 500,
      detail: "internal server error",
      error_code: "INTERNAL_ERROR",
    });
    expect(srv.userTitle).toBe("Server error");

    const notFound = parseApiError({
      title: "Not Found",
      status: 404,
      detail: "not found",
      error_code: "NOT_FOUND",
    });
    expect(notFound.userTitle).toBe("Resource not found");
  });

  it("handles native transport errors gracefully", () => {
    const parsed = parseApiError(new TypeError("Failed to fetch"));
    expect(parsed.code).toBe("NETWORK_ERROR");
    expect(parsed.userTitle).toBe("Connection problem");
    expect(parsed.detail).toBe("Failed to fetch");
  });

  it("maps ZodError instances to INTERNAL_ERROR instead of misleading NETWORK_ERROR", () => {
    const zodErr = new Error("Invalid ISO datetime");
    zodErr.name = "ZodError";
    const parsed = parseApiError(zodErr);
    expect(parsed.code).toBe("INTERNAL_ERROR");
    expect(parsed.userTitle).toBe("Response validation error");
    expect(parsed.detail).toBe("Invalid ISO datetime");
  });

  it("maps RATE_LIMITED (429) with retry guidance", () => {
    const parsed = parseApiError({
      title: "Too Many Requests",
      status: 429,
      detail: "rate limit exceeded; retry after 12 seconds",
      error_code: "RATE_LIMITED",
    });
    expect(parsed.status).toBe(429);
    expect(parsed.code).toBe("RATE_LIMITED");
    expect(parsed.actionHint).toContain("wait");
  });
});
