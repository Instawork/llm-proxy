import { useMemo } from "react";

import PageHeader, { ErrorAlert, LoadingBlock, StatusBadge } from "../components/ui/page-header";
import { SectionPanel } from "../components/ui/data-source";
import { useConfig } from "../hooks/queries";
import { featureEnabled, featureLabel, featureMeta } from "../lib/features";

export default function ConfigPage() {
  const { data, isLoading, error } = useConfig();

  const rows = useMemo(() => {
    if (!data?.features) return [];
    return Object.entries(data.features).map(([name, feature]) => ({
      name,
      enabled: featureEnabled(feature),
      meta: featureMeta(feature),
      label: featureLabel(name),
    }));
  }, [data]);

  if (isLoading) return <LoadingBlock />;
  if (error) {
    return <ErrorAlert message={error instanceof Error ? error.message : "Failed to load config"} />;
  }
  if (!data) return null;

  return (
    <div className="space-y-6">
      <PageHeader title="Configuration" description="Feature flags and active backends." />

      {data.environment ? (
        <div className="alert alert-info">
          <span>
            Environment: <strong>{data.environment}</strong>
          </span>
        </div>
      ) : null}

      <SectionPanel title="Features" subtitle="Loaded from proxy YAML / environment" source="config">
        <div className="overflow-x-auto">
          <table className="table table-zebra">
            <thead>
              <tr>
                <th>Feature</th>
                <th>Status</th>
                <th>Backend / mode</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.name}>
                  <td className="font-medium capitalize">{row.label}</td>
                  <td>
                    <StatusBadge active={row.enabled} activeLabel="Enabled" inactiveLabel="Disabled" />
                  </td>
                  <td className="text-base-content/70">{row.meta}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </SectionPanel>
    </div>
  );
}
