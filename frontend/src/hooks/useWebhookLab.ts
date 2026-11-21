import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  getOrderQueryKey,
  listEventsQueryKey,
  listMyOrdersQueryKey,
  paymentWebhookMutation,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import type { WebhookResult } from "@/lib/api/generated/types.gen";
import { toastApiError, type ParsedApiError } from "@/lib/api/errors";
import { computeSha512Hex } from "@/lib/format";

interface WebhookLabInitial {
  initialOrderId?: string;
  initialGrossAmount?: number;
}

interface WebhookFormState {
  key: string;
  orderId: string;
  statusCode: string;
  grossAmount: number;
  webhookSecret: string;
  signatureKey: string;
}

export function useWebhookLab(initial?: WebhookLabInitial) {
  const queryClient = useQueryClient();
  const initialKey = `${initial?.initialOrderId ?? ""}:${initial?.initialGrossAmount ?? ""}`;
  const [transactionId, setTransactionId] = useState(() => `lab-${Date.now()}`);
  const [transactionStatus, setTransactionStatus] = useState("settlement");
  const [formState, setFormState] = useState<WebhookFormState>(() => ({
    key: initialKey,
    orderId: initial?.initialOrderId ?? "",
    statusCode: "200",
    grossAmount: initial?.initialGrossAmount ?? 150000,
    webhookSecret: "dev-webhook-secret-change-in-production",
    signatureKey: "",
  }));
  const [lastResult, setLastResult] = useState<WebhookResult | null>(null);
  const [lastError, setLastError] = useState<ParsedApiError | null>(null);

  function resolveCurrent(prev: WebhookFormState): WebhookFormState {
    if (prev.key === initialKey) return prev;
    return {
      key: initialKey,
      orderId: initial?.initialOrderId ?? prev.orderId,
      statusCode: prev.statusCode,
      grossAmount: initial?.initialGrossAmount ?? prev.grossAmount,
      webhookSecret: prev.webhookSecret,
      signatureKey: "",
    };
  }

  const currentForm = resolveCurrent(formState);
  const { orderId, statusCode, grossAmount, webhookSecret, signatureKey } = currentForm;

  function setOrderId(val: string) {
    setFormState((prev) => ({ ...resolveCurrent(prev), orderId: val, signatureKey: "" }));
  }

  function setStatusCode(val: string) {
    setFormState((prev) => ({ ...resolveCurrent(prev), statusCode: val, signatureKey: "" }));
  }

  function setGrossAmount(val: number) {
    setFormState((prev) => ({ ...resolveCurrent(prev), grossAmount: val, signatureKey: "" }));
  }

  function setWebhookSecret(val: string) {
    setFormState((prev) => ({ ...resolveCurrent(prev), webhookSecret: val, signatureKey: "" }));
  }

  function setSignatureKey(val: string) {
    setFormState((prev) => ({ ...resolveCurrent(prev), signatureKey: val }));
  }

  const hookMut = useMutation({
    ...paymentWebhookMutation(),
    onSuccess: (res) => {
      setLastResult(res);
      setLastError(null);
      if (orderId.trim()) {
        void queryClient.invalidateQueries({ queryKey: getOrderQueryKey({ path: { id: orderId.trim() } }) });
      }
      void queryClient.invalidateQueries({ queryKey: listMyOrdersQueryKey() });
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
      toast.success(`Webhook outcome: ${res.outcome}`);
    },
    onError: (err) => {
      setLastResult(null);
      setLastError(toastApiError(err));
    },
  });

  async function computeValidSignature() {
    const raw = `${orderId.trim()}${statusCode.trim()}${grossAmount}${webhookSecret}`;
    try {
      const sig = await computeSha512Hex(raw);
      setSignatureKey(sig);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to compute SHA-512 signature");
    }
  }

  function sendWebhook() {
    setLastError(null);
    hookMut.mutate({
      body: {
        transaction_id: transactionId.trim(),
        order_id: orderId.trim(),
        status_code: statusCode.trim(),
        gross_amount: Number(grossAmount),
        transaction_status: transactionStatus,
        signature_key: signatureKey.trim(),
      },
    });
  }

  return {
    transactionId,
    setTransactionId,
    orderId,
    setOrderId,
    statusCode,
    setStatusCode,
    grossAmount,
    setGrossAmount,
    transactionStatus,
    setTransactionStatus,
    webhookSecret,
    setWebhookSecret,
    signatureKey,
    setSignatureKey,
    lastResult,
    lastError,
    isSending: hookMut.isPending,
    computeValidSignature,
    sendWebhook,
  };
}
