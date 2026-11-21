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

    headers.delete("X-User-ID");
    headers.delete("X-User-Role");

    if (state.accessToken) {
      headers.set("Authorization", `Bearer ${state.accessToken}`);
    } else {
      headers.delete("Authorization");
    }

    return new Request(request, { headers });
  });

  client.interceptors.error.use((error, response, request) => {
    if (response?.status === 401) {
      const reqUrl = request?.url ?? "";
      if (!reqUrl.includes("/api/v1/auth/login") && !reqUrl.includes("/api/v1/webhooks/payment")) {
        useAuthStore.getState().clearSession();
      }
    }

    if (response && typeof error === "string") {
      return {
        status: response.status,
        error_code:
          response.status === 429
            ? "RATE_LIMITED"
            : response.status === 401
              ? "UNAUTHORIZED"
              : "INTERNAL_ERROR",
        title: response.statusText || "HTTP Error",
        detail:
          response.status === 429
            ? "Too many requests at edge proxy. Please wait a moment and try again."
            : `HTTP ${response.status} from gateway.`,
      };
    }

    return error;
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
