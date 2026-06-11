export type Provider = "openai" | "anthropic" | "gemini";

export type PiiRedactSetting = boolean | null;

export interface AdminUser {
  email: string;
  name?: string;
}

export interface APIKey {
  key: string;
  provider: Provider;
  description?: string;
  daily_cost_limit: number;
  enabled: boolean;
  redact_pii?: PiiRedactSetting;
  tags?: Record<string, string>;
  created_at: string;
  updated_at: string;
  expires_at?: string | null;
}

export interface CreateAPIKeyRequest {
  provider: Provider;
  actual_key: string;
  description?: string;
  daily_cost_limit?: number;
  enabled?: boolean;
  redact_pii?: PiiRedactSetting;
  tags?: Record<string, string>;
}

export interface UpdateAPIKeyRequest {
  enabled?: boolean;
  description?: string;
  daily_cost_limit?: number;
  redact_pii?: PiiRedactSetting;
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
  };
}

export interface RateLimitConfig {
  requests_per_minute?: number;
  tokens_per_minute?: number;
  requests_per_day?: number;
  tokens_per_day?: number;
}

export interface RateLimitSnapshotEntry {
  requests?: number;
  tokens?: number;
}

export interface RateLimitsResponse {
  enabled: boolean;
  backend?: string;
  limits?: RateLimitConfig;
  overrides?: Record<string, RateLimitConfig>;
  snapshot?: Record<string, RateLimitSnapshotEntry>;
}

export interface APIError {
  error: string;
}
