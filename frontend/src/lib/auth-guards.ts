import { redirect } from "@tanstack/react-router";
import {
  getActiveRole,
  isAuthenticated,
  useAuthStore,
  type UserRole,
} from "@/store/auth-store";

export function safeRedirectPath(raw?: unknown): string {
  if (typeof raw !== "string") return "/";
  if (!raw.startsWith("/") || raw.startsWith("//") || raw.includes("\\")) {
    return "/";
  }
  return raw;
}

export function defaultHomeForRole(role: UserRole | null): "/" | "/organizer" {
  return role === "ORGANIZER" ? "/organizer" : "/";
}

export function resolvePostAuthRedirect(rawRedirect: unknown, role: UserRole): string {
  if (
    typeof rawRedirect !== "string" ||
    !rawRedirect.startsWith("/") ||
    rawRedirect.startsWith("//") ||
    rawRedirect.includes("\\")
  ) {
    return defaultHomeForRole(role);
  }

  const safe = safeRedirectPath(rawRedirect);
  const pathOnly = safe.split(/[?#]/)[0] ?? safe;

  if (pathOnly === "/login" || pathOnly === "/register" || pathOnly === "/forbidden") {
    return defaultHomeForRole(role);
  }

  if (role === "ORGANIZER" && (pathOnly === "/orders" || pathOnly.startsWith("/orders/"))) {
    return "/organizer";
  }

  if (role === "BUYER" && (pathOnly === "/organizer" || pathOnly.startsWith("/organizer/"))) {
    return "/";
  }

  return safe;
}

export function requireGuest(): void {
  const state = useAuthStore.getState();
  if (isAuthenticated(state)) {
    throw redirect({
      to: defaultHomeForRole(getActiveRole(state)),
    });
  }
}

export function requireRole(role: UserRole, redirectTo?: string): void {
  const state = useAuthStore.getState();
  if (!isAuthenticated(state)) {
    throw redirect({
      to: "/login",
      search: {
        redirect: safeRedirectPath(redirectTo),
      },
    });
  }

  if (getActiveRole(state) !== role) {
    throw redirect({
      to: "/forbidden",
      search: {
        required: role,
      },
    });
  }
}
