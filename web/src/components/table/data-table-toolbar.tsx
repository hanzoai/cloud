import { Button } from "@/src/components/ui/button";
import React, { type Dispatch, type SetStateAction, useState } from "react";
import { Input } from "@/src/components/ui/input";
import { DataTableColumnVisibilityFilter } from "@/src/components/table/data-table-column-visibility-filter";
import { type FilterState } from "@hanzo/shared";
import { PopoverFilterBuilder } from "@/src/features/filters/components/filter-builder";
import { type ColumnDefinition } from "@hanzo/shared";
import {
  type RowSelectionState,
  type ColumnOrderState,
  type VisibilityState,
} from "@tanstack/react-table";
import { type HanzoColumnDef } from "@/src/components/table/types";
import {
  DataTableRowHeightSwitch,
  type RowHeight,
} from "@/src/components/table/data-table-row-height-switch";
import { Search } from "lucide-react";
import { usePostHogClientCapture } from "@/src/features/posthog-analytics/usePostHogClientCapture";
import { TableDateRangeDropdown } from "@/src/components/date-range-dropdowns";
import {
  type TableDateRange,
  type TableDateRangeOptions,
} from "@/src/utils/date-range-utils";
import { DataTableSelectAllBanner } from "@/src/components/table/data-table-multi-select-actions/data-table-select-all-banner";
import { MultiSelect } from "@/src/features/filters/components/multi-select";

export interface MultiSelect {
  selectAll: boolean;
  setSelectAll: Dispatch<SetStateAction<boolean>>;
  selectedRowIds: string[];
  setRowSelection: Dispatch<SetStateAction<RowSelectionState>>;
  pageSize: number;
  pageIndex: number;
  totalCount: number | null;
}

interface SearchConfig {
  placeholder: string;
  updateQuery(event: string): void;
  currentQuery?: string;
}

interface DataTableToolbarProps<TData, TValue> {
  columns: HanzoColumnDef<TData, TValue>[];
  filterColumnDefinition?: ColumnDefinition[];
  searchConfig?: SearchConfig;
  actionButtons?: React.ReactNode;
  filterState?: FilterState;
  setFilterState?:
    | Dispatch<SetStateAction<FilterState>>
    | ((newState: FilterState) => void);
  columnVisibility?: VisibilityState;
  setColumnVisibility?: Dispatch<SetStateAction<VisibilityState>>;
  columnOrder?: ColumnOrderState;
  setColumnOrder?: Dispatch<SetStateAction<ColumnOrderState>>;
  rowHeight?: RowHeight;
  setRowHeight?: Dispatch<SetStateAction<RowHeight>>;
  columnsWithCustomSelect?: string[];
  selectedOption?: TableDateRangeOptions;
  setDateRangeAndOption?: (
    option: TableDateRangeOptions,
    date?: TableDateRange,
  ) => void;
  multiSelect?: MultiSelect;
  environmentFilter?: {
    values: string[];
    onValueChange: (values: string[]) => void;
    options: { value: string }[];
  };
}

export function DataTableToolbar<TData, TValue>({
  columns,
  filterColumnDefinition,
  searchConfig,
  actionButtons,
  filterState,
  setFilterState,
  columnVisibility,
  setColumnVisibility,
  columnOrder,
  setColumnOrder,
  rowHeight,
  setRowHeight,
  columnsWithCustomSelect,
  selectedOption,
  setDateRangeAndOption,
  multiSelect,
  environmentFilter,
}: DataTableToolbarProps<TData, TValue>) {
  const [searchString, setSearchString] = useState(
    searchConfig?.currentQuery ?? "",
  );
  const capture = usePostHogClientCapture();

  return (
    <>
      <div className="my-2 flex flex-wrap items-center gap-2 @container">
        {searchConfig && (
          <div className="flex max-w-md items-center">
            <Input
              autoFocus
              placeholder={searchConfig.placeholder}
              value={searchString}
              onChange={(event) => setSearchString(event.currentTarget.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  capture("table:search_submit");
                  searchConfig.updateQuery(searchString);
                }
              }}
              className="w-[150px] rounded-r-none @6xl:w-[250px]"
            />
            <Button
              variant="outline"
              onClick={() => {
                capture("table:search_submit");
                searchConfig.updateQuery(searchString);
              }}
              className="rounded-l-none border-l-0 p-3"
            >
              <Search className="h-4 w-4" />
            </Button>
          </div>
        )}
        {!!filterColumnDefinition && !!filterState && !!setFilterState && (
          <PopoverFilterBuilder
            columns={filterColumnDefinition}
            filterState={filterState}
            onChange={setFilterState}
            columnsWithCustomSelect={columnsWithCustomSelect}
          />
        )}
        {environmentFilter && (
          <MultiSelect
            title="Environment"
            label="Env"
            values={environmentFilter.values}
            onValueChange={environmentFilter.onValueChange}
            options={environmentFilter.options}
            className="my-0 w-auto overflow-hidden"
          />
        )}
        {selectedOption && setDateRangeAndOption && (
          <TableDateRangeDropdown
            selectedOption={selectedOption}
            setDateRangeAndOption={setDateRangeAndOption}
          />
        )}
        <div className="flex flex-row flex-wrap gap-2 pr-0.5 @6xl:ml-auto">
          {!!columnVisibility && !!setColumnVisibility && (
            <DataTableColumnVisibilityFilter
              columns={columns}
              columnVisibility={columnVisibility}
              setColumnVisibility={setColumnVisibility}
              columnOrder={columnOrder}
              setColumnOrder={setColumnOrder}
            />
          )}
          {!!rowHeight && !!setRowHeight && (
            <DataTableRowHeightSwitch
              rowHeight={rowHeight}
              setRowHeight={setRowHeight}
            />
          )}
          {actionButtons}
        </div>
      </div>
      {multiSelect &&
        multiSelect.pageIndex === 0 &&
        multiSelect.selectedRowIds.length === multiSelect.pageSize && (
          <DataTableSelectAllBanner {...multiSelect} />
        )}
    </>
  );
}
