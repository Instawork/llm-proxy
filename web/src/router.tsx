import { QueryClientProvider } from "react-query";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";

import AppShell from "./components/app-shell";
import { queryClient } from "./client";
import DashboardPage from "./pages/dashboard";
import KeysPage from "./pages/keys";
import LoginPage from "./pages/login";

export default function Router() {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename="/admin">
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route
            path="/"
            element={
              <AppShell>
                <DashboardPage />
              </AppShell>
            }
          />
          <Route
            path="/keys"
            element={
              <AppShell>
                <KeysPage />
              </AppShell>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
