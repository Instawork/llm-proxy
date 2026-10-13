import { FormEvent, useMemo, useState } from "react";

import UsersTable from "../../components/users/users-table";
import PageHeader, { ErrorAlert, LoadingBlock } from "../../components/ui/page-header";
import { useToast } from "../../components/ui/toast";
import { APIClientError } from "../../client";
import {
  useCreateUser,
  useDeleteUser,
  useMe,
  useUpdateUser,
  useUsers,
} from "../../hooks/queries";
import type { AdminRole, AdminUserRecord } from "../../types";

const ROLES: AdminRole[] = ["viewer", "editor", "admin"];

export default function UsersPage() {
  const { data: me } = useMe();
  const usersQuery = useUsers();
  const createUser = useCreateUser();
  const updateUser = useUpdateUser();
  const deleteUser = useDeleteUser();
  const toast = useToast();

  const [addOpen, setAddOpen] = useState(false);
  const [editUser, setEditUser] = useState<AdminUserRecord | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminUserRecord | null>(null);
  const [addEmail, setAddEmail] = useState("");
  const [addRole, setAddRole] = useState<AdminRole>("viewer");
  const [addServiceKeys, setAddServiceKeys] = useState(false);
  const [editRole, setEditRole] = useState<AdminRole>("viewer");
  const [editServiceKeys, setEditServiceKeys] = useState(false);

  const adminCount = useMemo(
    () => (usersQuery.data ?? []).filter((u) => u.role === "admin").length,
    [usersQuery.data],
  );

  const forbidden = usersQuery.error instanceof APIClientError && usersQuery.error.status === 403;

  const onAddSubmit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await createUser.mutateAsync({
        email: addEmail.trim(),
        role: addRole,
        ...(addRole === "viewer" && addServiceKeys ? { can_view_service_keys: true } : {}),
      });
      toast.push("User added", "success");
      setAddOpen(false);
      setAddEmail("");
      setAddRole("viewer");
      setAddServiceKeys(false);
    } catch (err) {
      toast.push(err instanceof Error ? err.message : "Failed to add user", "error");
    }
  };

  const onEditSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!editUser) return;
    try {
      const patch: { role?: AdminRole; can_view_service_keys?: boolean } = {};
      if (editRole !== editUser.role) patch.role = editRole;
      if (editServiceKeys !== Boolean(editUser.can_view_service_keys)) {
        patch.can_view_service_keys = editServiceKeys;
      }
      if (Object.keys(patch).length > 0) {
        await updateUser.mutateAsync({ email: editUser.email, ...patch });
      }
      toast.push("User updated", "success");
      setEditUser(null);
    } catch (err) {
      toast.push(err instanceof Error ? err.message : "Failed to update user", "error");
    }
  };

  const onConfirmDelete = async () => {
    if (!deleteTarget) return;
    try {
      await deleteUser.mutateAsync(deleteTarget.email);
      toast.push("User deleted", "success");
      setDeleteTarget(null);
    } catch (err) {
      toast.push(err instanceof Error ? err.message : "Failed to delete user", "error");
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Users"
        description="Manage admin dashboard access. Pre-provision roles before a user's first sign-in."
        actions={
          <button type="button" className="btn btn-primary btn-sm" onClick={() => setAddOpen(true)}>
            Add user
          </button>
        }
      />

      {forbidden ? (
        <ErrorAlert message="You do not have permission to view this page." />
      ) : usersQuery.isLoading ? (
        <LoadingBlock />
      ) : usersQuery.isError ? (
        <ErrorAlert message={usersQuery.error instanceof Error ? usersQuery.error.message : "Failed to load users"} />
      ) : (
        <UsersTable
          users={usersQuery.data ?? []}
          currentEmail={me?.email}
          adminCount={adminCount}
          onEdit={(user) => {
            setEditUser(user);
            setEditRole(user.role);
            setEditServiceKeys(Boolean(user.can_view_service_keys));
          }}
          onDelete={setDeleteTarget}
        />
      )}

      {addOpen ? (
        <dialog className="modal modal-open">
          <div className="modal-box">
            <h3 className="text-lg font-semibold">Add user</h3>
            <form className="mt-4 space-y-4" onSubmit={onAddSubmit}>
              <label className="form-control w-full">
                <span className="label-text">Email</span>
                <input
                  type="email"
                  className="input input-bordered w-full"
                  required
                  value={addEmail}
                  onChange={(e) => setAddEmail(e.target.value)}
                />
              </label>
              <label className="form-control w-full">
                <span className="label-text">Role</span>
                <select
                  className="select select-bordered w-full"
                  value={addRole}
                  onChange={(e) => setAddRole(e.target.value as AdminRole)}
                >
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                </select>
              </label>
              <label className="label cursor-pointer justify-start gap-3">
                <input
                  type="checkbox"
                  className="checkbox checkbox-sm"
                  checked={addServiceKeys}
                  disabled={addRole !== "viewer"}
                  onChange={(e) => setAddServiceKeys(e.target.checked)}
                />
                <span className="label-text">
                  Can view service keys
                  <span className="block text-xs text-base-content/60">
                    Read-only access to org-wide service keys. Editors and admins always have it.
                  </span>
                </span>
              </label>
              <div className="modal-action">
                <button type="button" className="btn btn-ghost" onClick={() => setAddOpen(false)}>
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary" disabled={createUser.isPending}>
                  Add
                </button>
              </div>
            </form>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button type="button" onClick={() => setAddOpen(false)}>
              close
            </button>
          </form>
        </dialog>
      ) : null}

      {editUser ? (
        <dialog className="modal modal-open">
          <div className="modal-box">
            <h3 className="text-lg font-semibold">Edit user</h3>
            <p className="mt-1 text-sm text-base-content/60">{editUser.email}</p>
            <form className="mt-4 space-y-4" onSubmit={onEditSubmit}>
              <label className="form-control w-full">
                <span className="label-text">Role</span>
                <select
                  className="select select-bordered w-full"
                  value={editRole}
                  onChange={(e) => setEditRole(e.target.value as AdminRole)}
                  disabled={editUser.email === me?.email && editUser.role === "admin"}
                >
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                </select>
              </label>
              <label className="label cursor-pointer justify-start gap-3">
                <input
                  type="checkbox"
                  className="checkbox checkbox-sm"
                  checked={editServiceKeys}
                  disabled={editRole !== "viewer"}
                  onChange={(e) => setEditServiceKeys(e.target.checked)}
                />
                <span className="label-text">
                  Can view service keys
                  <span className="block text-xs text-base-content/60">
                    Read-only access to org-wide service keys. Editors and admins always have it.
                  </span>
                </span>
              </label>
              <div className="modal-action">
                <button type="button" className="btn btn-ghost" onClick={() => setEditUser(null)}>
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary" disabled={updateUser.isPending}>
                  Save
                </button>
              </div>
            </form>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button type="button" onClick={() => setEditUser(null)}>
              close
            </button>
          </form>
        </dialog>
      ) : null}

      {deleteTarget ? (
        <dialog className="modal modal-open">
          <div className="modal-box">
            <h3 className="text-lg font-semibold">Delete user</h3>
            <p className="py-4">
              Remove <span className="font-medium">{deleteTarget.email}</span>? They will be re-created as a viewer on
              next sign-in.
            </p>
            <div className="modal-action">
              <button type="button" className="btn btn-ghost" onClick={() => setDeleteTarget(null)}>
                Cancel
              </button>
              <button type="button" className="btn btn-error" disabled={deleteUser.isPending} onClick={onConfirmDelete}>
                Delete
              </button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button type="button" onClick={() => setDeleteTarget(null)}>
              close
            </button>
          </form>
        </dialog>
      ) : null}
    </div>
  );
}
