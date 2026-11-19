import { createFileRoute } from "@tanstack/react-router";
import { LoginView } from "@/components/auth/LoginView";
import { useLoginForm } from "@/hooks/useAuthForms";
import { requireGuest, safeRedirectPath } from "@/lib/auth-guards";

interface LoginSearch {
  redirect?: string;
}

export const Route = createFileRoute("/login")({
  validateSearch: (search: Record<string, unknown>): LoginSearch => ({
    redirect: typeof search.redirect === "string" ? safeRedirectPath(search.redirect) : undefined,
  }),
  beforeLoad: () => {
    requireGuest();
  },
  component: LoginPage,
});

function LoginPage() {
  const { redirect } = Route.useSearch();
  const login = useLoginForm(redirect);

  return <LoginView login={login} redirectPath={redirect} />;
}
