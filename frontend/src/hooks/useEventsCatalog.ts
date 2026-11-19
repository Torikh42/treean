import { useDeferredValue, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listEventsOptions } from "@/lib/api/generated/@tanstack/react-query.gen";
import type { Event } from "@/lib/api/generated/types.gen";
import { getActiveRole, isAuthenticated, useAuthStore } from "@/store/auth-store";

const EMPTY_EVENTS: ReadonlyArray<Event> = [];

export function useEventsCatalog() {
  const auth = useAuthStore();
  const [searchQuery, setSearchQuery] = useState("");
  const deferredQuery = useDeferredValue(searchQuery.trim().toLowerCase());
  const query = useQuery(listEventsOptions());

  const allEvents = query.data ?? EMPTY_EVENTS;
  const filteredEvents = useMemo(() => {
    if (!deferredQuery) return allEvents;
    return allEvents.filter(
      (ev) =>
        ev.title.toLowerCase().includes(deferredQuery) ||
        ev.venue.toLowerCase().includes(deferredQuery),
    );
  }, [allEvents, deferredQuery]);

  return {
    events: filteredEvents,
    totalCount: allEvents.length,
    searchQuery,
    setSearchQuery,
    clearSearch: () => setSearchQuery(""),
    authenticated: isAuthenticated(auth),
    activeRole: getActiveRole(auth),
    isLoading: query.isLoading,
    isFetching: query.isFetching,
    error: query.error,
    refetch: query.refetch,
  };
}
