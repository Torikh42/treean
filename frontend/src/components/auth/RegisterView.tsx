import { Link } from "@tanstack/react-router";
import { UserPlus } from "@phosphor-icons/react";
import { firstFieldError, useRegisterForm } from "@/hooks/useAuthForms";
import type { UserRole } from "@/store/auth-store";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";

interface RegisterViewProps {
  register: ReturnType<typeof useRegisterForm>;
  redirectPath?: string;
}

const ROLE_OPTIONS: ReadonlyArray<{ role: UserRole; label: string; summary: string }> = [
  {
    role: "BUYER",
    label: "BUYER",
    summary: "Reserve and pay for event tickets with 15-minute holds.",
  },
  {
    role: "ORGANIZER",
    label: "ORGANIZER",
    summary: "Publish events, manage ticket tiers, and update quotas.",
  },
];

export function RegisterView({ register, redirectPath }: RegisterViewProps) {
  return (
    <div className="mx-auto max-w-lg space-y-6">
      <div className="border-b-3 border-ink pb-4">
        <h1 className="text-3xl font-bold tracking-tight">Create Account</h1>
        <p className="mt-1 text-sm text-ink-muted">
          Choose your role and register for a TREEAN JWT session.
        </p>
      </div>

      {register.submitError && (
        <StateNotice kind="error" title="Registration failed" error={register.submitError} />
      )}

      <BrutalCard>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void register.form.handleSubmit();
          }}
        >
          <register.form.Field name="name">
            {(field) => {
              const fieldErr = firstFieldError(field.state.meta.errors);
              return (
                <label className="block text-xs font-bold uppercase">
                  Full Name
                  <input
                    type="text"
                    autoComplete="name"
                    value={field.state.value}
                    onBlur={field.handleBlur}
                    onChange={(e) => field.handleChange(e.target.value)}
                    placeholder="Enter your full name"
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
          </register.form.Field>

          <register.form.Field name="email">
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
          </register.form.Field>

          <register.form.Field name="password">
            {(field) => {
              const fieldErr = firstFieldError(field.state.meta.errors);
              return (
                <label className="block text-xs font-bold uppercase">
                  Password (min 6 characters)
                  <input
                    type="password"
                    autoComplete="new-password"
                    value={field.state.value}
                    onBlur={field.handleBlur}
                    onChange={(e) => field.handleChange(e.target.value)}
                    placeholder="Minimum 6 characters"
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
          </register.form.Field>

          <register.form.Field name="role">
            {(field) => (
              <fieldset className="space-y-2">
                <legend className="text-xs font-bold uppercase">Account Role</legend>
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  {ROLE_OPTIONS.map((opt) => {
                    const selected = field.state.value === opt.role;
                    return (
                      <label
                        key={opt.role}
                        className={`min-h-[44px] cursor-pointer border-2 border-ink p-3 text-left transition-colors focus-within:ring-2 focus-within:ring-ink ${
                          selected
                            ? "bg-signal brutal-shadow-sm"
                            : "bg-canvas hover:bg-surface-muted"
                        }`}
                      >
                        <input
                          type="radio"
                          name="role"
                          value={opt.role}
                          checked={selected}
                          onChange={() => field.handleChange(opt.role)}
                          className="sr-only"
                        />
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-mono text-sm font-bold">{opt.label}</span>
                          <span className="border border-ink bg-surface px-1.5 py-0.5 font-mono text-[11px] font-bold">
                            {selected ? "SELECTED" : "SELECT"}
                          </span>
                        </div>
                        <p className="mt-1.5 text-xs text-ink-muted">{opt.summary}</p>
                      </label>
                    );
                  })}
                </div>
              </fieldset>
            )}
          </register.form.Field>

          <BrutalButton type="submit" variant="signal" className="w-full" disabled={register.isSubmitting}>
            <UserPlus size={18} weight="bold" />
            {register.isSubmitting ? "Creating Account..." : "Create Account"}
          </BrutalButton>
        </form>

        <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t-2 border-ink pt-4 text-sm">
          <span className="text-ink-muted">Already registered?</span>
          <Link
            to="/login"
            search={redirectPath ? { redirect: redirectPath } : {}}
            className="inline-flex min-h-[44px] items-center border-2 border-ink bg-canvas px-3 py-2 text-sm font-bold brutal-press"
          >
            Sign In Instead
          </Link>
        </div>
      </BrutalCard>
    </div>
  );
}
