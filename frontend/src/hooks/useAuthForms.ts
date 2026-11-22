import { useState } from "react";
import { useForm } from "@tanstack/react-form";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { z } from "zod";
import { toastApiError, type ParsedApiError } from "@/lib/api/errors";
import {
  getMeQueryKey,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import { loginUser, registerUser } from "@/lib/api/generated/sdk.gen";
import type {
  AuthResult,
  LoginBodyWritable,
  LoginUserError,
  RegisterBodyWritable,
  RegisterUserError,
} from "@/lib/api/generated/types.gen";
import { resolvePostAuthRedirect } from "@/lib/auth-guards";
import { solvePowChallenge } from "@/lib/pow";
import { useAuthStore, type UserRole } from "@/store/auth-store";

const POW_CHALLENGE_HEADER = "X-PoW-Challenge";
const POW_SOLUTION_HEADER = "X-PoW-Solution";

export type AuthAttemptResult<TData, TError> = (
  | { data: TData; error: undefined }
  | { data: undefined; error: TError }
) & {
  response?: Response;
};

function isChallengeRequired(error: unknown, response?: Response): boolean {
  if (response?.status !== 403) return false;
  if (!error || typeof error !== "object") return false;
  const candidate = error as { error_code?: unknown };
  return candidate.error_code === "CHALLENGE_REQUIRED";
}

/**
 * Executes an auth request and transparently solves one adaptive PoW challenge
 * when the backend returns HTTP 403 `CHALLENGE_REQUIRED` with `X-PoW-Challenge`.
 */
export async function executeAuthWithPowRetry<TData, TError>(
  callAuth: (headers?: Record<string, string>) => Promise<AuthAttemptResult<TData, TError>>,
): Promise<TData> {
  const firstResult = await callAuth();
  if (firstResult.error === undefined && firstResult.data !== undefined) {
    return firstResult.data;
  }

  const firstError = firstResult.error;
  if (!isChallengeRequired(firstError, firstResult.response)) {
    throw firstError;
  }

  const challengeToken = firstResult.response?.headers.get(POW_CHALLENGE_HEADER)?.trim();
  if (!challengeToken) {
    throw firstError;
  }

  let solution: string;
  try {
    solution = await solvePowChallenge(challengeToken);
  } catch (solveErr) {
    if (firstError && typeof firstError === "object" && solveErr instanceof Error) {
      throw { ...firstError, detail: solveErr.message };
    }
    throw firstError;
  }

  const retryResult = await callAuth({
    [POW_SOLUTION_HEADER]: solution,
  });
  if (retryResult.error === undefined && retryResult.data !== undefined) {
    return retryResult.data;
  }
  throw retryResult.error;
}

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

  const loginMut = useMutation<AuthResult, LoginUserError, { body: LoginBodyWritable }>({
    mutationFn: ({ body }) =>
      executeAuthWithPowRetry<AuthResult, LoginUserError>((headers) =>
        loginUser({
          body,
          headers,
          throwOnError: false,
        }),
      ),
    onSuccess: (data) => {
      setSubmitError(null);
      const role = normalizeUserRole(data.user.role);
      setJwtSession(data.user, data.access_token);
      queryClient.clear();
      queryClient.setQueryData([...getMeQueryKey(), data.access_token], data.user);
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

  const registerMut = useMutation<AuthResult, RegisterUserError, { body: RegisterBodyWritable }>({
    mutationFn: ({ body }) =>
      executeAuthWithPowRetry<AuthResult, RegisterUserError>((headers) =>
        registerUser({
          body,
          headers,
          throwOnError: false,
        }),
      ),
    onSuccess: (data) => {
      setSubmitError(null);
      const role = normalizeUserRole(data.user.role);
      setJwtSession(data.user, data.access_token);
      queryClient.clear();
      queryClient.setQueryData([...getMeQueryKey(), data.access_token], data.user);
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
