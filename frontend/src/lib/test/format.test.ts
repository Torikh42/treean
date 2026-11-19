import { describe, expect, it } from "vitest";
import { computeSha512Hex, formatCountdown, formatIDR } from "@/lib/format";

describe("format utilities", () => {
  it("formats IDR amounts without decimals", () => {
    expect(formatIDR(150000)).toContain("150.000");
    expect(formatIDR(Number.NaN)).toBe("Rp 0");
  });

  it("formats countdown minutes and seconds and clamps negatives", () => {
    expect(formatCountdown(900_000)).toBe("15:00");
    expect(formatCountdown(65_000)).toBe("01:05");
    expect(formatCountdown(-5000)).toBe("00:00");
  });

  it("computes standard lowercase SHA-512 hex digest", async () => {
    const digest = await computeSha512Hex("abc");
    expect(digest).toHaveLength(128);
    expect(digest).toMatch(/^[0-9a-f]{128}$/);
  });
});
