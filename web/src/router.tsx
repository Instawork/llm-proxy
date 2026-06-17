import { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";

import AppShell from "./components/app-shell";
import RequireRole from "./components/require-role";
import ErrorBoundary from "./components/ui/error-boundary";
import { queryClient } from "./client";
import CircuitPage from "./pages/circuit";
import ConfigPage from "./pages/config";
import CostPage from "./pages/cost";
import KeyDetailPage from "./pages/keys/detail";
import KeysPage from "./pages/keys";
import LoginPage from "./pages/login";
import OverviewPage from "./pages/overview";
import PIIPage from "./pages/pii";
import RateLimitsPage from "./pages/rate-limits";
import SharePage from "./pages/share";
import UsersPage from "./pages/users";
import UsagePage from "./pages/usage";

function shell(page: ReactNode) {
  return (
    <AppShell>
      <ErrorBoundary>{page}</ErrorBoundary>
    </AppShell>
  );
}

export default function Router() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename="/admin" future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
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
          <Route path="/usage" element={shell(<UsagePage />)} />
          <Route path="/circuit" element={shell(<CircuitPage />)} />
          <Route path="/rate-limits" element={shell(<RateLimitsPage />)} />
          <Route path="/cost" element={shell(<CostPage />)} />
          <Route path="/pii" element={shell(<PIIPage />)} />
          <Route
            path="/config"
            element={shell(
              <RequireRole minRole="admin">
                <ConfigPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/keys/:key"
            element={shell(
              <RequireRole minRole="editor">
                <KeyDetailPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/keys"
            element={shell(
              <RequireRole minRole="editor">
                <KeysPage />
              </RequireRole>,
            )}
          />
          <Route
            path="/users"
            element={shell(
              <RequireRole minRole="admin">
                <UsersPage />
              </RequireRole>,
            )}
          />
          <Route path="*" element={<Navigate to="/usage" replace />} />
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
