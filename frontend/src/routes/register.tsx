import { createFileRoute } from "@tanstack/react-router";
import { RegisterView } from "@/components/auth/RegisterView";
import { useRegisterForm } from "@/hooks/useAuthForms";
import { requireGuest, safeRedirectPath } from "@/lib/auth-guards";

interface RegisterSearch {
  redirect?: string;
}

export const Route = createFileRoute("/register")({
  validateSearch: (search: Record<string, unknown>): RegisterSearch => ({
    redirect: typeof search.redirect === "string" ? safeRedirectPath(search.redirect) : undefined,
  }),
  beforeLoad: () => {
    requireGuest();
  },
  component: RegisterPage,
});

function RegisterPage() {
  const { redirect } = Route.useSearch();
  const register = useRegisterForm(redirect);

  return <RegisterView register={register} redirectPath={redirect} />;
}
