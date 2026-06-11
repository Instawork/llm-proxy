import { Button, Card, Space, Typography, message } from "antd";
import { useState } from "react";
import { useSearchParams } from "react-router-dom";

import { baseUrl } from "../client";

export default function LoginPage() {
  const [params] = useSearchParams();
  const [loading, setLoading] = useState(false);

  const afterLoginPath = () => {
    const redirect = params.get("redirect");
    if (redirect && redirect.startsWith("/")) {
      return redirect;
    }
    return "/";
  };

  const onGoogleLogin = () => {
    const redirect = encodeURIComponent(`${window.location.origin}/admin${afterLoginPath()}`);
    window.location.href = `${baseUrl}/admin/auth/login?redirect=${redirect}`;
  };

  const onDevLogin = async () => {
    setLoading(true);
    try {
      const redirect = `${window.location.origin}/admin${afterLoginPath()}`;
      const response = await fetch(`${baseUrl}/admin/auth/dev-login`, {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ redirect }),
      });
      if (!response.ok) {
        throw new Error(await response.text());
      }
      const body = (await response.json()) as { redirect?: string };
      window.location.href = body.redirect ?? `${window.location.origin}/admin${afterLoginPath()}`;
    } catch (err) {
      message.error(err instanceof Error ? err.message : "Dev login failed");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-50 p-6">
      <Card className="w-full max-w-md text-center">
        <Typography.Title level={3}>LLM Proxy Admin</Typography.Title>
        <Typography.Paragraph type="secondary">
          Sign in to manage proxy keys and configuration.
        </Typography.Paragraph>
        <Space direction="vertical" className="w-full" size="middle">
          {import.meta.env.DEV && (
            <Button type="primary" size="large" block loading={loading} onClick={onDevLogin}>
              Dev login (local session)
            </Button>
          )}
          <Button size="large" block onClick={onGoogleLogin}>
            Continue with Google
          </Button>
        </Space>
      </Card>
    </div>
  );
}
