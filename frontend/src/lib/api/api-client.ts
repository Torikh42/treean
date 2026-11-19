import { client } from "@/lib/api/generated/client.gen";
import { useAuthStore } from "@/store/auth-store";

let configured = false;

export function getApiBaseUrl(): string {
  return import.meta.env.VITE_API_URL?.trim() || "";
}

export function configureApiClient(): void {
  if (configured) return;
  configured = true;

  client.setConfig({
    baseUrl: getApiBaseUrl(),
  });

  client.interceptors.request.use((request) => {
    const state = useAuthStore.getState();
    const headers = new Headers(request.headers);

    if (state.authMode === "jwt" && state.accessToken) {
      headers.set("Authorization", `Bearer ${state.accessToken}`);
      headers.delete("X-User-ID");
      headers.delete("X-User-Role");
    } else if (state.authMode === "demo" && state.demoUserId.trim()) {
      headers.set("X-User-ID", state.demoUserId.trim());
      headers.set("X-User-Role", state.demoRole);
      headers.delete("Authorization");
    }

    return new Request(request, { headers });
  });
}

export async function fetchApiHealth(): Promise<{ status: string }> {
  const base = getApiBaseUrl();
  const res = await fetch(`${base}/health`, { method: "GET" });
  const body = (await res.json()) as { status?: string };
  if (!res.ok) {
    throw new Error(body.status || `HTTP ${res.status}`);
  }
  return { status: body.status || "ok" };
}
