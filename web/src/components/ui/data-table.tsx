import { useEffect, useState } from "react";
import {
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type SortingState,
} from "@tanstack/react-table";

interface DataTableProps<T> {
  data: T[];
  columns: ColumnDef<T, unknown>[];
  searchPlaceholder?: string;
  emptyMessage?: string;
  getRowId?: (row: T, index: number) => string;
  /** When set, search/filter expands the table (e.g. disables top-N collapse). */
  onSearchActiveChange?: (active: boolean) => void;
  /** Optional footer shown when rows are collapsed elsewhere. */
  footer?: React.ReactNode;
  searchable?: boolean;
  tableClassName?: string;
  initialSorting?: SortingState;
}

export default function DataTable<T>({
  data,
  columns,
  searchPlaceholder = "Search…",
  emptyMessage = "No rows match",
  getRowId,
  onSearchActiveChange,
  footer,
  searchable = true,
  tableClassName = "table table-zebra",
  initialSorting = [],
}: DataTableProps<T>) {
  const [sorting, setSorting] = useState<SortingState>(initialSorting);
  const [globalFilter, setGlobalFilter] = useState("");

  const table = useReactTable({
    data,
    columns,
    state: { sorting, globalFilter },
    onSortingChange: setSorting,
    onGlobalFilterChange: setGlobalFilter,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getRowId: getRowId ? (row, index) => getRowId(row, index) : undefined,
    globalFilterFn: (row, _columnId, filter) => {
      const query = String(filter).trim().toLowerCase();
      if (!query) return true;
      return row.getVisibleCells().some((cell) => {
        const value = cell.getValue();
        if (value == null) return false;
        return String(value).toLowerCase().includes(query);
      });
    },
  });

  useEffect(() => {
    onSearchActiveChange?.(Boolean(globalFilter.trim()));
  }, [globalFilter, onSearchActiveChange]);

  const rows = table.getRowModel().rows;

  return (
    <div className="px-5 pb-5 pt-4">
      {searchable ? (
        <div className="mb-4">
          <label className="input input-bordered input-sm flex w-full max-w-sm items-center gap-2 bg-base-100/80">
            <svg
              xmlns="http://www.w3.org/2000/svg"
              viewBox="0 0 16 16"
              fill="currentColor"
              className="size-4 shrink-0 opacity-50"
              aria-hidden
            >
              <path
                fillRule="evenodd"
                d="M9.965 11.026a5 5 0 1 1 1.06-1.06l2.755 2.754a.75.75 0 1 1-1.06 1.06l-2.755-2.754ZM10.5 7a3.5 3.5 0 1 1-7 0 3.5 3.5 0 0 1 7 0Z"
                clipRule="evenodd"
              />
            </svg>
            <input
              type="search"
              className="grow bg-transparent"
              placeholder={searchPlaceholder}
              value={globalFilter}
              onChange={(event) => table.setGlobalFilter(event.target.value)}
            />
          </label>
        </div>
      ) : null}

      <div className="-mx-5 overflow-x-auto px-5">
        <table className={tableClassName}>
          <thead>
            {table.getHeaderGroups().map((headerGroup) => (
              <tr key={headerGroup.id}>
                {headerGroup.headers.map((header) => {
                  const canSort = header.column.getCanSort();
                  const sorted = header.column.getIsSorted();
                  return (
                    <th
                      key={header.id}
                      className={header.column.columnDef.meta?.alignRight ? "text-right" : undefined}
                    >
                      {header.isPlaceholder ? null : canSort ? (
                        <button
                          type="button"
                          className="inline-flex items-center gap-1 hover:text-base-content"
                          onClick={header.column.getToggleSortingHandler()}
                        >
                          {flexRender(header.column.columnDef.header, header.getContext())}
                          <span className="text-base-content/40" aria-hidden>
                            {sorted === "asc" ? "↑" : sorted === "desc" ? "↓" : "↕"}
                          </span>
                        </button>
                      ) : (
                        flexRender(header.column.columnDef.header, header.getContext())
                      )}
                    </th>
                  );
                })}
              </tr>
            ))}
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} className={row.original && (row.original as { isOthers?: boolean }).isOthers ? "text-base-content/70" : undefined}>
                {row.getVisibleCells().map((cell) => (
                  <td
                    key={cell.id}
                    className={cell.column.columnDef.meta?.alignRight ? "text-right" : undefined}
                  >
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                ))}
              </tr>
            ))}
            {rows.length === 0 ? (
              <tr>
                <td colSpan={columns.length} className="text-center text-base-content/50">
                  {emptyMessage}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      {footer ? <div className="mt-4 flex justify-end">{footer}</div> : null}
    </div>
  );
}

declare module "@tanstack/react-table" {
  interface ColumnMeta<TData, TValue> {
    alignRight?: boolean;
  }
}
