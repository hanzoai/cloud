import { prisma } from "@hanzo/shared/src/db";
import { withMiddlewares } from "@/src/features/public-api/server/withMiddlewares";
import { createAuthedAPIRoute } from "@/src/features/public-api/server/createAuthedAPIRoute";
import { HanzoNotFoundError } from "@hanzo/shared";
import {
  GetCommentV1Query,
  GetCommentV1Response,
} from "@/src/features/public-api/types/comments";

export default withMiddlewares({
  GET: createAuthedAPIRoute({
    name: "Get Comment",
    querySchema: GetCommentV1Query,
    responseSchema: GetCommentV1Response,
    fn: async ({ query, auth }) => {
      const { commentId } = query;

      const comment = await prisma.comment.findUnique({
        where: {
          id: commentId,
          projectId: auth.scope.projectId,
        },
      });

      if (!comment) {
        throw new HanzoNotFoundError(
          "Comment not found within authorized project",
        );
      }

      return comment;
    },
  }),
});
