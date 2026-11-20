import type { Order } from "@/lib/api/generated/types.gen";

export function computeExistingEventTickets(
  orders: ReadonlyArray<Order> | null | undefined,
  eventId: string,
): number {
  if (!orders || !eventId) return 0;
  return orders.reduce((sum, ord) => {
    if (ord.event_id !== eventId) return sum;
    if (ord.status !== "WAITING_PAYMENT" && ord.status !== "PAID") return sum;
    return sum + Math.max(0, ord.total_tickets);
  }, 0);
}

export function computeRemainingBuyerAllowance(
  orders: ReadonlyArray<Order> | null | undefined,
  eventId: string,
  maxPerEvent: number,
): number {
  const used = computeExistingEventTickets(orders, eventId);
  return Math.max(0, maxPerEvent - used);
}
