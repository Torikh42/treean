import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  demoFlashSaleMutation,
  listEventsQueryKey,
} from "@/lib/api/generated/@tanstack/react-query.gen";
import type { FlashSaleReport } from "@/lib/api/generated/types.gen";
import { parseApiError, type ParsedApiError } from "@/lib/api/errors";

export function useFlashSaleLab() {
  const queryClient = useQueryClient();
  const [requests, setRequests] = useState(200);
  const [quota, setQuota] = useState(100);
  const [report, setReport] = useState<FlashSaleReport | null>(null);
  const [submitError, setSubmitError] = useState<ParsedApiError | null>(null);

  const flashMut = useMutation({
    ...demoFlashSaleMutation(),
    onSuccess: (data) => {
      setReport(data);
      setSubmitError(null);
      void queryClient.invalidateQueries({ queryKey: listEventsQueryKey() });
      toast.success(
        `Flash sale done: ${data.succeeded_bookings} sold / ${data.rejected_quota_empty} rejected`,
      );
    },
    onError: (err) => {
      const parsed = parseApiError(err);
      setSubmitError(parsed);
      toast.error(`${parsed.code}: ${parsed.detail}`);
    },
  });

  function runFlashSale() {
    setSubmitError(null);
    flashMut.mutate({
      body: {
        requests: Math.max(1, Math.min(500, Number(requests) || 200)),
        quota: Math.max(1, Math.min(500, Number(quota) || 100)),
      },
    });
  }

  return {
    requests,
    setRequests,
    quota,
    setQuota,
    report,
    submitError,
    isRunning: flashMut.isPending,
    runFlashSale,
  };
}
