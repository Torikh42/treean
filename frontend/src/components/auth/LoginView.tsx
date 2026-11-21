import { Link } from "@tanstack/react-router";
import { SignIn } from "@phosphor-icons/react";
import { firstFieldError, useLoginForm } from "@/hooks/useAuthForms";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface LoginViewProps {
  login: ReturnType<typeof useLoginForm>;
  redirectPath?: string;
}

export function LoginView({ login, redirectPath }: LoginViewProps) {
  return (
    <div className="mx-auto max-w-lg space-y-6">
      <div className="border-b-3 border-ink pb-4">
        <h1 className="text-3xl font-bold tracking-tight">Sign In</h1>
        <p className="mt-1 text-sm text-ink-muted">
          Authenticate with your TREEAN Buyer or Organizer account.
        </p>
      </div>

      {redirectPath && redirectPath !== "/" && (
        <div className="break-all border-2 border-ink bg-surface-muted px-3 py-2 font-mono text-xs font-bold">
          RETURN TARGET: {redirectPath}
        </div>
      )}

      {login.submitError && (
        <StateNotice kind="error" title="Sign in failed" error={login.submitError} />
      )}

      <BrutalCard>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void login.form.handleSubmit();
          }}
        >
          <login.form.Field name="email">
            {(field) => {
              const fieldErr = firstFieldError(field.state.meta.errors);
              return (
                <label className="block text-xs font-bold uppercase">
                  Email
                  <input
                    type="email"
                    autoComplete="email"
                    value={field.state.value}
                    onBlur={field.handleBlur}
                    onChange={(e) => field.handleChange(e.target.value)}
                    placeholder="you@example.com"
                    className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 text-sm font-normal normal-case"
                  />
                  {fieldErr && (
                    <span className="mt-1 block font-mono text-xs font-bold normal-case text-status-dead-ink">
                      {fieldErr}
                    </span>
                  )}
                </label>
              );
            }}
          </login.form.Field>

          <login.form.Field name="password">
            {(field) => {
              const fieldErr = firstFieldError(field.state.meta.errors);
              return (
                <label className="block text-xs font-bold uppercase">
                  Password
                  <input
                    type="password"
                    autoComplete="current-password"
                    value={field.state.value}
                    onBlur={field.handleBlur}
                    onChange={(e) => field.handleChange(e.target.value)}
                    placeholder="Enter your password"
                    className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 text-sm font-normal normal-case"
                  />
                  {fieldErr && (
                    <span className="mt-1 block font-mono text-xs font-bold normal-case text-status-dead-ink">
                      {fieldErr}
                    </span>
                  )}
                </label>
              );
            }}
          </login.form.Field>

          <BrutalButton type="submit" variant="signal" className="w-full" disabled={login.isSubmitting}>
            <SignIn size={18} weight="bold" />
            {login.isSubmitting ? "Signing In..." : "Sign In"}
          </BrutalButton>
        </form>

        <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t-2 border-ink pt-4 text-sm">
          <span className="text-ink-muted">New to TREEAN?</span>
          <div className="flex flex-wrap gap-2">
            <Link
              to="/register"
              search={redirectPath ? { redirect: redirectPath } : {}}
              className="inline-flex min-h-[44px] items-center border-2 border-ink bg-canvas px-3 py-2 text-sm font-bold brutal-press"
            >
              Create Account
            </Link>
            <Link
              to="/"
              className="inline-flex min-h-[44px] items-center border-2 border-ink bg-surface px-3 py-2 text-sm font-bold brutal-press"
            >
              Browse Events
            </Link>
          </div>
        </div>
      </BrutalCard>
    </div>
  );
}
