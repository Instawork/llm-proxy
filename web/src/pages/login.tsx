import { Button, Card, Typography } from "antd";
import { useEffect } from "react";
import { useSearchParams } from "react-router-dom";

export default function LoginPage() {
  const [params] = useSearchParams();

  useEffect(() => {
    const redirect = params.get("redirect");
    const loginUrl = redirect
      ? `/admin/auth/login?redirect=${encodeURIComponent(redirect)}`
      : "/admin/auth/login";
    window.location.replace(loginUrl);
  }, [params]);

  const onLogin = () => {
    const redirect = params.get("redirect");
    const loginUrl = redirect
      ? `/admin/auth/login?redirect=${encodeURIComponent(redirect)}`
      : "/admin/auth/login";
    window.location.href = loginUrl;
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-50 p-6">
      <Card className="w-full max-w-md text-center">
        <Typography.Title level={3}>LLM Proxy Admin</Typography.Title>
        <Typography.Paragraph type="secondary">
          Sign in with your Instawork Google account to manage proxy keys and configuration.
        </Typography.Paragraph>
        <Button type="primary" size="large" onClick={onLogin}>
          Continue to Google sign-in
        </Button>
      </Card>
    </div>
  );
}
