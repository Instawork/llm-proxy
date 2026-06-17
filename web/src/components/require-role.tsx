import { ReactNode } from "react";
import { Navigate } from "react-router-dom";

import { useMe } from "../hooks/queries";
import { defaultPathForRole } from "../lib/nav";
import { roleAtLeast } from "../lib/rbac";
import type { AdminRole } from "../types";
import { LoadingBlock } from "./ui/page-header";

export default function RequireRole({
  minRole,
  children,
}: {
  minRole: AdminRole;
  children: ReactNode;
}) {
  const { data: me, isLoading } = useMe();
  const role = me?.role ?? "viewer";

  if (isLoading) return <LoadingBlock />;
  if (!roleAtLeast(role, minRole)) {
    return <Navigate to={defaultPathForRole(role)} replace />;
  }
  return children;
}
