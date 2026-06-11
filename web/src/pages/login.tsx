import { useState } from "react";
import { useSearchParams } from "react-router-dom";

import { authBaseUrl, baseUrl } from "../client";
import LLMProxyLogo from "../components/llm-proxy-logo";
import { useToast } from "../components/ui/toast";

export default function LoginPage() {
  const [params] = useSearchParams();
  const [loading, setLoading] = useState(false);
  const { push } = useToast();

  const afterLoginPath = () => {
    const redirect = params.get("redirect");
    if (redirect && redirect.startsWith("/")) {
      return redirect;
    }
    return "/";
  };

  const onGoogleLogin = () => {
    const redirect = encodeURIComponent(`${window.location.origin}/admin${afterLoginPath()}`);
    window.location.href = `${authBaseUrl}/admin/auth/login?redirect=${redirect}`;
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
      push(err instanceof Error ? err.message : "Dev login failed", "error");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-base-200 px-4 py-10">
      <div className="pointer-events-none fixed inset-0 bg-[radial-gradient(circle_at_top,rgba(59,130,246,0.18),transparent_40%),radial-gradient(circle_at_bottom,rgba(14,165,233,0.12),transparent_35%)]" />

      <div className="glass-panel relative w-full max-w-md p-8">
        <div className="mb-8 text-center">
          <div className="mx-auto mb-4 flex justify-center">
            <LLMProxyLogo size="lg" className="shadow-lg" />
          </div>
          <h1 className="text-2xl font-semibold tracking-tight">LLM Proxy Admin</h1>
          <p className="mt-2 text-sm text-base-content/60">
            Sign in to manage proxy keys, limits, and operational health.
          </p>
        </div>

        <div className="flex flex-col gap-3">
          {import.meta.env.DEV ? (
            <button
              type="button"
              className="btn btn-primary btn-lg"
              disabled={loading}
              onClick={onDevLogin}
            >
              {loading ? <span className="loading loading-spinner" /> : "Dev login (local session)"}
            </button>
          ) : null}
          <button type="button" className="btn btn-outline btn-lg gap-2" onClick={onGoogleLogin}>
            <svg viewBox="0 0 24 24" className="h-5 w-5" aria-hidden="true">
              <path
                fill="#4285F4"
                d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 0 1-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1Z"
              />
              <path
                fill="#34A853"
                d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23Z"
              />
              <path
                fill="#FBBC05"
                d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62Z"
              />
              <path
                fill="#EA4335"
                d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53Z"
              />
            </svg>
            Continue with Google
          </button>
        </div>
      </div>
    </div>
  );
}
