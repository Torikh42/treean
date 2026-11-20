import { z } from "zod";
import type { ParsedApiError } from "@/lib/api/errors";
import type {
  CreateEventBody,
  TicketType,
  UpdateEventBody,
} from "@/lib/api/generated/types.gen";

export const MIN_EVENT_TIERS = 2;
export const MAX_EVENT_TIERS = 10;

export interface TierDraft {
  id?: string;
  name: string;
  price_idr: number;
  total_quota: number;
  sold_or_held?: number;
}

export interface EventFormInput {
  title: string;
  venue: string;
  description: string;
  eventDateLocal: string;
  tiers: ReadonlyArray<TierDraft>;
}

export type BuildEventBodyResult<T> =
  | { ok: true; body: T }
  | { ok: false; error: ParsedApiError };

export const DEFAULT_TIER_DRAFTS: ReadonlyArray<TierDraft> = [
  { name: "Reguler", price_idr: 150000, total_quota: 80, sold_or_held: 0 },
  { name: "VIP", price_idr: 350000, total_quota: 20, sold_or_held: 0 },
];

const baseEventSchema = z.object({
  title: z.string().trim().min(3, "Event title must be at least 3 characters").max(150),
  venue: z.string().trim().min(2, "Venue must be at least 2 characters").max(150),
  description: z.string().trim().max(1000, "Description must be 1000 characters or fewer"),
  event_date: z.string().min(1, "Event date/time is required"),
  ticket_types: z
    .array(
      z.object({
        id: z.string().optional(),
        name: z.string().trim().min(1, "Tier name is required").max(60),
        price_idr: z.number().int("Price must be a whole IDR amount").min(0, "Price must be >= 0"),
        total_quota: z
          .number()
          .int("Quota must be a whole number")
          .min(1, "Quota must be >= 1")
          .max(100000, "Quota must be <= 100000"),
      }),
    )
    .min(MIN_EVENT_TIERS, "At least 2 ticket tiers are required")
    .max(MAX_EVENT_TIERS, "Maximum 10 ticket tiers allowed"),
});

export function toLocalDatetimeValue(iso?: string): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  if (iso) {
    const parsed = new Date(iso);
    if (!Number.isNaN(parsed.getTime())) {
      return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())}T${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
    }
  }
  const future = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000);
  return `${future.getFullYear()}-${pad(future.getMonth() + 1)}-${pad(future.getDate())}T19:00`;
}

export function computeSoldOrHeld(
  tier: Pick<TicketType, "total_quota" | "remaining_quota">,
): number {
  return Math.max(0, tier.total_quota - tier.remaining_quota);
}

export function mapEventToTierDrafts(
  tiers: ReadonlyArray<TicketType> | null | undefined,
): TierDraft[] {
  return (tiers ?? []).map((tier) => ({
    id: tier.id,
    name: tier.name,
    price_idr: tier.price_idr,
    total_quota: tier.total_quota,
    sold_or_held: computeSoldOrHeld(tier),
  }));
}

function validateEventInput(input: EventFormInput): BuildEventBodyResult<z.infer<typeof baseEventSchema>> {
  const parsedDate = new Date(input.eventDateLocal);
  if (Number.isNaN(parsedDate.getTime())) {
    return {
      ok: false,
      error: {
        status: 422,
        code: "VALIDATION_ERROR",
        detail: "Invalid event date/time format",
        userTitle: "Invalid event date",
        userMessage: "The selected event date and time could not be read.",
        actionHint: "Pick a valid date and time.",
      },
    };
  }

  const parsed = baseEventSchema.safeParse({
    title: input.title,
    venue: input.venue,
    description: input.description,
    event_date: parsedDate.toISOString(),
    ticket_types: input.tiers.map((tier) => ({
      id: tier.id,
      name: tier.name,
      price_idr: tier.price_idr,
      total_quota: tier.total_quota,
    })),
  });

  if (!parsed.success) {
    const issueMessage = parsed.error.issues[0]?.message ?? "Check event inputs";
    return {
      ok: false,
      error: {
        status: 422,
        code: "VALIDATION_ERROR",
        detail: issueMessage,
        userTitle: "Please check event details",
        userMessage: issueMessage,
        actionHint: "Ensure title, venue, valid date, and 2 to 10 valid tiers.",
      },
    };
  }

  const seenNames = new Set<string>();
  for (const tier of parsed.data.ticket_types) {
    const normalized = tier.name.toLowerCase();
    if (seenNames.has(normalized)) {
      const msg = `Duplicate ticket tier name "${tier.name}"`;
      return {
        ok: false,
        error: {
          status: 422,
          code: "VALIDATION_ERROR",
          detail: msg,
          userTitle: "Duplicate tier name",
          userMessage: `Each ticket tier needs a unique name ("${tier.name}" appears more than once).`,
          actionHint: "Rename the duplicate ticket tier before saving.",
        },
      };
    }
    seenNames.add(normalized);
  }

  return { ok: true, body: parsed.data };
}

export function buildCreateEventBody(
  input: EventFormInput,
): BuildEventBodyResult<CreateEventBody> {
  const validated = validateEventInput(input);
  if (!validated.ok) return validated;

  return {
    ok: true,
    body: {
      title: validated.body.title,
      venue: validated.body.venue,
      description: validated.body.description,
      event_date: validated.body.event_date,
      ticket_types: validated.body.ticket_types.map((tier) => ({
        name: tier.name,
        price_idr: tier.price_idr,
        total_quota: tier.total_quota,
      })),
    },
  };
}

export function buildUpdateEventBody(
  input: EventFormInput,
): BuildEventBodyResult<UpdateEventBody> {
  const validated = validateEventInput(input);
  if (!validated.ok) return validated;

  for (let i = 0; i < input.tiers.length; i += 1) {
    const draft = input.tiers[i];
    const cleanTier = validated.body.ticket_types[i];
    const soldOrHeld = draft?.sold_or_held ?? 0;
    if (cleanTier && soldOrHeld > 0 && cleanTier.total_quota < soldOrHeld) {
      const msg = `Tier "${cleanTier.name}" quota (${cleanTier.total_quota}) cannot be lower than ${soldOrHeld} sold/reserved ticket(s).`;
      return {
        ok: false,
        error: {
          status: 409,
          code: "QUOTA_CONFLICT",
          detail: msg,
          userTitle: "Quota cannot be lowered below sold tickets",
          userMessage: msg,
          actionHint: `Set total quota for "${cleanTier.name}" to at least ${soldOrHeld}.`,
        },
      };
    }
  }

  return {
    ok: true,
    body: {
      title: validated.body.title,
      venue: validated.body.venue,
      description: validated.body.description,
      event_date: validated.body.event_date,
      ticket_types: validated.body.ticket_types.map((tier) => ({
        ...(tier.id ? { id: tier.id } : {}),
        name: tier.name,
        price_idr: tier.price_idr,
        total_quota: tier.total_quota,
      })),
    },
  };
}
