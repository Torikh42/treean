import { describe, expect, it } from "vitest";
import {
  buildCreateEventBody,
  buildUpdateEventBody,
  computeSoldOrHeld,
  toLocalDatetimeValue,
} from "@/lib/organizer-event-helpers";

describe("organizer event helpers", () => {
  it("computes sold or held tickets safely", () => {
    expect(computeSoldOrHeld({ total_quota: 80, remaining_quota: 55 })).toBe(25);
    expect(computeSoldOrHeld({ total_quota: 20, remaining_quota: 20 })).toBe(0);
    expect(computeSoldOrHeld({ total_quota: 10, remaining_quota: 15 })).toBe(0);
  });

  it("formats ISO strings into local datetime-local values", () => {
    const formatted = toLocalDatetimeValue("2026-12-20T12:00:00Z");
    expect(formatted).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
    expect(toLocalDatetimeValue("not-a-date")).toMatch(/^\d{4}-\d{2}-\d{2}T19:00$/);
  });

  it("builds valid CreateEventBody and rejects invalid inputs", () => {
    const valid = buildCreateEventBody({
      title: "  Indie Night 2026  ",
      venue: "  Senayan Hall ",
      description: "  Main stage release ",
      eventDateLocal: "2026-12-20T19:00",
      tiers: [
        { name: "Reguler", price_idr: 150000, total_quota: 80 },
        { name: "VIP", price_idr: 350000, total_quota: 20 },
      ],
    });
    expect(valid.ok).toBe(true);
    if (valid.ok) {
      expect(valid.body.title).toBe("Indie Night 2026");
      expect(valid.body.venue).toBe("Senayan Hall");
      expect(valid.body.description).toBe("Main stage release");
      expect(valid.body.ticket_types).toHaveLength(2);
      expect(valid.body.event_date).toContain("T");
    }

    const dupNames = buildCreateEventBody({
      title: "Indie Night 2026",
      venue: "Senayan Hall",
      description: "",
      eventDateLocal: "2026-12-20T19:00",
      tiers: [
        { name: "Reguler", price_idr: 150000, total_quota: 80 },
        { name: "reguler", price_idr: 350000, total_quota: 20 },
      ],
    });
    expect(dupNames.ok).toBe(false);
    if (!dupNames.ok) {
      expect(dupNames.error.code).toBe("VALIDATION_ERROR");
    }

    const tooFewTiers = buildCreateEventBody({
      title: "Indie Night 2026",
      venue: "Senayan Hall",
      description: "",
      eventDateLocal: "2026-12-20T19:00",
      tiers: [{ name: "Reguler", price_idr: 150000, total_quota: 80 }],
    });
    expect(tooFewTiers.ok).toBe(false);
  });

  it("builds UpdateEventBody preserving existing tier IDs and enforcing sold/held minimum quota", () => {
    const shrinkConflict = buildUpdateEventBody({
      title: "Tech Flash Concert 2026",
      venue: "Jakarta Convention Hall",
      description: "Updated",
      eventDateLocal: "2026-12-20T19:00",
      tiers: [
        {
          id: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1",
          name: "Reguler",
          price_idr: 150000,
          total_quota: 10,
          sold_or_held: 25,
        },
        {
          id: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb2",
          name: "VIP",
          price_idr: 350000,
          total_quota: 20,
          sold_or_held: 0,
        },
      ],
    });
    expect(shrinkConflict.ok).toBe(false);
    if (!shrinkConflict.ok) {
      expect(shrinkConflict.error.code).toBe("QUOTA_CONFLICT");
      expect(shrinkConflict.error.detail).toContain("25");
    }

    const validUpdate = buildUpdateEventBody({
      title: "Tech Flash Concert 2026",
      venue: "Jakarta Convention Hall",
      description: "",
      eventDateLocal: "2026-12-20T19:00",
      tiers: [
        {
          id: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1",
          name: "Reguler",
          price_idr: 160000,
          total_quota: 90,
          sold_or_held: 25,
        },
        {
          name: "VVIP",
          price_idr: 500000,
          total_quota: 15,
          sold_or_held: 0,
        },
      ],
    });
    expect(validUpdate.ok).toBe(true);
    if (validUpdate.ok) {
      expect(validUpdate.body.ticket_types?.[0]).toEqual({
        id: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1",
        name: "Reguler",
        price_idr: 160000,
        total_quota: 90,
      });
      expect(validUpdate.body.ticket_types?.[1]).toEqual({
        name: "VVIP",
        price_idr: 500000,
        total_quota: 15,
      });
      expect("id" in (validUpdate.body.ticket_types?.[1] ?? {})).toBe(false);
    }
  });
});
