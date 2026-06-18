import { Navigate } from "react-router-dom";

import { useMe } from "../hooks/queries";
import { defaultPathForRole } from "../lib/nav";
import { LoadingBlock } from "./ui/page-header";

export default function DefaultRedirect() {
  const { data: me, isLoading } = useMe();
  if (isLoading) {
    return <LoadingBlock />;
  }
  const role = me?.role ?? "viewer";
  return <Navigate to={defaultPathForRole(role)} replace />;
}
