import { ApiAuthService } from "@/src/features/public-api/server/apiAuth";
import { type NextApiRequest } from "next";
import { UnauthorizedError, ForbiddenError } from "@hanzo/shared";
import { prisma } from "@hanzo/shared/src/db";
import { redis } from "@hanzo/shared/src/server";

export async function authorizePromptRequestOrThrow(req: NextApiRequest) {
  const authCheck = await new ApiAuthService(
    prisma,
    redis,
  ).verifyAuthHeaderAndReturnScope(req.headers.authorization);
  if (!authCheck.validKey) throw new UnauthorizedError(authCheck.error);
  if (authCheck.scope.accessLevel !== "all")
    throw new ForbiddenError(
      `Access denied - need to use basic auth with secret key to ${req.method} prompts`,
    );
  return authCheck;
}
