import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useForm } from "@tanstack/react-form";
import { z } from "zod";
import { toast } from "sonner";
import {
  loginUserMutation,
  registerUserMutation,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import { parseApiError } from "@/lib/api/errors";
import { SEEDED_DEMO_ACCOUNTS, type SeededDemoAccount } from "@/lib/demo-accounts";
import { getActiveRole, isAuthenticated, useAuthStore, type DemoRole } from "@/store/auth-store";

const loginSchema = z.object({
  email: z.string().email("Valid email required"),
  password: z.string().min(1, "Password is required"),
});

const registerSchema = z.object({
  email: z.string().email("Valid email required"),
  name: z.string().min(2, "Name must be at least 2 characters").max(100),
  password: z.string().min(6, "Password must be at least 6 characters").max(72),
  role: z.enum(["BUYER", "ORGANIZER"]),
});

export function useAuthPanel() {
  const queryClient = useQueryClient();
  const auth = useAuthStore();
  const [panelOpen, setPanelOpen] = useState(false);
  const [tab, setTab] = useState<"demo" | "login" | "register">("demo");
  const [customDemoId, setCustomDemoId] = useState(auth.demoUserId);
  const [customDemoRole, setCustomDemoRole] = useState<DemoRole>(auth.demoRole);

  const loginMut = useMutation({
    ...loginUserMutation(),
    onSuccess: (data) => {
      auth.setJwtSession(data.user, data.access_token);
      void queryClient.invalidateQueries();
      setPanelOpen(false);
      toast.success(`Signed in as ${data.user.email} (${data.user.role})`);
    },
    onError: (err) => {
      const parsed = parseApiError(err);
      toast.error(`${parsed.code}: ${parsed.detail}`);
    },
  });

  const registerMut = useMutation({
    ...registerUserMutation(),
    onSuccess: (data) => {
      auth.setJwtSession(data.user, data.access_token);
      void queryClient.invalidateQueries();
      setPanelOpen(false);
      toast.success(`Registered ${data.user.email} (${data.user.role})`);
    },
    onError: (err) => {
      const parsed = parseApiError(err);
      toast.error(`${parsed.code}: ${parsed.detail}`);
    },
  });

  const loginForm = useForm({
    defaultValues: {
      email: "",
      password: "",
    },
    validators: {
      onSubmit: loginSchema,
    },
    onSubmit: async ({ value }) => {
      await loginMut.mutateAsync({ body: value });
    },
  });

  const registerForm = useForm({
    defaultValues: {
      email: "",
      name: "",
      password: "",
      role: "BUYER" as DemoRole,
    },
    validators: {
      onSubmit: registerSchema,
    },
    onSubmit: async ({ value }) => {
      await registerMut.mutateAsync({ body: value });
    },
  });

  function switchToDemoAccount(account: SeededDemoAccount) {
    setCustomDemoId(account.id);
    setCustomDemoRole(account.role);
    auth.setDemoIdentity(account.id, account.role);
    void queryClient.invalidateQueries();
    toast.success(`Demo header set to ${account.email} (${account.role})`);
  }

  function rotateRandomBuyer() {
    const id = globalThis.crypto?.randomUUID
      ? globalThis.crypto.randomUUID()
      : `00000000-0000-4000-8000-${String(Date.now()).padStart(12, "0").slice(-12)}`;
    setCustomDemoId(id);
    setCustomDemoRole("BUYER");
    auth.setDemoIdentity(id, "BUYER");
    void queryClient.invalidateQueries();
    toast.success("Switched to fresh random Buyer UUID");
  }

  function signOutJwt() {
    auth.clearSession();
    void queryClient.invalidateQueries();
    toast.success("Signed out");
  }

  return {
    panelOpen,
    setPanelOpen,
    tab,
    setTab,
    auth,
    activeRole: getActiveRole(auth),
    authenticated: isAuthenticated(auth),
    seededAccounts: SEEDED_DEMO_ACCOUNTS,
    loginForm,
    registerForm,
    isPending: loginMut.isPending || registerMut.isPending,
    switchToDemoAccount,
    customDemoId,
    setCustomDemoId,
    customDemoRole,
    setCustomDemoRole,
    applyCustomDemoIdentity: () => {
      auth.setDemoIdentity(customDemoId.trim(), customDemoRole);
      void queryClient.invalidateQueries();
      toast.success(`Demo identity updated (${customDemoRole})`);
    },
    rotateRandomBuyer,
    signOutJwt,
    quickJwtLogin: (account: SeededDemoAccount) => {
      loginMut.mutate({
        body: { email: account.email, password: account.password },
      });
    },
  };
}
