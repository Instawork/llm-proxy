import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import DataTable from "../components/ui/data-table";
import PageHeader, { ErrorAlert, LoadingBlock, StatusBadge } from "../components/ui/page-header";
import { SectionPanel } from "../components/ui/data-source";
import { useConfig } from "../hooks/queries";
import { featureEnabled, featureLabel, featureMeta } from "../lib/features";

interface FeatureRow {
  name: string;
  label: string;
  enabled: boolean;
  meta: string;
}

export default function ConfigPage() {
  const { data, isLoading, error } = useConfig();

  const rows = useMemo<FeatureRow[]>(() => {
    if (!data?.features) return [];
    return Object.entries(data.features).map(([name, feature]) => ({
      name,
      enabled: featureEnabled(feature),
      meta: featureMeta(feature),
      label: featureLabel(name),
    }));
  }, [data]);

  const columns = useMemo<ColumnDef<FeatureRow, unknown>[]>(
    () => [
      {
        id: "feature",
        accessorKey: "label",
        header: "Feature",
        cell: ({ getValue }) => <span className="font-medium capitalize">{getValue<string>()}</span>,
      },
      {
        id: "status",
        accessorKey: "enabled",
        header: "Status",
        cell: ({ getValue }) => (
          <StatusBadge active={getValue<boolean>()} activeLabel="Enabled" inactiveLabel="Disabled" />
        ),
      },
      {
        id: "meta",
        accessorKey: "meta",
        header: "Backend / mode",
        cell: ({ getValue }) => <span className="text-base-content/70">{getValue<string>()}</span>,
      },
    ],
    [],
  );

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
        <DataTable
          data={rows}
          columns={columns}
          searchPlaceholder="Filter features…"
          emptyMessage="No features configured"
          getRowId={(row) => row.name}
        />
      </SectionPanel>
    </div>
  );
}
