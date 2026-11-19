import { createFileRoute } from "@tanstack/react-router";
import { OrganizerConsoleView } from "@/components/events/OrganizerConsoleView";
import { useOrganizerConsole } from "@/hooks/useOrganizerConsole";
import { requireRole } from "@/lib/auth-guards";

export const Route = createFileRoute("/organizer")({
  beforeLoad: ({ location }) => {
    requireRole("ORGANIZER", location.href);
  },
  component: OrganizerPage,
});

function OrganizerPage() {
  const consoleState = useOrganizerConsole();
  return <OrganizerConsoleView consoleState={consoleState} />;
}
