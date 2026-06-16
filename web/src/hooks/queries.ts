import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "../client";
import type {
  AdminUser,
  APIKey,
  ConfigSummary,
  CostResponse,
  CreateAPIKeyRequest,
  HealthResponse,
  CircuitActivityResponse,
  PIIResponse,
  Provider,
  RateLimitsResponse,
  ShareCreateResponse,
  ShareInfo,
  UpdateAPIKeyRequest,
  UsageResponse,
} from "../types";
import { usePollInterval } from "./use-poll-interval";

export const queryKeys = {
  me: ["admin", "me"] as const,
  keys: (provider?: string) => ["admin", "keys", provider ?? "all"] as const,
  key: (key: string) => ["admin", "keys", "detail", key] as const,
  config: ["admin", "config"] as const,
  health: ["admin", "health"] as const,
  circuitActivity: ["admin", "circuit-activity"] as const,
  rateLimits: ["admin", "rate-limits"] as const,
  cost: ["admin", "cost"] as const,
  usage: ["admin", "usage"] as const,
  pii: ["admin", "pii"] as const,
};

export function useMe() {
  return useQuery({
    queryKey: queryKeys.me,
    queryFn: () => apiFetch<AdminUser>("/admin/api/me"),
  });
}

export function useKeys(provider?: Provider) {
  const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return useQuery({
    queryKey: queryKeys.keys(provider),
    queryFn: () => apiFetch<APIKey[]>(`/admin/api/keys${query}`),
  });
}

export function useKey(key: string | undefined) {
  return useQuery({
    queryKey: queryKeys.key(key ?? ""),
    queryFn: () => apiFetch<APIKey>(`/admin/api/keys/${encodeURIComponent(key!)}`),
    enabled: Boolean(key),
  });
}

export function useConfig() {
  return useQuery({
    queryKey: queryKeys.config,
    queryFn: () => apiFetch<ConfigSummary>("/admin/api/config"),
  });
}

export function useHealth() {
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.health,
    // history=1 opts into the Redis-backed circuit daily_history; bare
    // /health (infra liveness probe) stays Redis-free.
    queryFn: () => apiFetch<HealthResponse>("/admin/api/health?history=1"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useCircuitActivity() {
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.circuitActivity,
    queryFn: () => apiFetch<CircuitActivityResponse>("/admin/api/circuit-activity"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useRateLimits() {
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.rateLimits,
    queryFn: () => apiFetch<RateLimitsResponse>("/admin/api/rate-limits"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useCost() {
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.cost,
    queryFn: () => apiFetch<CostResponse>("/admin/api/cost"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useUsage() {
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.usage,
    queryFn: () => apiFetch<UsageResponse>("/admin/api/usage"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function usePII() {
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.pii,
    queryFn: () => apiFetch<PIIResponse>("/admin/api/pii"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

function invalidateKeys(queryClient: ReturnType<typeof useQueryClient>) {
  queryClient.invalidateQueries({ queryKey: ["admin", "keys"] });
}

export function useCreateKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateAPIKeyRequest) =>
      apiFetch<APIKey>("/admin/api/keys", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => invalidateKeys(queryClient),
  });
}

export function useUpdateKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ key, body }: { key: string; body: UpdateAPIKeyRequest }) =>
      apiFetch<APIKey>(`/admin/api/keys/${encodeURIComponent(key)}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      invalidateKeys(queryClient);
      queryClient.invalidateQueries({ queryKey: queryKeys.key(variables.key) });
    },
  });
}

export function useDeleteKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (key: string) =>
      apiFetch<void>(`/admin/api/keys/${encodeURIComponent(key)}`, { method: "DELETE" }),
    onSuccess: () => invalidateKeys(queryClient),
  });
}

export function useLogout() {
  return useMutation({
    mutationFn: () => apiFetch<void>("/admin/auth/logout", { method: "POST" }),
  });
}

export function useShare(id: string | undefined) {
  return useQuery({
    queryKey: ["admin", "share", id ?? ""] as const,
    queryFn: () => apiFetch<ShareInfo>(`/admin/api/share/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
    retry: false,
  });
}

export function useCreateShare() {
  return useMutation({
    mutationFn: (key: string) =>
      apiFetch<ShareCreateResponse>("/admin/api/share", {
        method: "POST",
        body: JSON.stringify({ key }),
      }),
  });
}

export function useDeleteShare() {
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<void>(`/admin/api/share/${encodeURIComponent(id)}`, { method: "DELETE" }),
  });
}
