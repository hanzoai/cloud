import { prisma } from "@hanzo/shared/src/db";
import {
  GetSessionV1Query,
  GetSessionV1Response,
} from "@/src/features/public-api/types/sessions";
import { withMiddlewares } from "@/src/features/public-api/server/withMiddlewares";
import { createAuthedAPIRoute } from "@/src/features/public-api/server/createAuthedAPIRoute";
import { HanzoNotFoundError } from "@hanzo/shared";
import { getTracesBySessionId } from "@hanzo/shared/src/server";

export default withMiddlewares({
  GET: createAuthedAPIRoute({
    name: "Get Session",
    querySchema: GetSessionV1Query,
    responseSchema: GetSessionV1Response,
    fn: async ({ query, auth }) => {
      const { sessionId } = query;
      const session = await prisma.traceSession.findUnique({
        where: {
          id_projectId: {
            id: sessionId,
            projectId: auth.scope.projectId,
          },
        },
        select: {
          id: true,
          createdAt: true,
          projectId: true,
          environment: true,
        },
      });

      if (!session) {
        throw new HanzoNotFoundError(
          "Session not found within authorized project",
        );
      }

      const traces = await getTracesBySessionId(auth.scope.projectId, [
        sessionId,
      ]);

      return {
        ...session,
        traces,
      };
    },
  }),
});
