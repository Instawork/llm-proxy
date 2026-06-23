import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "../client";
import { canManageByoBans, roleAtLeast } from "../lib/rbac";
import type {
  AdminUser,
  AdminUserRecord,
  APIKey,
  ConfigSummary,
  CostResponse,
  CreateAdminUserRequest,
  CreateAPIKeyRequest,
  CreateKeyRequestBody,
  HealthResponse,
  KeyRequestRecord,
  KeyStatsResponse,
  CircuitActivityResponse,
  ModelStatusResponse,
  PIIResponse,
  ProvisioningStatus,
  Provider,
  RateLimitsResponse,
  ShareCreateResponse,
  ShareInfo,
  UpdateAdminUserRoleRequest,
  UpdateAPIKeyRequest,
  ReviewKeyRequestBody,
  BYOBanRecord,
  BYOKeyRecord,
  CreateBYOBanRequest,
  UsageResponse,
} from "../types";
import { usePollInterval } from "./use-poll-interval";

export const queryKeys = {
  me: ["admin", "me"] as const,
  keys: (provider?: string) => ["admin", "keys", provider ?? "all"] as const,
  key: (key: string) => ["admin", "keys", "detail", key] as const,
  keyStats: (key: string) => ["admin", "keys", "stats", key] as const,
  config: ["admin", "config"] as const,
  health: ["admin", "health"] as const,
  circuitActivity: ["admin", "circuit-activity"] as const,
  rateLimits: ["admin", "rate-limits"] as const,
  cost: ["admin", "cost"] as const,
  usage: ["admin", "usage"] as const,
  pii: ["admin", "pii"] as const,
  modelStatus: ["admin", "model-status"] as const,
  provisioning: ["admin", "provisioning"] as const,
  users: ["admin", "users"] as const,
  keyRequests: (status?: string) => ["admin", "key-requests", status ?? "all"] as const,
  myKeyRequests: ["admin", "key-requests", "mine"] as const,
  byoBans: (provider?: string) => ["admin", "byo-bans", provider ?? "all"] as const,
  byoKeys: (provider?: string) => ["admin", "byo-keys", provider ?? "all"] as const,
};

export function useMe() {
  return useQuery({
    queryKey: queryKeys.me,
    queryFn: () => apiFetch<AdminUser>("/admin/api/me"),
  });
}

export function useKeys(provider?: Provider) {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return useQuery({
    queryKey: queryKeys.keys(provider),
    queryFn: () => apiFetch<APIKey[]>(`/admin/api/keys${query}`),
    enabled: roleAtLeast(role, "viewer"),
  });
}

export function useKey(key: string | undefined) {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  return useQuery({
    queryKey: queryKeys.key(key ?? ""),
    queryFn: () => apiFetch<APIKey>(`/admin/api/keys/${encodeURIComponent(key!)}`),
    enabled: Boolean(key) && roleAtLeast(role, "viewer"),
  });
}

export function useKeyStats(key: string | undefined) {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.keyStats(key ?? ""),
    queryFn: () => apiFetch<KeyStatsResponse>(`/admin/api/keys/${encodeURIComponent(key!)}/stats`),
    enabled: Boolean(key) && roleAtLeast(role, "viewer"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useConfig() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  return useQuery({
    queryKey: queryKeys.config,
    queryFn: () => apiFetch<ConfigSummary>("/admin/api/config"),
    enabled: roleAtLeast(role, "editor"),
  });
}

export function useHealth() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.health,
    // history=1 opts into the Redis-backed circuit daily_history; bare
    // /health (infra liveness probe) stays Redis-free.
    queryFn: () => apiFetch<HealthResponse>("/admin/api/health?history=1"),
    enabled: roleAtLeast(role, "editor"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useCircuitActivity() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.circuitActivity,
    queryFn: () => apiFetch<CircuitActivityResponse>("/admin/api/circuit-activity"),
    enabled: roleAtLeast(role, "editor"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useProvisioning() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  return useQuery({
    queryKey: queryKeys.provisioning,
    queryFn: () => apiFetch<ProvisioningStatus>("/admin/api/provisioning"),
    enabled: roleAtLeast(role, "viewer"),
  });
}

export function useRateLimits() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.rateLimits,
    queryFn: () => apiFetch<RateLimitsResponse>("/admin/api/rate-limits"),
    enabled: roleAtLeast(role, "editor"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useCost() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.cost,
    queryFn: () => apiFetch<CostResponse>("/admin/api/cost"),
    enabled: roleAtLeast(role, "editor"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useUsage() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.usage,
    queryFn: () => apiFetch<UsageResponse>("/admin/api/usage"),
    enabled: roleAtLeast(role, "editor"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function usePII() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.pii,
    queryFn: () => apiFetch<PIIResponse>("/admin/api/pii"),
    enabled: roleAtLeast(role, "editor"),
    refetchInterval,
    refetchIntervalInBackground: true,
  });
}

export function useModelStatus() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const refetchInterval = usePollInterval();
  return useQuery({
    queryKey: queryKeys.modelStatus,
    queryFn: () => apiFetch<ModelStatusResponse>("/admin/api/model-status"),
    enabled: roleAtLeast(role, "editor"),
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

export function useUsers() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  return useQuery({
    queryKey: queryKeys.users,
    queryFn: () => apiFetch<AdminUserRecord[]>("/admin/api/users"),
    enabled: roleAtLeast(role, "admin"),
  });
}

export function useCreateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateAdminUserRequest) =>
      apiFetch<AdminUserRecord>("/admin/api/users", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.users }),
  });
}

export function useUpdateUserRole() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ email, role }: { email: string; role: UpdateAdminUserRoleRequest["role"] }) =>
      apiFetch<AdminUserRecord>(`/admin/api/users/${encodeURIComponent(email)}`, {
        method: "PATCH",
        body: JSON.stringify({ role }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: queryKeys.users });
      qc.invalidateQueries({ queryKey: queryKeys.me });
    },
  });
}

export function useDeleteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (email: string) =>
      apiFetch<void>(`/admin/api/users/${encodeURIComponent(email)}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.users }),
  });
}

export function useKeyRequests(status?: string) {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  const qs = status ? `?status=${encodeURIComponent(status)}` : "";
  return useQuery({
    queryKey: queryKeys.keyRequests(status),
    queryFn: () => apiFetch<KeyRequestRecord[]>(`/admin/api/key-requests${qs}`),
    enabled: roleAtLeast(role, "admin"),
  });
}

export function useMyKeyRequests() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  return useQuery({
    queryKey: queryKeys.myKeyRequests,
    queryFn: () => apiFetch<KeyRequestRecord[]>("/admin/api/key-requests/mine"),
    enabled: role === "viewer" || role === "editor",
  });
}

export function useCreateKeyRequest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateKeyRequestBody) =>
      apiFetch<KeyRequestRecord>("/admin/api/key-requests", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: queryKeys.myKeyRequests });
      qc.invalidateQueries({ queryKey: queryKeys.keyRequests() });
    },
  });
}

export function useReviewKeyRequest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: ReviewKeyRequestBody & { id: string }) =>
      apiFetch<KeyRequestRecord>(`/admin/api/key-requests/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: queryKeys.keyRequests() });
      qc.invalidateQueries({ queryKey: queryKeys.myKeyRequests });
      qc.invalidateQueries({ queryKey: queryKeys.keys() });
    },
  });
}

export function useBYOKeys(provider?: Provider) {
  const { data: me } = useMe();
  const qs = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return useQuery({
    queryKey: queryKeys.byoKeys(provider),
    queryFn: () => apiFetch<BYOKeyRecord[]>(`/admin/api/byo-keys${qs}`),
    enabled: canManageByoBans(me?.role),
  });
}

export function useBYOBans(provider?: Provider) {
  const { data: me } = useMe();
  const qs = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return useQuery({
    queryKey: queryKeys.byoBans(provider),
    queryFn: () => apiFetch<BYOBanRecord[]>(`/admin/api/byo-bans${qs}`),
    enabled: canManageByoBans(me?.role),
  });
}

export function useBanBYOKey() {
  const qc = useQueryClient();
  const { data: me } = useMe();
  return useMutation({
    mutationFn: (body: CreateBYOBanRequest) => {
      if (!canManageByoBans(me?.role)) {
        return Promise.reject(new Error("Admin role required"));
      }
      return apiFetch<BYOBanRecord>("/admin/api/byo-bans", {
        method: "POST",
        body: JSON.stringify(body),
      });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "byo-bans"] });
      qc.invalidateQueries({ queryKey: ["admin", "byo-keys"] });
    },
  });
}

export function useUnbanBYOKey() {
  const qc = useQueryClient();
  const { data: me } = useMe();
  return useMutation({
    mutationFn: ({ provider, hash }: { provider: Provider; hash: string }) => {
      if (!canManageByoBans(me?.role)) {
        return Promise.reject(new Error("Admin role required"));
      }
      return apiFetch<void>(`/admin/api/byo-bans/${encodeURIComponent(provider)}/${encodeURIComponent(hash)}`, {
        method: "DELETE",
      });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "byo-bans"] });
      qc.invalidateQueries({ queryKey: ["admin", "byo-keys"] });
    },
  });
}
