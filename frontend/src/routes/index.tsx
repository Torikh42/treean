import { createFileRoute } from "@tanstack/react-router";
import { EventCatalogList } from "@/components/events/EventCatalogList";
import { useEventsCatalog } from "@/hooks/useEventsCatalog";

export const Route = createFileRoute("/")({
  component: EventsIndexPage,
});

function EventsIndexPage() {
  const catalog = useEventsCatalog();

  return (
    <EventCatalogList
      events={catalog.events}
      isLoading={catalog.isLoading}
      isFetching={catalog.isFetching}
      error={catalog.error}
      onRefresh={() => void catalog.refetch()}
    />
  );
}
