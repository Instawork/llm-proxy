import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import DataTable from "../ui/data-table";
import type { AdminRole, AdminUserRecord } from "../../types";

function RoleBadge({ role }: { role: AdminRole }) {
  const cls =
    role === "admin"
      ? "badge badge-primary"
      : role === "editor"
        ? "badge badge-secondary"
        : "badge badge-ghost";
  return <span className={cls}>{role}</span>;
}

function formatOptionalTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

interface UsersTableProps {
  users: AdminUserRecord[];
  currentEmail?: string;
  adminCount: number;
  onEdit: (user: AdminUserRecord) => void;
  onDelete: (user: AdminUserRecord) => void;
}

export default function UsersTable({
  users,
  currentEmail,
  adminCount,
  onEdit,
  onDelete,
}: UsersTableProps) {
  const columns = useMemo<ColumnDef<AdminUserRecord, unknown>[]>(
    () => [
      {
        id: "email",
        accessorKey: "email",
        header: "Email",
        cell: ({ row }) => {
          const isSelf = currentEmail && row.original.email === currentEmail;
          return (
            <span>
              {row.original.email}
              {isSelf ? <span className="ml-2 text-xs text-base-content/50">(you)</span> : null}
            </span>
          );
        },
      },
      {
        id: "name",
        accessorKey: "name",
        header: "Name",
        cell: ({ getValue }) => (getValue<string>() || "—"),
      },
      {
        id: "role",
        accessorKey: "role",
        header: "Role",
        cell: ({ getValue }) => <RoleBadge role={getValue<AdminRole>()} />,
      },
      {
        id: "last_login_at",
        accessorKey: "last_login_at",
        header: "Last login",
        cell: ({ getValue }) => formatOptionalTime(getValue<string>()),
      },
      {
        id: "created_at",
        accessorKey: "created_at",
        header: "Created",
        cell: ({ getValue }) => formatOptionalTime(getValue<string>()),
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => {
          const user = row.original;
          const isSelf = currentEmail === user.email;
          const isLastAdmin = user.role === "admin" && adminCount <= 1;
          return (
            <div className="flex justify-end gap-2">
              <button type="button" className="btn btn-ghost btn-xs" disabled={isSelf} onClick={() => onEdit(user)}>
                Edit role
              </button>
              <button
                type="button"
                className="btn btn-ghost btn-xs text-error"
                disabled={isSelf || isLastAdmin}
                onClick={() => onDelete(user)}
              >
                Delete
              </button>
            </div>
          );
        },
      },
    ],
    [adminCount, currentEmail, onDelete, onEdit],
  );

  return <DataTable columns={columns} data={users} emptyMessage="No users yet" />;
}
