import { beforeEach, describe, expect, it } from "vitest";
import type { User } from "@/lib/api/generated/types.gen";
import { getActiveRole, isAuthenticated, useAuthStore } from "@/store/auth-store";

const organizerUser: User = {
  id: "11111111-1111-1111-1111-111111111111",
  email: "organizer@event.local",
  name: "Demo Organizer",
  role: "ORGANIZER",
  created_at: "2026-11-15T00:00:00Z",
};

const buyerUser: User = {
  id: "22222222-2222-2222-2222-222222222221",
  email: "buyer1@event.local",
  name: "Buyer One",
  role: "BUYER",
  created_at: "2026-11-15T00:00:00Z",
};

describe("auth store", () => {
  beforeEach(() => {
    useAuthStore.setState({
      accessToken: null,
      user: null,
    });
  });

  it("defaults to unauthenticated state", () => {
    const state = useAuthStore.getState();
    expect(state.accessToken).toBeNull();
    expect(state.user).toBeNull();
    expect(isAuthenticated(state)).toBe(false);
    expect(getActiveRole(state)).toBeNull();
  });

  it("stores JWT session, updates user profile, and clears cleanly", () => {
    useAuthStore.getState().setJwtSession(organizerUser, "jwt-token-1");

    const signedIn = useAuthStore.getState();
    expect(signedIn.accessToken).toBe("jwt-token-1");
    expect(signedIn.user?.email).toBe("organizer@event.local");
    expect(getActiveRole(signedIn)).toBe("ORGANIZER");
    expect(isAuthenticated(signedIn)).toBe(true);

    useAuthStore.getState().updateUser({ ...buyerUser, name: "Updated Buyer" });
    const updated = useAuthStore.getState();
    expect(updated.accessToken).toBe("jwt-token-1");
    expect(updated.user?.name).toBe("Updated Buyer");
    expect(getActiveRole(updated)).toBe("BUYER");

    updated.clearSession();
    const signedOut = useAuthStore.getState();
    expect(signedOut.accessToken).toBeNull();
    expect(signedOut.user).toBeNull();
    expect(isAuthenticated(signedOut)).toBe(false);
    expect(getActiveRole(signedOut)).toBeNull();
  });
});
