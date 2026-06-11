import { DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import {
  Button,
  Drawer,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from "antd";
import { useMemo, useState } from "react";

import {
  useCreateKey,
  useDeleteKey,
  useKeys,
  useUpdateKey,
} from "../../hooks/queries";
import type { APIKey, CreateAPIKeyRequest, PiiRedactSetting, Provider } from "../../types";

const PROVIDERS: Provider[] = ["openai", "anthropic", "gemini"];

type KeyFormValues = {
  provider: Provider;
  actual_key?: string;
  description?: string;
  daily_cost_limit_dollars?: number;
  enabled: boolean;
  redact_pii: "inherit" | "on" | "off";
};

function piiToFormValue(value?: PiiRedactSetting): KeyFormValues["redact_pii"] {
  if (value === true) return "on";
  if (value === false) return "off";
  return "inherit";
}

function piiFromFormValue(value: KeyFormValues["redact_pii"]): PiiRedactSetting {
  if (value === "on") return true;
  if (value === "off") return false;
  return null;
}

function piiLabel(value?: PiiRedactSetting): string {
  if (value === true) return "On";
  if (value === false) return "Off";
  return "Inherit";
}

function formatCostLimit(cents: number): string {
  return `$${(cents / 100).toFixed(2)}/day`;
}

export default function KeysPage() {
  const [providerFilter, setProviderFilter] = useState<Provider | undefined>();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingKey, setEditingKey] = useState<APIKey | null>(null);
  const [form] = Form.useForm<KeyFormValues>();

  const { data: keys = [], isLoading, error } = useKeys(providerFilter);
  const createKey = useCreateKey();
  const updateKey = useUpdateKey();
  const deleteKey = useDeleteKey();

  const openCreate = () => {
    setEditingKey(null);
    form.setFieldsValue({
      provider: "openai",
      enabled: true,
      redact_pii: "inherit",
      daily_cost_limit_dollars: 100,
    });
    setDrawerOpen(true);
  };

  const openEdit = (record: APIKey) => {
    setEditingKey(record);
    form.setFieldsValue({
      provider: record.provider,
      description: record.description,
      daily_cost_limit_dollars: record.daily_cost_limit / 100,
      enabled: record.enabled,
      redact_pii: piiToFormValue(record.redact_pii),
    });
    setDrawerOpen(true);
  };

  const closeDrawer = () => {
    setDrawerOpen(false);
    setEditingKey(null);
    form.resetFields();
  };

  const onSubmit = async (values: KeyFormValues) => {
    const dailyCostLimit = Math.round((values.daily_cost_limit_dollars ?? 0) * 100);
    const redactPii = piiFromFormValue(values.redact_pii);

    try {
      if (editingKey) {
        await updateKey.mutateAsync({
          key: editingKey.key,
          body: {
            description: values.description,
            daily_cost_limit: dailyCostLimit,
            enabled: values.enabled,
            redact_pii: redactPii,
          },
        });
        message.success("Key updated");
      } else {
        const body: CreateAPIKeyRequest = {
          provider: values.provider,
          actual_key: values.actual_key ?? "",
          description: values.description,
          daily_cost_limit: dailyCostLimit,
          enabled: values.enabled,
          redact_pii: redactPii,
        };
        await createKey.mutateAsync(body);
        message.success("Key created");
      }
      closeDrawer();
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Request failed");
    }
  };

  const onDelete = async (key: string) => {
    try {
      await deleteKey.mutateAsync(key);
      message.success("Key deleted");
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Delete failed");
    }
  };

  const columns = useMemo(
    () => [
      {
        title: "Key",
        dataIndex: "key",
        key: "key",
        render: (value: string) => <Typography.Text code>{value}</Typography.Text>,
      },
      {
        title: "Provider",
        dataIndex: "provider",
        key: "provider",
        render: (value: Provider) => <Tag>{value}</Tag>,
      },
      {
        title: "Enabled",
        dataIndex: "enabled",
        key: "enabled",
        render: (value: boolean) => (
          <Tag color={value ? "green" : "default"}>{value ? "Yes" : "No"}</Tag>
        ),
      },
      {
        title: "Cost limit",
        dataIndex: "daily_cost_limit",
        key: "daily_cost_limit",
        render: (value: number) => formatCostLimit(value),
      },
      {
        title: "PII redact",
        dataIndex: "redact_pii",
        key: "redact_pii",
        render: (value: PiiRedactSetting) => piiLabel(value),
      },
      {
        title: "Description",
        dataIndex: "description",
        key: "description",
        ellipsis: true,
      },
      {
        title: "Actions",
        key: "actions",
        render: (_: unknown, record: APIKey) => (
          <Space>
            <Button icon={<EditOutlined />} onClick={() => openEdit(record)}>
              Edit
            </Button>
            <Popconfirm title="Delete this key?" onConfirm={() => onDelete(record.key)}>
              <Button danger icon={<DeleteOutlined />}>
                Delete
              </Button>
            </Popconfirm>
          </Space>
        ),
      },
    ],
    [],
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Typography.Title level={3} className="!mb-1">
            API Keys
          </Typography.Title>
          <Typography.Paragraph type="secondary" className="!mb-0">
            Manage iw: proxy keys, cost limits, and per-key PII redaction overrides.
          </Typography.Paragraph>
        </div>
        <Space>
          <Select
            allowClear
            placeholder="Filter provider"
            style={{ width: 180 }}
            value={providerFilter}
            onChange={(value) => setProviderFilter(value)}
            options={PROVIDERS.map((provider) => ({ value: provider, label: provider }))}
          />
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            Create key
          </Button>
        </Space>
      </div>

      {error != null ? (
        <Typography.Text type="danger">
          {error instanceof Error ? error.message : "Failed to load keys"}
        </Typography.Text>
      ) : null}

      <Table rowKey="key" loading={isLoading} columns={columns} dataSource={keys} pagination={{ pageSize: 20 }} />

      <Drawer
        title={editingKey ? "Edit API key" : "Create API key"}
        width={480}
        open={drawerOpen}
        onClose={closeDrawer}
        destroyOnClose
      >
        <Form form={form} layout="vertical" onFinish={onSubmit}>
          <Form.Item
            name="provider"
            label="Provider"
            rules={[{ required: true, message: "Provider is required" }]}
          >
            <Select
              disabled={Boolean(editingKey)}
              options={PROVIDERS.map((provider) => ({ value: provider, label: provider }))}
            />
          </Form.Item>

          {!editingKey && (
            <Form.Item
              name="actual_key"
              label="Provider API key"
              rules={[{ required: true, message: "Provider key is required" }]}
            >
              <Input.Password placeholder="sk-..." />
            </Form.Item>
          )}

          <Form.Item name="description" label="Description">
            <Input.TextArea rows={2} placeholder="What is this key used for?" />
          </Form.Item>

          <Form.Item
            name="daily_cost_limit_dollars"
            label="Daily cost limit (USD)"
            rules={[{ required: true, message: "Cost limit is required" }]}
          >
            <InputNumber className="w-full" min={0} step={1} prefix="$" />
          </Form.Item>

          <Form.Item name="enabled" label="Enabled" valuePropName="checked">
            <Switch />
          </Form.Item>

          <Form.Item name="redact_pii" label="PII redaction">
            <Select
              options={[
                { value: "inherit", label: "Inherit global default" },
                { value: "on", label: "On" },
                { value: "off", label: "Off" },
              ]}
            />
          </Form.Item>

          <Space>
            <Button onClick={closeDrawer}>Cancel</Button>
            <Button
              type="primary"
              htmlType="submit"
              loading={createKey.isLoading || updateKey.isLoading}
            >
              {editingKey ? "Save changes" : "Create key"}
            </Button>
          </Space>
        </Form>
      </Drawer>
    </div>
  );
}
