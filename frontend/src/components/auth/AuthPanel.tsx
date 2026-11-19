import { Key, Plus, Shuffle, SignOut, UserSwitch, X } from "@phosphor-icons/react";
import type { useAuthPanel } from "@/hooks/useAuthPanel";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import type { DemoRole } from "@/store/auth-store";

type AuthPanelProps = {
  state: ReturnType<typeof useAuthPanel>;
};

export function AuthPanel({ state }: AuthPanelProps) {
  if (!state.panelOpen) return null;

  return (
    <div className="border-b-3 border-ink bg-surface-muted px-4 py-6">
      <div className="mx-auto max-w-6xl">
        <BrutalCard>
          <div className="flex flex-wrap items-center justify-between gap-3 border-b-2 border-ink pb-4">
            <div className="flex flex-wrap gap-2">
              <BrutalButton
                variant={state.tab === "demo" ? "signal" : "secondary"}
                onClick={() => state.setTab("demo")}
              >
                <UserSwitch size={16} weight="bold" />
                Demo Header Auth
              </BrutalButton>
              <BrutalButton
                variant={state.tab === "login" ? "signal" : "secondary"}
                onClick={() => state.setTab("login")}
              >
                <Key size={16} weight="bold" />
                JWT Login
              </BrutalButton>
              <BrutalButton
                variant={state.tab === "register" ? "signal" : "secondary"}
                onClick={() => state.setTab("register")}
              >
                <Plus size={16} weight="bold" />
                Register
              </BrutalButton>
            </div>
            <BrutalButton variant="secondary" onClick={() => state.setPanelOpen(false)} aria-label="Close identity drawer">
              <X size={16} weight="bold" />
              Close
            </BrutalButton>
          </div>

          {state.tab === "demo" && (
            <div className="mt-5 grid gap-6 md:grid-cols-2">
              <div>
                <h2 className="text-base font-bold">Seeded Demo Presets (X-User-ID + X-User-Role)</h2>
                <p className="mt-1 text-sm text-ink-muted">
                  Uses local opt-in demo headers (`ALLOW_DEMO_HEADER_AUTH=true`) matching `002_seed_demo.sql`.
                </p>
                <div className="mt-3 grid gap-2">
                  {state.seededAccounts.map((acc) => (
                    <div key={acc.id} className="flex flex-wrap items-center justify-between gap-2 border-2 border-ink bg-canvas p-3">
                      <div>
                        <p className="text-sm font-bold">{acc.name} ({acc.role})</p>
                        <p className="font-mono text-xs text-ink-muted">{acc.email}</p>
                      </div>
                      <div className="flex gap-2">
                        <BrutalButton variant="signal" onClick={() => state.switchToDemoAccount(acc)}>
                          Use Header
                        </BrutalButton>
                        <BrutalButton variant="secondary" disabled={state.isPending} onClick={() => state.quickJwtLogin(acc)}>
                          Login JWT
                        </BrutalButton>
                      </div>
                    </div>
                  ))}
                </div>
              </div>

              <div className="space-y-3 border-2 border-ink bg-canvas p-4">
                <h3 className="text-base font-bold">Custom Demo Header Actor</h3>
                <label className="block text-xs font-bold uppercase">
                  User UUID
                  <input
                    type="text"
                    value={state.customDemoId}
                    onChange={(e) => state.setCustomDemoId(e.target.value)}
                    placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
                    className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-sm"
                  />
                </label>
                <label className="block text-xs font-bold uppercase">
                  Role
                  <select
                    value={state.customDemoRole}
                    onChange={(e) => state.setCustomDemoRole(e.target.value as DemoRole)}
                    className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm font-bold"
                  >
                    <option value="BUYER">BUYER</option>
                    <option value="ORGANIZER">ORGANIZER</option>
                  </select>
                </label>
                <div className="flex flex-wrap gap-2 pt-1">
                  <BrutalButton variant="signal" onClick={state.applyCustomDemoIdentity}>
                    Apply Demo Headers
                  </BrutalButton>
                  <BrutalButton variant="secondary" onClick={state.rotateRandomBuyer}>
                    <Shuffle size={16} weight="bold" />
                    New Random Buyer
                  </BrutalButton>
                </div>
              </div>
            </div>
          )}

          {state.tab === "login" && (
            <form
              className="mt-5 grid max-w-lg gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                void state.loginForm.handleSubmit();
              }}
            >
              <state.loginForm.Field name="email">
                {(field) => (
                  <label className="block text-xs font-bold uppercase">
                    Email
                    <input
                      type="email"
                      value={field.state.value}
                      onChange={(e) => field.handleChange(e.target.value)}
                      placeholder="buyer1@event.local"
                      className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm"
                    />
                  </label>
                )}
              </state.loginForm.Field>
              <state.loginForm.Field name="password">
                {(field) => (
                  <label className="block text-xs font-bold uppercase">
                    Password
                    <input
                      type="password"
                      value={field.state.value}
                      onChange={(e) => field.handleChange(e.target.value)}
                      placeholder="password123"
                      className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm"
                    />
                  </label>
                )}
              </state.loginForm.Field>
              <div className="flex flex-wrap gap-2">
                <BrutalButton type="submit" variant="signal" disabled={state.isPending}>
                  {state.isPending ? "Signing in..." : "Sign In (JWT)"}
                </BrutalButton>
                {state.auth.accessToken && (
                  <BrutalButton variant="danger" onClick={state.signOutJwt}>
                    <SignOut size={16} weight="bold" />
                    Clear JWT
                  </BrutalButton>
                )}
              </div>
            </form>
          )}

          {state.tab === "register" && (
            <form
              className="mt-5 grid max-w-lg gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                void state.registerForm.handleSubmit();
              }}
            >
              <state.registerForm.Field name="name">
                {(field) => (
                  <label className="block text-xs font-bold uppercase">
                    Full Name
                    <input
                      type="text"
                      value={field.state.value}
                      onChange={(e) => field.handleChange(e.target.value)}
                      placeholder="Your Name"
                      className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm"
                    />
                  </label>
                )}
              </state.registerForm.Field>
              <state.registerForm.Field name="email">
                {(field) => (
                  <label className="block text-xs font-bold uppercase">
                    Email
                    <input
                      type="email"
                      value={field.state.value}
                      onChange={(e) => field.handleChange(e.target.value)}
                      placeholder="email@example.com"
                      className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm"
                    />
                  </label>
                )}
              </state.registerForm.Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <state.registerForm.Field name="password">
                  {(field) => (
                    <label className="block text-xs font-bold uppercase">
                      Password (min 6)
                      <input
                        type="password"
                        value={field.state.value}
                        onChange={(e) => field.handleChange(e.target.value)}
                        placeholder="Minimum 6 characters"
                        className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm"
                      />
                    </label>
                  )}
                </state.registerForm.Field>
                <state.registerForm.Field name="role">
                  {(field) => (
                    <label className="block text-xs font-bold uppercase">
                      Role
                      <select
                        value={field.state.value}
                        onChange={(e) => field.handleChange(e.target.value as DemoRole)}
                        className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm font-bold"
                      >
                        <option value="BUYER">BUYER</option>
                        <option value="ORGANIZER">ORGANIZER</option>
                      </select>
                    </label>
                  )}
                </state.registerForm.Field>
              </div>
              <BrutalButton type="submit" variant="signal" disabled={state.isPending}>
                {state.isPending ? "Creating account..." : "Create Account"}
              </BrutalButton>
            </form>
          )}
        </BrutalCard>
      </div>
    </div>
  );
}
