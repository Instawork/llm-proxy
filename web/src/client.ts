import { QueryClient } from "@tanstack/react-query";

// Relative paths keep API calls on the same origin as the UI — works for Vite
// dev (proxy) and Go-served dist (including localhost:9002). Using hostname
// alone drops non-default ports and breaks share pages served from :9002.
export const baseUrl = "";
// OAuth redirects must hit the Go server directly (browser navigation, not XHR).
export const authBaseUrl = import.meta.env.DEV ? "http://localhost:9002" : location.origin;

export class APIClientError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function parseError(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { error?: string };
    return body.error ?? response.statusText;
  } catch {
    return response.statusText;
  }
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    ...init,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });

  if (response.status === 401) {
    const redirect = encodeURIComponent(window.location.pathname + window.location.search);
    window.location.href = `/admin/login?redirect=${redirect}`;
    throw new APIClientError(401, "Unauthorized");
  }

  if (!response.ok) {
    throw new APIClientError(response.status, await parseError(response));
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return (await response.json()) as T;
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      refetchOnWindowFocus: false,
    },
  },
});
