import { Lightning } from "@phosphor-icons/react";
import type { FlashSaleReport } from "@/lib/api/generated/types.gen";
import type { ParsedApiError } from "@/lib/api/errors";
import { BrutalButton } from "@/components/ui/BrutalButton";
import { BrutalCard } from "@/components/ui/BrutalCard";
import { StateNotice } from "@/components/ui/StateNotice";
import { StatusBadge } from "@/components/ui/StatusBadge";

interface FlashSaleLabPanelProps {
  requests: number;
  onRequestsChange: (v: number) => void;
  quota: number;
  onQuotaChange: (v: number) => void;
  report: FlashSaleReport | null;
  submitError: ParsedApiError | null;
  isRunning: boolean;
  onRun: () => void;
}

export function FlashSaleLabPanel({
  requests,
  onRequestsChange,
  quota,
  onQuotaChange,
  report,
  submitError,
  isRunning,
  onRun,
}: FlashSaleLabPanelProps) {
  return (
    <BrutalCard>
      <div className="border-b-2 border-ink pb-3">
        <h2 className="flex items-center gap-2 text-2xl font-bold">
          <Lightning size={22} weight="bold" />
          200-Concurrent Flash Sale Benchmark
        </h2>
        <p className="mt-1 text-sm text-ink-muted">
          Calls POST /api/v1/demo/flash-sale, spinning parallel buyer goroutines simultaneously against PostgreSQL row locks.
        </p>
      </div>

      <div className="mt-4 grid gap-4 sm:grid-cols-3">
        <label className="block text-xs font-bold uppercase">
          Concurrent Buyers (1-500)
          <input
            type="number"
            min={1}
            max={500}
            value={requests}
            onChange={(e) => onRequestsChange(Number(e.target.value))}
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 font-mono text-sm"
          />
        </label>
        <label className="block text-xs font-bold uppercase">
          Initial Tier Quota (1-500)
          <input
            type="number"
            min={1}
            max={500}
            value={quota}
            onChange={(e) => onQuotaChange(Number(e.target.value))}
            className="mt-1 min-h-[44px] w-full border-2 border-ink bg-canvas px-3 font-mono text-sm"
          />
        </label>
        <div className="flex items-end">
          <BrutalButton variant="signal" className="w-full" disabled={isRunning} onClick={onRun}>
            <Lightning size={16} weight="bold" />
            {isRunning ? "Running Burst..." : "Launch Concurrent Burst"}
          </BrutalButton>
        </div>
      </div>

      {submitError && (
        <div className="mt-4">
          <StateNotice kind="error" title="Benchmark failed" error={submitError} showTechnicalDetails />
        </div>
      )}

      {report && (
        <div className="mt-5 border-2 border-ink bg-canvas p-4">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b-2 border-ink pb-3">
            <span className="font-mono text-sm font-bold">Measured Server Result ({report.duration_ms} ms)</span>
            <StatusBadge status={report.zero_oversell ? "PAID" : "EXPIRED"} />
          </div>

          <div className="mt-3 grid gap-3 font-mono sm:grid-cols-4">
            <div className="border-2 border-ink bg-surface p-3">
              <p className="text-xs text-ink-muted">Requests</p>
              <p className="text-xl font-bold">{report.concurrent_requests}</p>
            </div>
            <div className="border-2 border-ink bg-status-paid-bg p-3">
              <p className="text-xs text-status-paid-ink">Succeeded (201)</p>
              <p className="text-xl font-bold text-status-paid-ink">{report.succeeded_bookings}</p>
            </div>
            <div className="border-2 border-ink bg-status-wait-bg p-3">
              <p className="text-xs text-status-wait-ink">Rejected (409)</p>
              <p className="text-xl font-bold text-status-wait-ink">{report.rejected_quota_empty}</p>
            </div>
            <div className="border-2 border-ink bg-signal p-3">
              <p className="text-xs">Remaining After</p>
              <p className="text-xl font-bold">{report.remaining_quota_after}</p>
            </div>
          </div>

          <p className="mt-3 font-mono text-xs text-ink-muted">
            Event ID: {report.event_id} | Zero Oversell: {String(report.zero_oversell)} | Other Failures: {report.rejected_other}
          </p>
        </div>
      )}
    </BrutalCard>
  );
}
