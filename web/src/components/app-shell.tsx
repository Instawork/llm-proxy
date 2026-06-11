import { DashboardOutlined, KeyOutlined, LogoutOutlined } from "@ant-design/icons";
import { Button, Layout, Menu, Typography } from "antd";
import { ReactNode } from "react";
import { Link, useLocation } from "react-router-dom";

import { useLogout, useMe } from "../hooks/queries";

const { Header, Content, Sider } = Layout;

export default function AppShell({ children }: { children: ReactNode }) {
  const location = useLocation();
  const { data: me } = useMe();
  const logout = useLogout();

  const selectedKey = location.pathname.startsWith("/keys") ? "keys" : "dashboard";

  const onLogout = async () => {
    await logout.mutateAsync();
    window.location.href = "/admin/login";
  };

  return (
    <Layout className="min-h-screen">
      <Sider breakpoint="lg" collapsedWidth={0}>
        <div className="px-4 py-5">
          <Typography.Title level={4} className="!m-0 !text-white">
            LLM Proxy
          </Typography.Title>
          <Typography.Text className="text-white/70">Admin</Typography.Text>
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[selectedKey]}
          items={[
            {
              key: "dashboard",
              icon: <DashboardOutlined />,
              label: <Link to="/">Dashboard</Link>,
            },
            {
              key: "keys",
              icon: <KeyOutlined />,
              label: <Link to="/keys">API Keys</Link>,
            },
          ]}
        />
      </Sider>
      <Layout>
        <Header className="flex items-center justify-end gap-4 bg-white px-6 shadow-sm">
          {me?.email && <Typography.Text type="secondary">{me.email}</Typography.Text>}
          <Button icon={<LogoutOutlined />} loading={logout.isLoading} onClick={onLogout}>
            Log out
          </Button>
        </Header>
        <Content className="p-6">{children}</Content>
      </Layout>
    </Layout>
  );
}
