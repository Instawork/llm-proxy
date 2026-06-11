import { Alert, Descriptions, Spin, Table, Tabs, Tag, Typography } from "antd";
import { useMemo } from "react";

import { useConfig, useHealth, useRateLimits } from "../../hooks/queries";
import type { FeatureToggle, HealthResponse } from "../../types";

function featureStatus(feature?: FeatureToggle) {
  if (!feature) return <Tag>unknown</Tag>;
  return <Tag color={feature.enabled ? "green" : "default"}>{feature.enabled ? "enabled" : "disabled"}</Tag>;
}

function HealthTab() {
  const { data, isLoading, error } = useHealth();

  if (isLoading) return <Spin />;
  if (error) {
    return (
      <Alert
        type="error"
        message={error instanceof Error ? error.message : "Failed to load health"}
      />
    );
  }
  if (!data) return null;

  const circuitProviders = data.circuit_breaker?.providers ?? {};
  const providerRows = Object.entries(circuitProviders).map(([name, stats]) => ({
    key: name,
    provider: name,
    state: stats.state ?? stats.error ?? "unknown",
    failures: stats.failures ?? "—",
    rollupOpen: stats.rollup?.open ? "yes" : "no",
  }));

  return (
    <div className="space-y-4">
      <Descriptions bordered size="small" column={1}>
        <Descriptions.Item label="Status">
          <Tag color={data.status === "healthy" ? "green" : "red"}>{data.status}</Tag>
        </Descriptions.Item>
        <Descriptions.Item label="Timestamp">
          {new Date(data.timestamp * 1000).toLocaleString()}
        </Descriptions.Item>
        <Descriptions.Item label="Cost tracking">
          {data.features?.cost_tracking ? "on" : "off"}
        </Descriptions.Item>
        <Descriptions.Item label="Circuit breaker">
          {data.features?.circuit_breaker ? "on" : "off"}
        </Descriptions.Item>
      </Descriptions>

      {data.circuit_breaker && (
        <>
          <Typography.Title level={5}>Circuit breaker</Typography.Title>
          <Descriptions bordered size="small" column={2}>
            <Descriptions.Item label="Mode">{data.circuit_breaker.mode}</Descriptions.Item>
            <Descriptions.Item label="Backend">{data.circuit_breaker.backend}</Descriptions.Item>
            <Descriptions.Item label="Redis fallback">
              {data.circuit_breaker.redis_fallback ? "yes" : "no"}
            </Descriptions.Item>
            <Descriptions.Item label="Degraded signal">
              {data.circuit_breaker.degraded_signal ?? "—"}
            </Descriptions.Item>
          </Descriptions>
          <Table
            size="small"
            pagination={false}
            dataSource={providerRows}
            columns={[
              { title: "Provider", dataIndex: "provider" },
              { title: "State", dataIndex: "state" },
              { title: "Failures", dataIndex: "failures" },
              { title: "Rollup open", dataIndex: "rollupOpen" },
            ]}
          />
        </>
      )}

      <Typography.Title level={5}>Raw response</Typography.Title>
      <pre className="overflow-auto rounded bg-slate-100 p-4 text-xs">
        {JSON.stringify(data as HealthResponse, null, 2)}
      </pre>
    </div>
  );
}

function RateLimitsTab() {
  const { data, isLoading, error } = useRateLimits();

  if (isLoading) return <Spin />;
  if (error) {
    return (
      <Alert
        type="error"
        message={error instanceof Error ? error.message : "Failed to load rate limits"}
      />
    );
  }
  if (!data) return null;

  const snapshotRows = Object.entries(data.snapshot ?? {}).map(([scope, values]) => ({
    key: scope,
    scope,
    requests: values.requests ?? "—",
    tokens: values.tokens ?? "—",
  }));

  return (
    <div className="space-y-4">
      <Descriptions bordered size="small" column={2}>
        <Descriptions.Item label="Enabled">{data.enabled ? "yes" : "no"}</Descriptions.Item>
        <Descriptions.Item label="Backend">{data.backend ?? "—"}</Descriptions.Item>
        <Descriptions.Item label="RPM">{data.limits?.requests_per_minute ?? "—"}</Descriptions.Item>
        <Descriptions.Item label="TPM">{data.limits?.tokens_per_minute ?? "—"}</Descriptions.Item>
        <Descriptions.Item label="RPD">{data.limits?.requests_per_day ?? "—"}</Descriptions.Item>
        <Descriptions.Item label="TPD">{data.limits?.tokens_per_day ?? "—"}</Descriptions.Item>
      </Descriptions>

      {snapshotRows.length > 0 && (
        <>
          <Typography.Title level={5}>Live snapshot</Typography.Title>
          <Table
            size="small"
            pagination={false}
            dataSource={snapshotRows}
            columns={[
              { title: "Scope", dataIndex: "scope" },
              { title: "Requests", dataIndex: "requests" },
              { title: "Tokens", dataIndex: "tokens" },
            ]}
          />
        </>
      )}

      <Typography.Title level={5}>Raw response</Typography.Title>
      <pre className="overflow-auto rounded bg-slate-100 p-4 text-xs">
        {JSON.stringify(data, null, 2)}
      </pre>
    </div>
  );
}

function ConfigTab() {
  const { data, isLoading, error } = useConfig();

  const rows = useMemo(() => {
    if (!data?.features) return [];
    return Object.entries(data.features).map(([name, feature]) => ({
      key: name,
      feature: name,
      enabled: feature?.enabled ?? false,
      backend: feature?.backend ?? feature?.mode ?? "—",
      details: feature,
    }));
  }, [data]);

  if (isLoading) return <Spin />;
  if (error) {
    return (
      <Alert type="error" message={error instanceof Error ? error.message : "Failed to load config"} />
    );
  }
  if (!data) return null;

  return (
    <div className="space-y-4">
      {data.environment && (
        <Typography.Paragraph>
          Environment: <Tag>{data.environment}</Tag>
        </Typography.Paragraph>
      )}
      <Table
        size="small"
        pagination={false}
        dataSource={rows}
        columns={[
          { title: "Feature", dataIndex: "feature" },
          {
            title: "Status",
            dataIndex: "enabled",
            render: (enabled: boolean) => featureStatus({ enabled }),
          },
          { title: "Backend / mode", dataIndex: "backend" },
        ]}
      />
      <Typography.Title level={5}>Raw response</Typography.Title>
      <pre className="overflow-auto rounded bg-slate-100 p-4 text-xs">
        {JSON.stringify(data, null, 2)}
      </pre>
    </div>
  );
}

export default function DashboardPage() {
  return (
    <div className="space-y-4">
      <div>
        <Typography.Title level={3} className="!mb-1">
          Dashboard
        </Typography.Title>
        <Typography.Paragraph type="secondary" className="!mb-0">
          Proxy health, rate limits, and feature-flag summary.
        </Typography.Paragraph>
      </div>

      <Tabs
        items={[
          { key: "health", label: "Health", children: <HealthTab /> },
          { key: "rate-limits", label: "Rate limits", children: <RateLimitsTab /> },
          { key: "config", label: "Config", children: <ConfigTab /> },
        ]}
      />
    </div>
  );
}
