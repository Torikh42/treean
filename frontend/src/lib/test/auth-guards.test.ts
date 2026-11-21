import { isRedirect } from "@tanstack/react-router";
import { beforeEach, describe, expect, it } from "vitest";
import type { User } from "@/lib/api/generated/types.gen";
import {
  defaultHomeForRole,
  requireGuest,
  requireRole,
  resolvePostAuthRedirect,
  safeRedirectPath,
} from "@/lib/auth-guards";
import { useAuthStore } from "@/store/auth-store";

const buyerUser: User = {
  id: "22222222-2222-2222-2222-222222222221",
  email: "buyer1@event.local",
  name: "Buyer One",
  role: "BUYER",
  created_at: "2026-11-15T00:00:00Z",
};

const organizerUser: User = {
  id: "11111111-1111-1111-1111-111111111111",
  email: "organizer@event.local",
  name: "Organizer One",
  role: "ORGANIZER",
  created_at: "2026-11-15T00:00:00Z",
};

function captureRedirect(fn: () => void): { to?: string; search?: Record<string, unknown> } | null {
  try {
    fn();
    return null;
  } catch (err) {
    expect(isRedirect(err)).toBe(true);
    return (err as { options: { to?: string; search?: Record<string, unknown> } }).options;
  }
}

describe("auth guards", () => {
  beforeEach(() => {
    useAuthStore.setState({
      accessToken: null,
      user: null,
    });
  });

  it("sanitizes redirect paths strictly", () => {
    expect(safeRedirectPath("/orders")).toBe("/orders");
    expect(safeRedirectPath("/events/123?tab=tiers")).toBe("/events/123?tab=tiers");
    expect(safeRedirectPath("/events/123#tiers")).toBe("/events/123#tiers");
    expect(safeRedirectPath(undefined)).toBe("/");
    expect(safeRedirectPath("")).toBe("/");
    expect(safeRedirectPath("orders")).toBe("/");
    expect(safeRedirectPath("https://evil.example")).toBe("/");
    expect(safeRedirectPath("//evil.example")).toBe("/");
    expect(safeRedirectPath("/\\evil.example")).toBe("/");
    expect(safeRedirectPath("/orders\\bad")).toBe("/");
    expect(safeRedirectPath("\\evil.example")).toBe("/");
    expect(safeRedirectPath("/\t/evil.example")).toBe("/");
    expect(safeRedirectPath("/\n/evil.example")).toBe("/");
  });

  it("resolves default home per role", () => {
    expect(defaultHomeForRole("ORGANIZER")).toBe("/organizer");
    expect(defaultHomeForRole("BUYER")).toBe("/");
    expect(defaultHomeForRole(null)).toBe("/");
  });

  it("resolves post-auth redirect respecting role compatibility", () => {
    expect(resolvePostAuthRedirect(undefined, "BUYER")).toBe("/");
    expect(resolvePostAuthRedirect(undefined, "ORGANIZER")).toBe("/organizer");
    expect(resolvePostAuthRedirect("/events/abc", "BUYER")).toBe("/events/abc");
    expect(resolvePostAuthRedirect("/events/abc#tiers", "BUYER")).toBe("/events/abc#tiers");
    expect(resolvePostAuthRedirect("/orders", "ORGANIZER")).toBe("/organizer");
    expect(resolvePostAuthRedirect("/orders#latest", "ORGANIZER")).toBe("/organizer");
    expect(resolvePostAuthRedirect("/organizer", "BUYER")).toBe("/");
    expect(resolvePostAuthRedirect("/organizer#top", "BUYER")).toBe("/");
    expect(resolvePostAuthRedirect("/login", "ORGANIZER")).toBe("/organizer");
    expect(resolvePostAuthRedirect("/login#form", "ORGANIZER")).toBe("/organizer");
    expect(resolvePostAuthRedirect("/register#step", "BUYER")).toBe("/");
    expect(resolvePostAuthRedirect("/\\evil.example", "BUYER")).toBe("/");
  });

  it("allows guests and redirects authenticated users in requireGuest", () => {
    expect(() => requireGuest()).not.toThrow();

    useAuthStore.getState().setJwtSession(buyerUser, "buyer-jwt");
    expect(captureRedirect(() => requireGuest())).toMatchObject({ to: "/" });

    useAuthStore.getState().setJwtSession(organizerUser, "org-jwt");
    expect(captureRedirect(() => requireGuest())).toMatchObject({ to: "/organizer" });
  });

  it("enforces authentication and role matching in requireRole", () => {
    const unauthRedirect = captureRedirect(() => requireRole("BUYER", "/orders"));
    expect(unauthRedirect).toMatchObject({
      to: "/login",
      search: { redirect: "/orders" },
    });

    const sanitizedUnauth = captureRedirect(() => requireRole("ORGANIZER", "//evil.example"));
    expect(sanitizedUnauth).toMatchObject({
      to: "/login",
      search: { redirect: "/" },
    });

    useAuthStore.getState().setJwtSession(buyerUser, "buyer-jwt");
    expect(() => requireRole("BUYER", "/orders")).not.toThrow();

    const wrongRole = captureRedirect(() => requireRole("ORGANIZER", "/organizer"));
    expect(wrongRole).toMatchObject({
      to: "/forbidden",
      search: { required: "ORGANIZER" },
    });
  });
});
