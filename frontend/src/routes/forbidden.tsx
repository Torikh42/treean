import { createFileRoute } from "@tanstack/react-router";
import { ForbiddenView } from "@/components/auth/ForbiddenView";
import { getActiveRole, useAuthStore, type UserRole } from "@/store/auth-store";

interface ForbiddenSearch {
  required?: UserRole;
}

export const Route = createFileRoute("/forbidden")({
  validateSearch: (search: Record<string, unknown>): ForbiddenSearch => ({
    required:
      search.required === "BUYER" || search.required === "ORGANIZER"
        ? search.required
        : undefined,
  }),
  component: ForbiddenPage,
});

function ForbiddenPage() {
  const { required } = Route.useSearch();
  const auth = useAuthStore();

  return (
    <ForbiddenView
      requiredRole={required}
      currentRole={getActiveRole(auth)}
      userEmail={auth.user?.email}
    />
  );
}
