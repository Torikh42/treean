import { createFileRoute } from "@tanstack/react-router";
import { FlashSaleLabPanel } from "@/components/lab/FlashSaleLabPanel";
import { WebhookSimulatorPanel } from "@/components/lab/WebhookSimulatorPanel";
import { useFlashSaleLab } from "@/hooks/useFlashSaleLab";
import { useWebhookLab } from "@/hooks/useWebhookLab";

type FlashSaleSearch = {
  orderId?: string;
  grossAmount?: number;
};

export const Route = createFileRoute("/flash-sale")({
  validateSearch: (search: Record<string, unknown>): FlashSaleSearch => ({
    orderId: typeof search.orderId === "string" ? search.orderId : undefined,
    grossAmount:
      typeof search.grossAmount === "number"
        ? search.grossAmount
        : typeof search.grossAmount === "string" && search.grossAmount !== "" && !Number.isNaN(Number(search.grossAmount))
          ? Number(search.grossAmount)
          : undefined,
  }),
  component: FlashSalePage,
});

function FlashSalePage() {
  const { orderId, grossAmount } = Route.useSearch();
  const flash = useFlashSaleLab();
  const webhookLab = useWebhookLab({
    initialOrderId: orderId,
    initialGrossAmount: grossAmount,
  });

  return (
    <div className="space-y-8">
      <div className="border-b-3 border-ink pb-4">
        <h1 className="text-3xl font-bold tracking-tight">Flash Sale & Webhook Lab</h1>
        <p className="mt-1 text-sm text-ink-muted">
          Verify zero-oversell PostgreSQL serialization under 200 concurrent buyers and SHA-512 webhook idempotency.
        </p>
      </div>

      <FlashSaleLabPanel
        requests={flash.requests}
        onRequestsChange={flash.setRequests}
        quota={flash.quota}
        onQuotaChange={flash.setQuota}
        report={flash.report}
        submitError={flash.submitError}
        isRunning={flash.isRunning}
        onRun={flash.runFlashSale}
      />

      <WebhookSimulatorPanel lab={webhookLab} />
    </div>
  );
}
