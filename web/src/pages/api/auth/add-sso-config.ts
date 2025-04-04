/**
 * This endpoint is used to add a new SSO configuration to the database.
 *
 * This is an EE feature and will return a 404 response if EE is not available.
 */

import { createNewSsoConfigHandler } from "@/src/features/multi-tenant-sso/createNewSsoConfigHandler";

export default createNewSsoConfigHandler;
