export type Provider = "openai" | "anthropic" | "gemini" | "bedrock";

export type PiiRedactSetting = boolean | null;

export type AdminRole = "admin" | "editor" | "viewer";

export interface EditorLimits {
  max_daily_cost_limit_cents: number;
}

export interface ViewerLimits {
  personal_monthly_cost_limit_cents: number;
}

export interface AdminUser {
  email: string;
  name?: string;
  picture?: string;
  role?: AdminRole;
  can_bypass_pii_off_non_bedrock_policy?: boolean;
  editor_limits?: EditorLimits;
  viewer_limits?: ViewerLimits;
}

export interface AdminUserRecord {
  email: string;
  name?: string;
  picture?: string;
  role: AdminRole;
  created_at: string;
  updated_at: string;
  last_login_at?: string;
}

export interface CreateAdminUserRequest {
  email: string;
  role: AdminRole;
}

export interface UpdateAdminUserRoleRequest {
  role: AdminRole;
}

export type KeyRequestStatus = "pending" | "approved" | "rejected";

export interface KeyRequestRecord {
  id: string;
  requester_email: string;
  provider: Provider;
  description: string;
  daily_cost_limit?: number;
  status: KeyRequestStatus;
  created_at: string;
  updated_at: string;
  reviewed_by?: string;
  reviewed_at?: string;
  created_key?: string;
  rejection_reason?: string;
}

export interface CreateKeyRequestBody {
  provider: Provider;
  description: string;
  daily_cost_limit?: number;
}

export interface ReviewKeyRequestBody {
  action: "approve" | "reject";
  rejection_reason?: string;
}

export interface APIKey {
  key: string;
  provider: Provider;
  description?: string;
  daily_cost_limit: number;
  monthly_cost_limit?: number;
  owner_email?: string;
  enabled: boolean;
  redact_pii?: PiiRedactSetting;
  rate_limit_rpm?: number;
  rate_limit_tpm?: number;
  rate_limit_rpd?: number;
  rate_limit_tpd?: number;
  tags?: Record<string, string>;
  created_at: string;
  updated_at: string;
  expires_at?: string | null;
  provisioned?: boolean;
}

export interface ProvisioningProviderStatus {
  auto_provision: boolean;
  pool_available?: number;
  default_tier?: string;
  tiers?: string[];
}

export type AnthropicTier = "metered" | "elevated" | "unrestricted";

export interface ProvisioningStatus {
  enabled: boolean;
  providers?: Partial<Record<Provider, ProvisioningProviderStatus>>;
}

export interface CreateAPIKeyRequest {
  provider: Provider;
  actual_key?: string;
  auto_provision?: boolean;
  personal?: boolean;
  description?: string;
  daily_cost_limit?: number;
  enabled?: boolean;
  redact_pii?: PiiRedactSetting;
  rate_limit_rpm?: number;
  rate_limit_tpm?: number;
  rate_limit_rpd?: number;
  rate_limit_tpd?: number;
  tags?: Record<string, string>;
}

export interface UpdateAPIKeyRequest {
  enabled?: boolean;
  description?: string;
  daily_cost_limit?: number;
  redact_pii?: PiiRedactSetting;
  rate_limit_rpm?: number;
  rate_limit_tpm?: number;
  rate_limit_rpd?: number;
  rate_limit_tpd?: number;
  tags?: Record<string, string>;
}

export interface FeatureToggle {
  enabled: boolean;
  backend?: string;
  table_name?: string;
  region?: string;
  mode?: string;
  analyzer_url?: string;
  fail_mode?: string;
}

export interface ConfigSummary {
  environment?: string;
  features: {
    cost_tracking?: FeatureToggle;
    api_key_management?: FeatureToggle;
    rate_limiting?: FeatureToggle;
    circuit_breaker?: FeatureToggle;
    pii_redact?: FeatureToggle;
    admin_dashboard?: FeatureToggle;
  };
}

export interface DailyHistoryRow {
  day: string;
  [key: string]:
  | string
  | number
  | boolean
  | undefined
  | Record<string, unknown>;
}

export interface StatsWithDailyHistory {
  daily_history?: DailyHistoryRow[];
  daily_history_available?: boolean;
}

export interface CircuitBreakerProviderHealth {
  state?: string;
  failures?: number;
  cooldown_until?: number;
  error?: string;
  rollup?: {
    enabled: boolean;
    open: boolean;
    count: number;
    threshold: number;
    window_seconds: number;
    open_keys?: string[];
  };
}

export interface HealthResponse {
  status: string;
  timestamp: number;
  providers?: Record<string, unknown>;
  features?: {
    cost_tracking?: boolean;
    circuit_breaker?: boolean;
  };
  circuit_breaker?: {
    enabled: boolean;
    mode: string;
    backend: string;
    redis_fallback?: boolean;
    providers?: Record<string, CircuitBreakerProviderHealth>;
    degraded_signal?: string;
    total_failures?: number;
    daily_history?: DailyHistoryRow[];
    daily_history_available?: boolean;
  };
}

export interface CircuitActivityEvent {
  time: number;
  provider: string;
  key?: string;
  kind: string;
  new_state?: string;
  status_code?: number;
  failure_kind?: string;
  upstream_error?: string;
  reason?: string;
}

export interface CircuitActivityResponse {
  available?: boolean;
  backend?: string;
  day?: string;
  started_at?: number;
  checks_total?: number;
  blocked_open?: number;
  probes_started?: number;
  probes_succeeded?: number;
  probes_failed?: number;
  circuits_opened?: number;
  by_provider?: Record<string, number>;
  by_key?: Record<string, number>;
  recent_events?: CircuitActivityEvent[];
  daily_history?: DailyHistoryRow[];
  daily_history_available?: boolean;
}

export interface RateLimitConfig {
  RequestsPerMinute?: number;
  TokensPerMinute?: number;
  RequestsPerDay?: number;
  TokensPerDay?: number;
}

export interface RateLimitCounter {
  requests?: number;
  tokens?: number;
}

export interface RateLimitWindow {
  window_start?: string;
  counters?: Record<string, RateLimitCounter>;
}

export interface RateLimitOverrides {
  PerKey?: Record<string, RateLimitConfig> | null;
  PerUser?: Record<string, RateLimitConfig> | null;
  PerModel?: Record<string, RateLimitConfig> | null;
}

export interface RateLimitSnapshot {
  minute?: RateLimitWindow;
  day?: RateLimitWindow;
}

export interface RateLimitsResponse {
  enabled: boolean;
  backend?: string;
  limits?: RateLimitConfig;
  overrides?: RateLimitOverrides;
  snapshot?: RateLimitSnapshot;
}

export interface CostTransport {
  type: string;
  path?: string;
  table_name?: string;
  region?: string;
  host?: string;
  port?: string;
  namespace?: string;
}

export interface CostKeySpend {
  key_id?: string;
  spend_usd: number;
  input_spend_usd?: number;
  output_spend_usd?: number;
  requests: number;
  input_tokens: number;
  output_tokens: number;
}

export interface CostProviderSpend {
  name: string;
  spend_usd: number;
  input_spend_usd?: number;
  output_spend_usd?: number;
  requests: number;
  input_tokens: number;
  output_tokens: number;
}

export interface CostRecentEvent {
  time: number;
  provider: string;
  key_id?: string;
  user_id?: string;
  spend_usd: number;
  input_spend_usd?: number;
  output_spend_usd?: number;
  input_tokens: number;
  output_tokens: number;
  model?: string;
}

export interface CostStats extends StatsWithDailyHistory {
  available: boolean;
  day?: string;
  started_at?: number;
  spend_today_usd?: number;
  input_spend_today_usd?: number;
  output_spend_today_usd?: number;
  requests_today?: number;
  input_tokens_today?: number;
  output_tokens_today?: number;
  by_key?: CostKeySpend[];
  by_provider?: CostProviderSpend[];
  recent?: CostRecentEvent[];
}

export interface CostResponse {
  enabled: boolean;
  async?: boolean;
  workers?: number;
  queue_size?: number;
  flush_interval?: number;
  transport_count?: number;
  transports?: CostTransport[];
  stats?: CostStats;
}

export interface PIINameCount {
  name: string;
  count: number;
}

export interface PIIRecentEvent {
  time: number;
  provider: string;
  key_id?: string;
  entity_counts: Record<string, number>;
  entity_total: number;
  body_bytes: number;
  duration_ms: number;
  outcome: "ok" | "fail_open" | "fail_closed" | "oversize";
}

export interface PIIStats extends StatsWithDailyHistory {
  available: boolean;
  started_at?: number;
  requests_scanned?: number;
  requests_with_pii?: number;
  entities_total?: number;
  detection_rate?: number;
  fail_open?: number;
  fail_closed?: number;
  oversize?: number;
  by_entity?: PIINameCount[];
  by_provider?: PIINameCount[];
  top_keys?: PIINameCount[];
  recent?: PIIRecentEvent[];
  recent_backend?: string;
}

export interface PIIResponse {
  enabled: boolean;
  allow_per_key_override: boolean;
  fail_mode: string;
  stats: PIIStats;
}

export interface ModelStatusNameCount {
  name: string;
  count: number;
}

export interface ModelStatusRegistryEntry {
  provider: string;
  model: string;
  retired_date?: string;
  replacement?: string;
  aliases?: string[];
}

export interface ModelStatusRegistry {
  retired: ModelStatusRegistryEntry[];
  deprecated: ModelStatusRegistryEntry[];
}

export interface ModelStatusStats extends StatsWithDailyHistory {
  available: boolean;
  backend?: string;
  day?: string;
  started_at?: number;
  retired_total?: number;
  deprecated_total?: number;
  unknown_total?: number;
  by_retired?: ModelStatusNameCount[];
  by_deprecated?: ModelStatusNameCount[];
  by_unknown?: ModelStatusNameCount[];
}

export interface ModelStatusResponse {
  stats: ModelStatusStats;
  registry: ModelStatusRegistry;
}

export interface UsageScopeCounter {
  requests?: number;
  tokens?: number;
}

export interface UsageStats extends StatsWithDailyHistory {
  available: boolean;
  day?: string;
  started_at?: number;
  requests_today?: number;
  tokens_today?: number;
  top_models?: PIINameCount[];
  top_providers?: PIINameCount[];
  counters?: Record<string, UsageScopeCounter>;
}

export interface UsageResponse {
  enabled: boolean;
  source?: string;
  stats?: UsageStats;
}

export interface ShareCreateResponse {
  id: string;
  url: string;
  provider: Provider;
  created_at: string;
  expires_at?: string;
}

export interface ShareInfo {
  id: string;
  provider: Provider;
  key: string;
  description?: string;
  enabled: boolean;
  proxy_base: string;
  base_url: string;
  created_at: string;
  created_by?: string;
  expires_at?: string;
}

export interface APIError {
  error: string;
}

export type KeyStatsSource = "memory" | "redis" | "redislive";

export interface KeyCostStats {
  source: KeyStatsSource;
  spend_usd: number;
  input_spend_usd: number;
  output_spend_usd: number;
  requests: number;
  input_tokens: number;
  output_tokens: number;
}

export interface KeyPIIStats {
  source: KeyStatsSource;
  detections: number;
}

export interface KeyDayPoint {
  day: string;
  value: number;
}

export interface KeyCostRecentEvent {
  time: number;
  provider: string;
  key_id?: string;
  spend_usd: number;
  input_spend_usd?: number;
  output_spend_usd?: number;
  input_tokens: number;
  output_tokens: number;
  model?: string;
}

export interface KeyPIIRecentEvent {
  time: number;
  provider: string;
  key_id?: string;
  entity_counts: Record<string, number>;
  entity_total: number;
  duration_ms: number;
  outcome: "ok" | "fail_open" | "fail_closed" | "oversize";
}

export interface KeyStatsResponse {
  masked_key_id: string;
  day: string;
  rollup_available: boolean;
  rollup_backend?: string;
  cost_today: KeyCostStats;
  pii_today: KeyPIIStats;
  cost_history: KeyDayPoint[];
  pii_history: KeyDayPoint[];
  recent_cost: KeyCostRecentEvent[];
  recent_pii: KeyPIIRecentEvent[];
}

export interface BYOBanRecord {
  provider: Provider;
  masked_id: string;
  hash: string;
  banned_by?: string;
  reason?: string;
  created_at: string;
}

export interface BYOKeyRecord {
  provider: Provider;
  masked_id: string;
  hash: string;
  banned: boolean;
  banned_by?: string;
  reason?: string;
  banned_at?: string;
  pii_scans: number;
  cost_requests: number;
  spend_usd: number;
  sources: string[];
}

export interface CreateBYOBanRequest {
  provider: Provider;
  masked_id: string;
  reason?: string;
}
