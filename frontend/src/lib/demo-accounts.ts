import type { DemoRole } from "@/store/auth-store";

export interface SeededDemoAccount {
  id: string;
  email: string;
  name: string;
  password: string;
  role: DemoRole;
}

export const SEEDED_DEMO_ACCOUNTS: ReadonlyArray<SeededDemoAccount> = [
  {
    id: "11111111-1111-1111-1111-111111111111",
    email: "organizer@event.local",
    name: "Demo Organizer",
    password: "password123",
    role: "ORGANIZER",
  },
  {
    id: "22222222-2222-2222-2222-222222222221",
    email: "buyer1@event.local",
    name: "Demo Buyer 1",
    password: "password123",
    role: "BUYER",
  },
  {
    id: "22222222-2222-2222-2222-222222222222",
    email: "buyer2@event.local",
    name: "Demo Buyer 2",
    password: "password123",
    role: "BUYER",
  },
];
