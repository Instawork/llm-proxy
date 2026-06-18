import { Navigate } from "react-router-dom";

import { useMe } from "../hooks/queries";
import { defaultPathForRole } from "../lib/nav";

export default function DefaultRedirect() {
  const { data: me } = useMe();
  const role = me?.role ?? "viewer";
  return <Navigate to={defaultPathForRole(role)} replace />;
}
