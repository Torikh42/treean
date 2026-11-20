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

export function useWebhookLab(initial?: WebhookLabInitial) {
  const queryClient = useQueryClient();
  const [transactionId, setTransactionId] = useState(() => `lab-${Date.now()}`);
  const [orderId, setOrderId] = useState(initial?.initialOrderId ?? "");
  const [statusCode, setStatusCode] = useState("200");
  const [grossAmount, setGrossAmount] = useState(initial?.initialGrossAmount ?? 150000);
  const [transactionStatus, setTransactionStatus] = useState("settlement");
  const [webhookSecret, setWebhookSecret] = useState("dev-webhook-secret-change-in-production");
  const [signatureKey, setSignatureKey] = useState("");
  const [lastResult, setLastResult] = useState<WebhookResult | null>(null);
  const [lastError, setLastError] = useState<ParsedApiError | null>(null);

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
