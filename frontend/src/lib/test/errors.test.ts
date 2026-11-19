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
});
