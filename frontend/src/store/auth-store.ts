import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { User } from "@/lib/api/generated/types.gen";

export type AuthMode = "jwt" | "demo";
export type DemoRole = "BUYER" | "ORGANIZER";

export interface AuthState {
  authMode: AuthMode;
  accessToken: string | null;
  user: User | null;
  demoUserId: string;
  demoRole: DemoRole;
  setJwtSession: (user: User, accessToken: string) => void;
  setDemoIdentity: (userId: string, role: DemoRole) => void;
  setAuthMode: (mode: AuthMode) => void;
  clearSession: () => void;
}

const DEFAULT_DEMO_BUYER_ID = "22222222-2222-2222-2222-222222222221";

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      authMode: "demo",
      accessToken: null,
      user: null,
      demoUserId: DEFAULT_DEMO_BUYER_ID,
      demoRole: "BUYER",
      setJwtSession: (user, accessToken) =>
        set({
          authMode: "jwt",
          accessToken,
          user,
        }),
      setDemoIdentity: (demoUserId, demoRole) =>
        set({
          authMode: "demo",
          demoUserId,
          demoRole,
        }),
      setAuthMode: (authMode) => set({ authMode }),
      clearSession: () =>
        set({
          accessToken: null,
          user: null,
        }),
    }),
    {
      name: "treean-auth-session",
    },
  ),
);

export function getActiveRole(state: AuthState): DemoRole | null {
  if (state.authMode === "jwt") {
    if (!state.accessToken || !state.user) return null;
    return state.user.role === "ORGANIZER" ? "ORGANIZER" : "BUYER";
  }
  return state.demoUserId.trim() ? state.demoRole : null;
}

export function isAuthenticated(state: AuthState): boolean {
  if (state.authMode === "jwt") {
    return Boolean(state.accessToken && state.user);
  }
  return Boolean(state.demoUserId.trim());
}
