import { useState } from "react";
import { useForm } from "@tanstack/react-form";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { z } from "zod";
import { toastApiError, type ParsedApiError } from "@/lib/api/errors";
import {
  getMeQueryKey,
  loginUserMutation,
  registerUserMutation,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import { resolvePostAuthRedirect } from "@/lib/auth-guards";
import { useAuthStore, type UserRole } from "@/store/auth-store";

const loginSchema = z.object({
  email: z.string().trim().email("Enter a valid email address"),
  password: z.string().min(1, "Password is required"),
});

const registerSchema = z.object({
  name: z.string().trim().min(2, "Name must be at least 2 characters").max(100, "Name must be at most 100 characters"),
  email: z.string().trim().email("Enter a valid email address"),
  password: z.string().min(6, "Password must be at least 6 characters").max(72, "Password must be at most 72 characters"),
  role: z.enum(["BUYER", "ORGANIZER"]),
});

export function firstFieldError(errors: ReadonlyArray<unknown>): string | null {
  const err = errors[0];
  if (!err) return null;
  if (typeof err === "string") return err;
  if (typeof err === "object" && "message" in err && typeof (err as { message: unknown }).message === "string") {
    return (err as { message: string }).message;
  }
  return "Invalid value";
}

function normalizeUserRole(raw: string): UserRole {
  return raw === "ORGANIZER" ? "ORGANIZER" : "BUYER";
}

export function useLoginForm(redirectPath?: string) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const setJwtSession = useAuthStore((s) => s.setJwtSession);
  const [submitError, setSubmitError] = useState<ParsedApiError | null>(null);

  const loginMut = useMutation({
    ...loginUserMutation(),
    onSuccess: (data) => {
      setSubmitError(null);
      const role = normalizeUserRole(data.user.role);
      setJwtSession(data.user, data.access_token);
      queryClient.setQueryData(getMeQueryKey(), data.user);
      void queryClient.invalidateQueries();
      toast.success(`Signed in as ${data.user.email} (${role})`);
      const target = resolvePostAuthRedirect(redirectPath, role);
      void navigate({ href: target });
    },
    onError: (err) => {
      setSubmitError(toastApiError(err));
    },
  });

  const form = useForm({
    defaultValues: {
      email: "",
      password: "",
    },
    validators: {
      onSubmit: loginSchema,
    },
    onSubmit: async ({ value }) => {
      setSubmitError(null);
      try {
        await loginMut.mutateAsync({
          body: {
            email: value.email.trim(),
            password: value.password,
          },
        });
      } catch {
        // Error state is handled by loginMut.onError
      }
    },
  });

  return {
    form,
    isSubmitting: loginMut.isPending,
    submitError,
  };
}

export function useRegisterForm(redirectPath?: string) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const setJwtSession = useAuthStore((s) => s.setJwtSession);
  const [submitError, setSubmitError] = useState<ParsedApiError | null>(null);

  const registerMut = useMutation({
    ...registerUserMutation(),
    onSuccess: (data) => {
      setSubmitError(null);
      const role = normalizeUserRole(data.user.role);
      setJwtSession(data.user, data.access_token);
      queryClient.setQueryData(getMeQueryKey(), data.user);
      void queryClient.invalidateQueries();
      toast.success(`Registered ${data.user.email} (${role})`);
      const target = resolvePostAuthRedirect(redirectPath, role);
      void navigate({ href: target });
    },
    onError: (err) => {
      setSubmitError(toastApiError(err));
    },
  });

  const form = useForm({
    defaultValues: {
      name: "",
      email: "",
      password: "",
      role: "BUYER" as UserRole,
    },
    validators: {
      onSubmit: registerSchema,
    },
    onSubmit: async ({ value }) => {
      setSubmitError(null);
      try {
        await registerMut.mutateAsync({
          body: {
            name: value.name.trim(),
            email: value.email.trim(),
            password: value.password,
            role: value.role,
          },
        });
      } catch {
        // Error state is handled by registerMut.onError
      }
    },
  });

  return {
    form,
    isSubmitting: registerMut.isPending,
    submitError,
  };
}
