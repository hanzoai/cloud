import { type Plan } from "@hanzo/shared";

// Entitlements: Binary feature access
const entitlements = [
  // features
  "playground",
  "model-based-evaluations",
  "rbac-project-roles",
  "cloud-billing",
  "integration-posthog",
  "annotation-queues",
  "self-host-ui-customization",
  "self-host-allowed-organization-creators",
  "prompt-experiments",
  "trace-deletion", // Not in use anymore, but necessary to use the TableAction type.
  "audit-logs",
  "data-retention",
] as const;
export type Entitlement = (typeof entitlements)[number];

const cloudAllPlansEntitlements: Entitlement[] = [
  "playground",
  "model-based-evaluations",
  "cloud-billing",
  "integration-posthog",
  "annotation-queues",
  "prompt-experiments",
  "trace-deletion",
];

const selfHostedAllPlansEntitlements: Entitlement[] = ["trace-deletion"];

// Entitlement Limits: Limits on the number of resources that can be created/used
const entitlementLimits = [
  "annotation-queue-count",
  "organization-member-count",
  "data-access-days",
  "model-based-evaluations-count-evaluators",
  "prompt-management-count-prompts",
] as const;
export type EntitlementLimit = (typeof entitlementLimits)[number];

export type EntitlementLimits = Record<
  EntitlementLimit,
  | number // if limited
  | false // unlimited
>;

export const entitlementAccess: Record<
  Plan,
  {
    entitlements: Entitlement[];
    entitlementLimits: EntitlementLimits;
  }
> = {
  "cloud:free": {  // Add new free plan
    entitlements: [...cloudAllPlansEntitlements],
    entitlementLimits: {
      "annotation-queue-count": 0,
      "organization-member-count": 2,
      "data-access-days": 7,
      "model-based-evaluations-count-evaluators": 1,
      "prompt-management-count-prompts": false,
    },
  },
  "cloud:pro": {
    entitlements: [...cloudAllPlansEntitlements],
    entitlementLimits: {
      "annotation-queue-count": 3,
      "organization-member-count": false,
      "data-access-days": false,
      "model-based-evaluations-count-evaluators": false,
      "prompt-management-count-prompts": false,
    },
  },
  "cloud:team": {
    entitlements: [
      ...cloudAllPlansEntitlements,
      "rbac-project-roles",
      "audit-logs",
      "data-retention",
    ],
    entitlementLimits: {
      "annotation-queue-count": false,
      "organization-member-count": false,
      "data-access-days": false,
      "model-based-evaluations-count-evaluators": false,
      "prompt-management-count-prompts": false,
    },
  },
  "cloud:dev": {
    entitlements: [...cloudAllPlansEntitlements],
    entitlementLimits: {
      "annotation-queue-count": 1,
      "organization-member-count": 2,
      "data-access-days": 30,
      "model-based-evaluations-count-evaluators": 1,
      "prompt-management-count-prompts": false,
    },
  },
  "self-hosted:pro": {
    entitlements: [
      ...selfHostedAllPlansEntitlements,
      "annotation-queues",
      "model-based-evaluations",
      "playground",
      "prompt-experiments",
      "integration-posthog",
    ],
    entitlementLimits: {
      "annotation-queue-count": false,
      "organization-member-count": false,
      "data-access-days": false,
      "model-based-evaluations-count-evaluators": false,
      "prompt-management-count-prompts": false,
    },
  },
  "self-hosted:team": {
    entitlements: [
      ...selfHostedAllPlansEntitlements,
      "annotation-queues",
      "model-based-evaluations",
      "playground",
      "prompt-experiments",
      "rbac-project-roles",
      "integration-posthog",
      "audit-logs",
      "data-retention",
      "self-host-ui-customization",
      "self-host-allowed-organization-creators",
    ],
    entitlementLimits: {
      "annotation-queue-count": false,
      "organization-member-count": false,
      "data-access-days": false,
      "model-based-evaluations-count-evaluators": false,
      "prompt-management-count-prompts": false,
    },
  },
  "self-hosted:dev": {
    entitlements: [
      ...selfHostedAllPlansEntitlements,
      "annotation-queues",
      "model-based-evaluations",
      "playground",
      "prompt-experiments",
      "integration-posthog",
    ],
    entitlementLimits: {
      "annotation-queue-count": 1,
      "organization-member-count": 2,
      "data-access-days": 30,
      "model-based-evaluations-count-evaluators": 1,
      "prompt-management-count-prompts": false,
    },
  },
};
