import { type RowData, type ColumnDef } from "@tanstack/react-table";
import { type LucideIcon } from "lucide-react";

export type TableRowOptions = {
  columnId: string;
  options: { label: string; value: number; icon?: LucideIcon }[];
};

// extends tanstack ColumnDef to include additional properties
type ExtendedColumnDef<TData extends RowData, TValue = unknown> = ColumnDef<
  TData,
  TValue
> & {
  defaultHidden?: boolean;
  headerTooltip?: {
    description: string;
    href?: string;
  };
  isPinned?: boolean; // if true, column cannot be reordered
};

// limits types of defined tanstack ColumnDef properties to specific subset of tanstack type union
export type HanzoColumnDef<
  TData extends RowData,
  TValue = unknown,
> = ExtendedColumnDef<TData, TValue> & {
  // Enforce hanzo columns to be of type 'AccessorKeyColumnDefBase' with 'accessorKey' property of type string
  accessorKey: string;
  // Enforce hanzo group columns to have children of type 'HanzoColumnDef'
  columns?: HanzoColumnDef<TData, TValue>[];
};
