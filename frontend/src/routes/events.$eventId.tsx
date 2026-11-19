import { createFileRoute } from "@tanstack/react-router";
import { EventCheckoutPanel } from "@/components/events/EventCheckoutPanel";
import { useEventCheckout } from "@/hooks/useEventCheckout";

export const Route = createFileRoute("/events/$eventId")({
  component: EventCheckoutPage,
});

function EventCheckoutPage() {
  const { eventId } = Route.useParams();
  const checkout = useEventCheckout(eventId);

  return (
    <EventCheckoutPanel
      event={checkout.event}
      tiers={checkout.tiers}
      isLoading={checkout.isLoading}
      error={checkout.error}
      onRetry={() => void checkout.refetch()}
      quantities={checkout.quantities}
      totalSelected={checkout.totalSelected}
      totalAmountIDR={checkout.totalAmountIDR}
      onUpdateQty={checkout.updateQuantity}
      onSubmit={checkout.submitCheckout}
      isSubmitting={checkout.isSubmitting}
      submitError={checkout.submitError}
    />
  );
}
