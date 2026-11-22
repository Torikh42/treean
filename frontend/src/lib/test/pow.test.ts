import { afterEach, describe, expect, it, vi } from "vitest";
import { executeAuthWithPowRetry } from "@/hooks/useAuthForms";
import { solvePowChallenge } from "@/lib/pow";

function countLeadingZeroBits(bytes: Uint8Array): number {
  let bits = 0;
  for (const byte of bytes) {
    if (byte === 0) {
      bits += 8;
      continue;
    }
    for (let shift = 7; shift >= 0; shift--) {
      if ((byte & (1 << shift)) === 0) {
        bits += 1;
      } else {
        return bits;
      }
    }
  }
  return bits;
}

describe("solvePowChallenge", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("solves a v1 challenge token with 8 leading zero bits and returns token:nonce", async () => {
    const token = "v1.1893456000.8.00112233445566778899aabbccddeeff.fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210";
    const solution = await solvePowChallenge(token);

    expect(solution.startsWith(`${token}:`)).toBe(true);
    const nonce = solution.slice(token.length + 1);
    expect(nonce).toMatch(/^\d+$/);

    const encoded = new TextEncoder().encode(solution);
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", encoded));
    expect(countLeadingZeroBits(digest)).toBeGreaterThanOrEqual(8);
  });

  it("solves non-byte-aligned difficulty (5 leading zero bits) accurately", async () => {
    const token = "v1.1893456000.5.abcdef0123456789abcdef0123456789.0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
    const solution = await solvePowChallenge(token);

    const encoded = new TextEncoder().encode(solution);
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", encoded));
    expect(countLeadingZeroBits(digest)).toBeGreaterThanOrEqual(5);
  });

  it("rejects with a descriptive error when crypto.subtle is unavailable", async () => {
    vi.stubGlobal("crypto", {});

    const token = "v1.1893456000.8.00112233445566778899aabbccddeeff.fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210";
    await expect(solvePowChallenge(token)).rejects.toThrow(/crypto\.subtle/i);
  });

  it("rejects malformed challenge tokens", async () => {
    await expect(solvePowChallenge("")).rejects.toThrow(/challenge/i);
    await expect(solvePowChallenge("v2.1893456000.8.aa.bb")).rejects.toThrow(/challenge/i);
    await expect(solvePowChallenge("v1.1893456000.8.aa")).rejects.toThrow(/challenge/i);
    await expect(solvePowChallenge("v1.notanumber.8.aa.bb")).rejects.toThrow(/challenge/i);
    await expect(solvePowChallenge("v1.1893456000.8..bb")).rejects.toThrow(/challenge/i);
    await expect(solvePowChallenge("v1.1893456000.8.aa.")).rejects.toThrow(/challenge/i);
  });

  it("rejects out-of-range difficulties (< 1 or > 24)", async () => {
    await expect(solvePowChallenge("v1.1893456000.0.aa.bb")).rejects.toThrow(/difficulty/i);
    await expect(solvePowChallenge("v1.1893456000.-1.aa.bb")).rejects.toThrow(/difficulty/i);
    await expect(solvePowChallenge("v1.1893456000.25.aa.bb")).rejects.toThrow(/difficulty/i);
    await expect(solvePowChallenge("v1.1893456000.abc.aa.bb")).rejects.toThrow(/difficulty/i);
  });
});

describe("executeAuthWithPowRetry", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("solves 403 CHALLENGE_REQUIRED once and retries with X-PoW-Solution", async () => {
    const token = "v1.1893456000.8.00112233445566778899aabbccddeeff.fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210";
    const callAuth = vi
      .fn()
      .mockResolvedValueOnce({
        data: undefined,
        error: { status: 403, title: "Forbidden", error_code: "CHALLENGE_REQUIRED", detail: "solve pow" },
        response: new Response(null, {
          status: 403,
          headers: { "X-PoW-Challenge": token },
        }),
      })
      .mockResolvedValueOnce({
        data: { access_token: "jwt-ok" },
        error: undefined,
        response: new Response(null, { status: 200 }),
      });

    const result = await executeAuthWithPowRetry(callAuth);
    expect(result).toEqual({ access_token: "jwt-ok" });
    expect(callAuth).toHaveBeenCalledTimes(2);
    expect(callAuth).toHaveBeenNthCalledWith(1);

    const retryHeaders = callAuth.mock.calls[1]?.[0] as Record<string, string> | undefined;
    expect(retryHeaders?.["X-PoW-Solution"]).toBeDefined();
    expect(retryHeaders?.["X-PoW-Solution"]?.startsWith(`${token}:`)).toBe(true);
  });

  it("enforces maxAttempts = 1 and does not loop if retry also returns CHALLENGE_REQUIRED", async () => {
    const token = "v1.1893456000.8.00112233445566778899aabbccddeeff.fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210";
    const retryErr = { status: 403, title: "Forbidden", error_code: "CHALLENGE_REQUIRED", detail: "second challenge" };
    const callAuth = vi.fn().mockResolvedValue({
      data: undefined,
      error: retryErr,
      response: new Response(null, {
        status: 403,
        headers: { "X-PoW-Challenge": token },
      }),
    });

    await expect(executeAuthWithPowRetry(callAuth)).rejects.toEqual(retryErr);
    expect(callAuth).toHaveBeenCalledTimes(2);
  });

  it("does not retry on non-challenge errors or missing X-PoW-Challenge header", async () => {
    const credErr = { status: 401, title: "Unauthorized", error_code: "INVALID_CREDENTIALS", detail: "bad password" };
    const callAuth = vi.fn().mockResolvedValueOnce({
      data: undefined,
      error: credErr,
      response: new Response(null, { status: 401 }),
    });

    await expect(executeAuthWithPowRetry(callAuth)).rejects.toEqual(credErr);
    expect(callAuth).toHaveBeenCalledTimes(1);

    const missingHeaderErr = { status: 403, title: "Forbidden", error_code: "CHALLENGE_REQUIRED", detail: "missing header" };
    const callMissingHeader = vi.fn().mockResolvedValueOnce({
      data: undefined,
      error: missingHeaderErr,
      response: new Response(null, { status: 403 }),
    });

    await expect(executeAuthWithPowRetry(callMissingHeader)).rejects.toEqual(missingHeaderErr);
    expect(callMissingHeader).toHaveBeenCalledTimes(1);
  });
});
