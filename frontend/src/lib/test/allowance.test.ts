import { describe, expect, it } from "vitest";
import { computeExistingEventTickets, computeRemainingBuyerAllowance } from "@/lib/allowance";
import type { Order } from "@/lib/api/generated/types.gen";

function makeOrder(eventId: string, status: string, totalTickets: number): Order {
  return {
    id: `ord-${eventId}-${status}`,
    invoice_number: "INV-1",
    event_id: eventId,
    buyer_id: "buyer-1",
    total_tickets: totalTickets,
    total_amount_idr: 150000 * totalTickets,
    status,
    expires_at: "2026-12-01T10:00:00Z",
    created_at: "2026-12-01T09:45:00Z",
  };
}

describe("buyer event allowance helpers", () => {
  it("counts only WAITING_PAYMENT and PAID orders for the matching event", () => {
    const orders: Order[] = [
      makeOrder("evt-1", "WAITING_PAYMENT", 1),
      makeOrder("evt-1", "PAID", 1),
      makeOrder("evt-1", "CANCELLED", 2),
      makeOrder("evt-1", "EXPIRED", 3),
      makeOrder("evt-2", "PAID", 4),
    ];

    expect(computeExistingEventTickets(orders, "evt-1")).toBe(2);
    expect(computeRemainingBuyerAllowance(orders, "evt-1", 4)).toBe(2);
  });

  it("never drops remaining allowance below zero", () => {
    const orders: Order[] = [makeOrder("evt-1", "PAID", 5)];
    expect(computeRemainingBuyerAllowance(orders, "evt-1", 4)).toBe(0);
    expect(computeRemainingBuyerAllowance(undefined, "evt-1", 4)).toBe(4);
  });
});
