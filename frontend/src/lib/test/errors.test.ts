import { describe, expect, it } from "vitest";
import { parseApiError } from "@/lib/api/errors";

describe("parseApiError", () => {
  it("extracts domain error codes and recovery guidance", () => {
    const parsed = parseApiError({
      title: "Conflict",
      status: 409,
      detail: "insufficient remaining quota",
      error_code: "QUOTA_EXHAUSTED",
    });

    expect(parsed.status).toBe(409);
    expect(parsed.code).toBe("QUOTA_EXHAUSTED");
    expect(parsed.detail).toBe("insufficient remaining quota");
    expect(parsed.actionHint).toContain("tier");
  });

  it("handles native transport errors without casting", () => {
    const parsed = parseApiError(new TypeError("Failed to fetch"));
    expect(parsed.code).toBe("NETWORK_ERROR");
    expect(parsed.detail).toBe("Failed to fetch");
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
