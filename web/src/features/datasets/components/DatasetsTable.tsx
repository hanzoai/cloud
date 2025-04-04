import { DataTable } from "@/src/components/table/data-table";
import TableLink from "@/src/components/table/table-link";
import { type HanzoColumnDef } from "@/src/components/table/types";
import { Button } from "@/src/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/src/components/ui/dropdown-menu";
import { DatasetActionButton } from "@/src/features/datasets/components/DatasetActionButton";
import { useDetailPageLists } from "@/src/features/navigate-detail-pages/context";
import { api } from "@/src/utils/api";
import { useQueryParams, withDefault, NumberParam } from "use-query-params";
import { type RouterOutput } from "@/src/utils/types";
import { MoreVertical } from "lucide-react";
import { useEffect } from "react";
import useColumnVisibility from "@/src/features/column-visibility/hooks/useColumnVisibility";
import { DataTableToolbar } from "@/src/components/table/data-table-toolbar";
import { type Prisma } from "@hanzo/shared";
import { IOTableCell } from "@/src/components/ui/CodeJsonViewer";
import { useRowHeightLocalStorage } from "@/src/components/table/data-table-row-height-switch";
import useColumnOrder from "@/src/features/column-visibility/hooks/useColumnOrder";
import { LocalIsoDate } from "@/src/components/LocalIsoDate";

type RowData = {
  key: {
    id: string;
    name: string;
  };
  description?: string;
  createdAt: Date;
  lastRunAt?: Date;
  countItems: number;
  countRuns: number;
  metadata: Prisma.JsonValue;
};

export function DatasetsTable(props: { projectId: string }) {
  const { setDetailPageList } = useDetailPageLists();

  const [rowHeight, setRowHeight] = useRowHeightLocalStorage("datasets", "s");

  const [paginationState, setPaginationState] = useQueryParams({
    pageIndex: withDefault(NumberParam, 0),
    pageSize: withDefault(NumberParam, 50),
  });

  const datasets = api.datasets.allDatasets.useQuery({
    projectId: props.projectId,
    page: paginationState.pageIndex,
    limit: paginationState.pageSize,
  });

  useEffect(() => {
    if (datasets.isSuccess) {
      setDetailPageList(
        "datasets",
        datasets.data.datasets.map((t) => ({ id: t.id })),
      );
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [datasets.isSuccess, datasets.data]);

  const columns: HanzoColumnDef<RowData>[] = [
    {
      accessorKey: "key",
      header: "Name",
      id: "key",
      size: 150,
      isPinned: true,
      cell: ({ row }) => {
        const key: RowData["key"] = row.getValue("key");
        return (
          <TableLink
            path={`/project/${props.projectId}/datasets/${key.id}`}
            value={key.name}
          />
        );
      },
    },
    {
      accessorKey: "description",
      header: "Description",
      id: "description",
      enableHiding: true,
      size: 200,
      cell: ({ row }) => {
        const description: RowData["description"] = row.getValue("description");
        return <div className="h-full overflow-y-auto">{description}</div>;
      },
    },
    {
      accessorKey: "countItems",
      header: "Items",
      id: "countItems",
      enableHiding: true,
      size: 60,
    },
    {
      accessorKey: "countRuns",
      header: "Runs",
      id: "countRuns",
      enableHiding: true,
      size: 60,
    },
    {
      accessorKey: "createdAt",
      header: "Created",
      id: "createdAt",
      enableHiding: true,
      size: 150,
      cell: ({ row }) => {
        const value: RowData["createdAt"] = row.getValue("createdAt");
        return <LocalIsoDate date={value} />;
      },
    },
    {
      accessorKey: "lastRunAt",
      header: "Last Run",
      id: "lastRunAt",
      enableHiding: true,
      size: 150,
      cell: ({ row }) => {
        const value: RowData["lastRunAt"] = row.getValue("lastRunAt");
        return value ? <LocalIsoDate date={value} /> : undefined;
      },
    },
    {
      accessorKey: "metadata",
      header: "Metadata",
      id: "metadata",
      enableHiding: true,
      size: 300,
      cell: ({ row }) => {
        const metadata: RowData["metadata"] = row.getValue("metadata");
        return !!metadata ? (
          <IOTableCell data={metadata} singleLine={rowHeight === "s"} />
        ) : null;
      },
    },
    {
      id: "actions",
      accessorKey: "actions",
      header: "Actions",
      size: 70,
      cell: ({ row }) => {
        const key: RowData["key"] = row.getValue("key");
        return (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" className="h-8 w-8 p-0">
                <span className="sr-only">Open menu</span>
                <MoreVertical className="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className="flex flex-col [&>*]:w-full [&>*]:justify-start"
            >
              <DropdownMenuLabel>Actions</DropdownMenuLabel>
              <DropdownMenuItem asChild>
                <DatasetActionButton
                  mode="update"
                  projectId={props.projectId}
                  datasetId={key.id}
                  datasetName={key.name}
                  datasetDescription={row.getValue("description") ?? undefined}
                />
              </DropdownMenuItem>
              <DropdownMenuItem asChild>
                <DatasetActionButton
                  mode="delete"
                  projectId={props.projectId}
                  datasetId={key.id}
                  datasetName={key.name}
                />
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        );
      },
    },
  ];

  const convertToTableRow = (
    item: RouterOutput["datasets"]["allDatasets"]["datasets"][number],
  ): RowData => {
    return {
      key: { id: item.id, name: item.name },
      description: item.description ?? "",
      createdAt: item.createdAt,
      lastRunAt: item.lastRunAt ?? undefined,
      countItems: item.countDatasetItems,
      countRuns: item.countDatasetRuns,
      metadata: item.metadata,
    };
  };

  const [columnVisibility, setColumnVisibility] = useColumnVisibility<RowData>(
    "datasetsColumnVisibility",
    columns,
  );

  const [columnOrder, setColumnOrder] = useColumnOrder<RowData>(
    "datasetsColumnOrder",
    columns,
  );

  return (
    <>
      <DataTableToolbar
        columns={columns}
        columnVisibility={columnVisibility}
        setColumnVisibility={setColumnVisibility}
        columnOrder={columnOrder}
        setColumnOrder={setColumnOrder}
        actionButtons={
          <DatasetActionButton projectId={props.projectId} mode="create" />
        }
        rowHeight={rowHeight}
        setRowHeight={setRowHeight}
      />
      <DataTable
        columns={columns}
        data={
          datasets.isLoading
            ? { isLoading: true, isError: false }
            : datasets.isError
              ? {
                  isLoading: false,
                  isError: true,
                  error: datasets.error.message,
                }
              : {
                  isLoading: false,
                  isError: false,
                  data: datasets.data.datasets.map((t) => convertToTableRow(t)),
                }
        }
        pagination={{
          totalCount: datasets.data?.totalDatasets ?? null,
          onChange: setPaginationState,
          state: paginationState,
        }}
        columnVisibility={columnVisibility}
        onColumnVisibilityChange={setColumnVisibility}
        columnOrder={columnOrder}
        onColumnOrderChange={setColumnOrder}
        rowHeight={rowHeight}
      />
    </>
  );
}
