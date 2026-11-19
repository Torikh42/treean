# DESIGN.md - Treean Web Console

## Product Identity
- **Product Name:** TREEAN // Flash Ticketing Console
- **Visual Direction:** Web-Brutalism / Neo-Brutalism grounded in archival concert manifest & printed box-office ticket stubs. High structural clarity without generic dark SaaS glows or glassmorphism.
- **Target Audience:** Event organizers configuring tiered releases, buyers booking limited-quota tickets under a 15-minute hold, and developers testing 200-concurrent flash sales and signed webhooks.

## Antislop Dials (R-37)
- **ENERGY:** 3 (Thick carbon borders, sharp 0-blur offset shadows, high-contrast Signal Yellow focus accents)
- **RHYTHM:** 3 (Asymmetric layout split: active checkout / flash-sale panels lead visually; supporting lists and history use compact tabular density)
- **MOTION:** 2 (Tactile `2px` button press displacement on hover/active; zero infinite pulses, floats, or decorative loops)

## Color System (<= 3 Core + 1 Accent, R-29)
- **Canvas Ground:** `#F6F3EB` (Warm archival paper cream)
- **Card / Surface:** `#FFFFFF` (Crisp ticket stock white)
- **Muted / Secondary Surface:** `#EDE8DC` (Pressed paper tint for table headers & metadata bands)
- **Primary Ink & Borders:** `#111111` (Solid `2px` / `3px` borders and `4px 4px 0 #111111` hard offset shadows on key interactive containers)
- **Primary Accent (Key Moments Only):** `#FFE500` (Signal Yellow - used strictly on primary CTAs, active route tabs, and interactive focus highlights with `#111111` ink; >= 16:1 WCAG AAA contrast)
- **Functional Status States (Semantic Only):**
  - `PAID` / `OK` / `Zero Oversell`: `#DCFCE7` fill, `#166534` text, `#166534` border
  - `WAITING_PAYMENT`: `#FEF3C7` fill, `#92400E` text, `#92400E` border
  - `CANCELLED` / `EXPIRED` / `UNHEALTHY`: `#FEE2E2` fill, `#991B1B` text, `#991B1B` border

## Typography (R-06)
- **Display & UI Sans:** `Space Grotesk Variable` (`@fontsource-variable/space-grotesk`) with system grotesk fallback - crisp geometric readability for quota counters, titles, and navigation.
- **Technical Monospace:** `IBM Plex Mono` (`@fontsource/ibm-plex-mono`) - reserved strictly for seat ticket codes (`TKT-...`), UUIDs, IDR tabular alignment, 15-minute `MM:SS` timers, and raw JSON webhook inspection.

## Structural & Antislop Guardrails
1. **Zero decorative slop:** No gradients, no `backdrop-filter` blur, no emoji in headings/buttons, no eyebrow pills above H1, no decorative status dots (`R-01`, `R-04`, `R-09`, `R-10`, `R-31`). Live status dots appear ONLY on `/health` when reflecting actual API ping state.
2. **Real Behavior Only (`R-24`, `R-26`):** Every navigation item routes cleanly via TanStack Router; every button triggers an actual OpenAPI-backed mutation or navigation.
3. **Explicit States (`R-27`):** Loading states name what is loading; empty states offer the exact next action (e.g. create an event or place an order); errors surface backend `error_code` + `detail` + recovery steps.
4. **Strict Component Separation:** All logic, forms, intervals, and API state reside in `src/hooks/` and TanStack Query; `src/components/` remain pure JSX presentational views.
5. **Accessibility (`R-03`, `R-34`):** Minimum `44px` interactive height, high-contrast `3px solid #111111` + `#FFE500` `:focus-visible` outline, mobile-first reflow down to `375px` with zero horizontal overflow.
