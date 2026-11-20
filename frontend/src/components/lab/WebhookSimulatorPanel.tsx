import { ShieldCheck } from "@phosphor-icons/react";
import type { useWebhookLab } from "@/hooks/useWebhookLab";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";
import { StatusBadge } from "@/components/ui/StatusBadge";

export function WebhookSimulatorPanel({ lab }: { lab: ReturnType<typeof useWebhookLab> }) {
  return (
    <BrutalCard muted>
      <div className="border-b-2 border-ink pb-3">
        <h2 className="flex items-center gap-2 text-2xl font-bold">
          <ShieldCheck size={22} weight="bold" />
          Signed Webhook & Late-Capture Tester
        </h2>
        <p className="mt-1 text-sm text-ink-muted">
          Tests POST /api/v1/webhooks/payment directly for replay idempotency, wrong signature (401), amount mismatch (400), and post-expiry audit flags.
        </p>
      </div>

      <div className="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <label className="block text-xs font-bold uppercase">
          Order UUID
          <input
            type="text"
            value={lab.orderId}
            onChange={(e) => lab.setOrderId(e.target.value)}
            placeholder="Paste order UUID"
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-xs"
          />
        </label>
        <label className="block text-xs font-bold uppercase">
          Transaction ID (replay same ID)
          <input
            type="text"
            value={lab.transactionId}
            onChange={(e) => lab.setTransactionId(e.target.value)}
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-xs"
          />
        </label>
        <label className="block text-xs font-bold uppercase">
          Transaction Status
          <select
            value={lab.transactionStatus}
            onChange={(e) => lab.setTransactionStatus(e.target.value)}
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 text-sm font-bold"
          >
            <option value="settlement">settlement</option>
            <option value="capture">capture</option>
            <option value="deny">deny</option>
            <option value="cancel">cancel</option>
            <option value="expire">expire</option>
          </select>
        </label>
        <label className="block text-xs font-bold uppercase">
          Gross Amount (IDR)
          <input
            type="number"
            value={lab.grossAmount}
            onChange={(e) => lab.setGrossAmount(Number(e.target.value))}
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-sm"
          />
        </label>
        <label className="block text-xs font-bold uppercase">
          Status Code
          <input
            type="text"
            value={lab.statusCode}
            onChange={(e) => lab.setStatusCode(e.target.value)}
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-sm"
          />
        </label>
        <label className="block text-xs font-bold uppercase">
          WEBHOOK_SECRET (Local Compute)
          <input
            type="text"
            value={lab.webhookSecret}
            onChange={(e) => lab.setWebhookSecret(e.target.value)}
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-xs"
          />
        </label>
        <label className="block text-xs font-bold uppercase sm:col-span-2 lg:col-span-3">
          signature_key (SHA-512 Hex - clear or edit to test 401)
          <input
            type="text"
            value={lab.signatureKey}
            onChange={(e) => lab.setSignatureKey(e.target.value)}
            placeholder="Click Sign SHA-512 below or leave blank to trigger 401 UNAUTHORIZED_WEBHOOK"
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-surface px-3 font-mono text-xs"
          />
        </label>
      </div>

      <div className="mt-4 flex flex-wrap gap-3">
        <BrutalButton variant="secondary" onClick={() => void lab.computeValidSignature()}>
          Compute Valid SHA-512
        </BrutalButton>
        <BrutalButton variant="signal" disabled={lab.isSending} onClick={lab.sendWebhook}>
          {lab.isSending ? "Sending Webhook..." : "POST /api/v1/webhooks/payment"}
        </BrutalButton>
      </div>

      {lab.lastError && (
        <div className="mt-4">
          <StateNotice kind="error" title="Webhook rejected" error={lab.lastError} showTechnicalDetails />
        </div>
      )}

      {lab.lastResult && (
        <div className="mt-4 border-2 border-ink bg-surface p-4 font-mono text-xs">
          <div className="flex flex-wrap items-center gap-3">
            <StatusBadge status={lab.lastResult.outcome} />
            <span>idempotent_replay: {String(lab.lastResult.idempotent_replay)}</span>
          </div>
          {lab.lastResult.order && (
            <p className="mt-2">
              Order {lab.lastResult.order.invoice_number} status: {lab.lastResult.order.status} (tickets: {lab.lastResult.order.tickets?.length ?? 0})
            </p>
          )}
        </div>
      )}
    </BrutalCard>
  );
}
