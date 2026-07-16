import { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";

import DefaultRedirect from "./components/default-redirect";
import AppShell from "./components/app-shell";
import RequireRole from "./components/require-role";
import ErrorBoundary from "./components/ui/error-boundary";
import { queryClient } from "./client";
import CircuitPage from "./pages/circuit";
import ConfigPage from "./pages/config";
import CostPage from "./pages/cost";
import KeyDetailPage from "./pages/keys/detail";
import KeySetupPage from "./pages/keys/setup";
import KeysPage from "./pages/keys";
import LoginPage from "./pages/login";
import ModelStatusPage from "./pages/model-status";
import OverviewPage from "./pages/overview";
import PIIPage from "./pages/pii";
import RateLimitsPage from "./pages/rate-limits";
import SharePage from "./pages/share";
import UsersPage from "./pages/users";
import UsagePage from "./pages/usage";
import { ADMIN_BASENAME } from "./lib/admin-path";

function shell(page: ReactNode) {
  return (
    <AppShell>
      <ErrorBoundary>{page}</ErrorBoundary>
    </AppShell>
  );
}

export default function Router() {
  const basename = window.location.pathname.startsWith(`${ADMIN_BASENAME}/`) ? ADMIN_BASENAME : undefined;

  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename={basename} future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/share/:id" element={<SharePage />} />
          <Route
            path="/"
            element={shell(
              <RequireRole minRole="editor">
                <OverviewPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/usage"
            element={shell(
              <RequireRole minRole="editor">
                <UsagePage />
              </RequireRole>,
            )}
          />
          <Route
            path="/circuit"
            element={shell(
              <RequireRole minRole="editor">
                <CircuitPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/rate-limits"
            element={shell(
              <RequireRole minRole="editor">
                <RateLimitsPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/cost"
            element={shell(
              <RequireRole minRole="editor">
                <CostPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/pii"
            element={shell(
              <RequireRole minRole="editor">
                <PIIPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/model-status"
            element={shell(
              <RequireRole minRole="editor">
                <ModelStatusPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/config"
            element={shell(
              <RequireRole minRole="admin">
                <ConfigPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/keys/:key/setup"
            element={shell(
              <RequireRole minRole="viewer">
                <KeySetupPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/keys/:key"
            element={shell(
              <RequireRole minRole="viewer">
                <KeyDetailPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/keys"
            element={shell(
              <RequireRole minRole="viewer">
                <KeysPage />
              </RequireRole>,
            )}
          />
          <Route path="/request-key" element={<Navigate to="/keys?request=1" replace />} />
          <Route path="/key-requests" element={<Navigate to="/keys?tab=requests" replace />} />
          <Route
            path="/users"
            element={shell(
              <RequireRole minRole="admin">
                <UsersPage />
              </RequireRole>,
            )}
          />
          <Route path="*" element={<DefaultRedirect />} />
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
