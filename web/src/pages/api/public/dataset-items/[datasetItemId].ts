import { prisma } from "@hanzo/shared/src/db";
import { withMiddlewares } from "@/src/features/public-api/server/withMiddlewares";
import { createAuthedAPIRoute } from "@/src/features/public-api/server/createAuthedAPIRoute";
import {
  GetDatasetItemV1Query,
  GetDatasetItemV1Response,
  DeleteDatasetItemV1Query,
  DeleteDatasetItemV1Response,
  transformDbDatasetItemToAPIDatasetItem,
} from "@/src/features/public-api/types/datasets";
import { HanzoNotFoundError } from "@hanzo/shared";

export default withMiddlewares({
  GET: createAuthedAPIRoute({
    name: "Get Dataset Item",
    querySchema: GetDatasetItemV1Query,
    responseSchema: GetDatasetItemV1Response,
    fn: async ({ query, auth }) => {
      const { datasetItemId } = query;

      const datasetItem = await prisma.datasetItem.findUnique({
        where: {
          id_projectId: {
            projectId: auth.scope.projectId,
            id: datasetItemId,
          },
        },
        include: {
          dataset: {
            select: {
              name: true,
            },
          },
        },
      });
      if (!datasetItem) {
        throw new HanzoNotFoundError("Dataset item not found");
      }

      const { dataset, ...datasetItemBody } = datasetItem;

      return transformDbDatasetItemToAPIDatasetItem({
        ...datasetItemBody,
        datasetName: dataset.name,
      });
    },
  }),
  DELETE: createAuthedAPIRoute({
    name: "Delete Dataset Item",
    querySchema: DeleteDatasetItemV1Query,
    responseSchema: DeleteDatasetItemV1Response,
    fn: async ({ query, auth }) => {
      const { datasetItemId } = query;

      // First get the item to check if it exists
      const datasetItem = await prisma.datasetItem.findUnique({
        where: {
          id_projectId: {
            projectId: auth.scope.projectId,
            id: datasetItemId,
          },
        },
      });

      if (!datasetItem) {
        throw new HanzoNotFoundError("Dataset item not found");
      }

      // Delete the dataset item
      await prisma.datasetItem.delete({
        where: {
          id_projectId: {
            projectId: auth.scope.projectId,
            id: datasetItemId,
          },
        },
      });

      return {
        message: "Dataset item successfully deleted" as const,
      };
    },
  }),
});
