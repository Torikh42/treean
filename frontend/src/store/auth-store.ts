import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { User } from "@/lib/api/generated/types.gen";

export type UserRole = "BUYER" | "ORGANIZER";

export interface AuthState {
  accessToken: string | null;
  user: User | null;
  setJwtSession: (user: User, accessToken: string) => void;
  updateUser: (user: User) => void;
  clearSession: () => void;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      accessToken: null,
      user: null,
      setJwtSession: (user, accessToken) =>
        set({
          accessToken,
          user,
        }),
      updateUser: (user) =>
        set({
          user,
        }),
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

export function getActiveRole(state: Pick<AuthState, "accessToken" | "user">): UserRole | null {
  if (!state.accessToken || !state.user) return null;
  if (state.user.role === "ORGANIZER") return "ORGANIZER";
  if (state.user.role === "BUYER") return "BUYER";
  return null;
}

export function isAuthenticated(state: Pick<AuthState, "accessToken" | "user">): boolean {
  return Boolean(state.accessToken && state.user);
}
