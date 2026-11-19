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
      totalCount={catalog.totalCount}
      searchQuery={catalog.searchQuery}
      onSearchChange={catalog.setSearchQuery}
      onClearSearch={catalog.clearSearch}
      authenticated={catalog.authenticated}
      activeRole={catalog.activeRole}
      isLoading={catalog.isLoading}
      isFetching={catalog.isFetching}
      error={catalog.error}
      onRefresh={() => void catalog.refetch()}
    />
  );
}
