import { beforeEach, describe, expect, it } from "vitest";
import { getActiveRole, isAuthenticated, useAuthStore } from "@/store/auth-store";

describe("auth store", () => {
  beforeEach(() => {
    useAuthStore.setState({
      authMode: "demo",
      accessToken: null,
      user: null,
      demoUserId: "22222222-2222-2222-2222-222222222221",
      demoRole: "BUYER",
    });
  });

  it("defaults to demo buyer header mode", () => {
    const state = useAuthStore.getState();
    expect(isAuthenticated(state)).toBe(true);
    expect(getActiveRole(state)).toBe("BUYER");
  });

  it("switches cleanly to JWT organizer session and clears cleanly", () => {
    useAuthStore.getState().setJwtSession(
      {
        id: "11111111-1111-1111-1111-111111111111",
        email: "organizer@event.local",
        name: "Demo Organizer",
        role: "ORGANIZER",
        created_at: "2026-11-15T00:00:00Z",
      },
      "jwt-token",
    );

    const signedIn = useAuthStore.getState();
    expect(signedIn.authMode).toBe("jwt");
    expect(getActiveRole(signedIn)).toBe("ORGANIZER");
    expect(isAuthenticated(signedIn)).toBe(true);

    signedIn.clearSession();
    const signedOut = useAuthStore.getState();
    expect(isAuthenticated(signedOut)).toBe(false);
    expect(getActiveRole(signedOut)).toBeNull();
  });
});
