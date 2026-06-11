import { useMutation, useQuery, useQueryClient } from "react-query";

import { apiFetch } from "../client";
import type {
  AdminUser,
  APIKey,
  ConfigSummary,
  CreateAPIKeyRequest,
  HealthResponse,
  Provider,
  RateLimitsResponse,
  UpdateAPIKeyRequest,
} from "../types";

export const queryKeys = {
  me: ["admin", "me"] as const,
  keys: (provider?: string) => ["admin", "keys", provider ?? "all"] as const,
  key: (key: string) => ["admin", "keys", "detail", key] as const,
  config: ["admin", "config"] as const,
  health: ["admin", "health"] as const,
  rateLimits: ["admin", "rate-limits"] as const,
};

export function useMe() {
  return useQuery(queryKeys.me, () => apiFetch<AdminUser>("/admin/api/me"));
}

export function useKeys(provider?: Provider) {
  const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return useQuery(queryKeys.keys(provider), () => apiFetch<APIKey[]>(`/admin/api/keys${query}`));
}

export function useKey(key: string | undefined) {
  return useQuery(
    queryKeys.key(key ?? ""),
    () => apiFetch<APIKey>(`/admin/api/keys/${encodeURIComponent(key!)}`),
    { enabled: Boolean(key) },
  );
}

export function useConfig() {
  return useQuery(queryKeys.config, () => apiFetch<ConfigSummary>("/admin/api/config"));
}

export function useHealth() {
  return useQuery(queryKeys.health, () => apiFetch<HealthResponse>("/admin/api/health"));
}

export function useRateLimits() {
  return useQuery(queryKeys.rateLimits, () => apiFetch<RateLimitsResponse>("/admin/api/rate-limits"));
}

function invalidateKeys(queryClient: ReturnType<typeof useQueryClient>) {
  queryClient.invalidateQueries(["admin", "keys"]);
}

export function useCreateKey() {
  const queryClient = useQueryClient();
  return useMutation(
    (body: CreateAPIKeyRequest) =>
      apiFetch<APIKey>("/admin/api/keys", { method: "POST", body: JSON.stringify(body) }),
    {
      onSuccess: () => invalidateKeys(queryClient),
    },
  );
}

export function useUpdateKey() {
  const queryClient = useQueryClient();
  return useMutation(
    ({ key, body }: { key: string; body: UpdateAPIKeyRequest }) =>
      apiFetch<APIKey>(`/admin/api/keys/${encodeURIComponent(key)}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    {
      onSuccess: (_data, variables) => {
        invalidateKeys(queryClient);
        queryClient.invalidateQueries(queryKeys.key(variables.key));
      },
    },
  );
}

export function useDeleteKey() {
  const queryClient = useQueryClient();
  return useMutation(
    (key: string) =>
      apiFetch<void>(`/admin/api/keys/${encodeURIComponent(key)}`, { method: "DELETE" }),
    {
      onSuccess: () => invalidateKeys(queryClient),
    },
  );
}

export function useLogout() {
  return useMutation(() => apiFetch<void>("/admin/auth/logout", { method: "POST" }));
}
