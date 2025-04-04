import { removeEmptyEnvVariables } from "@hanzo/shared";
import { z } from "zod";

const EnvSchema = z.object({
  BUILD_ID: z.string().optional(),
  NODE_ENV: z
    .enum(["development", "test", "production"])
    .default("development"),
  DATABASE_URL: z.string(),
  HOSTNAME: z.string().default("0.0.0.0"),
  PORT: z.coerce
    .number({
      description:
        ".env files convert numbers to strings, therefoore we have to enforce them to be numbers",
    })
    .positive()
    .max(65536, `options.port should be >= 0 and < 65536`)
    .default(3030),

  HANZO_S3_BATCH_EXPORT_ENABLED: z.enum(["true", "false"]).default("false"),
  HANZO_S3_BATCH_EXPORT_BUCKET: z.string().optional(),
  HANZO_S3_BATCH_EXPORT_PREFIX: z.string().default(""),
  HANZO_S3_BATCH_EXPORT_REGION: z.string().optional(),
  HANZO_S3_BATCH_EXPORT_ENDPOINT: z.string().optional(),
  HANZO_S3_BATCH_EXPORT_ACCESS_KEY_ID: z.string().optional(),
  HANZO_S3_BATCH_EXPORT_SECRET_ACCESS_KEY: z.string().optional(),
  HANZO_S3_BATCH_EXPORT_FORCE_PATH_STYLE: z
    .enum(["true", "false"])
    .default("false"),

  HANZO_S3_EVENT_UPLOAD_BUCKET: z.string({
    required_error: "Hanzo Cloud requires a bucket name for S3 Event Uploads.",
  }),
  HANZO_S3_EVENT_UPLOAD_PREFIX: z.string().default(""),
  HANZO_S3_EVENT_UPLOAD_REGION: z.string().optional(),
  HANZO_S3_EVENT_UPLOAD_ENDPOINT: z.string().optional(),
  HANZO_S3_EVENT_UPLOAD_ACCESS_KEY_ID: z.string().optional(),
  HANZO_S3_EVENT_UPLOAD_SECRET_ACCESS_KEY: z.string().optional(),
  HANZO_S3_EVENT_UPLOAD_FORCE_PATH_STYLE: z
    .enum(["true", "false"])
    .default("false"),

  BATCH_EXPORT_ROW_LIMIT: z.coerce.number().positive().default(1_500_000),
  BATCH_EXPORT_DOWNLOAD_LINK_EXPIRATION_HOURS: z.coerce
    .number()
    .positive()
    .default(24),
  BATCH_ACTION_EXPORT_ROW_LIMIT: z.coerce.number().positive().default(50_000),
  HANZO_MAX_HISTORIC_EVAL_CREATION_LIMIT: z.coerce
    .number()
    .positive()
    .default(50_000),
  EMAIL_FROM_ADDRESS: z.string().optional(),
  SMTP_CONNECTION_URL: z.string().optional(),
  HANZO_INGESTION_QUEUE_PROCESSING_CONCURRENCY: z.coerce
    .number()
    .positive()
    .default(20),
  HANZO_INGESTION_SECONDARY_QUEUE_PROCESSING_CONCURRENCY: z.coerce
    .number()
    .positive()
    .default(5),
  HANZO_SECONDARY_INGESTION_QUEUE_ENABLED_PROJECT_IDS: z.string().optional(),
  HANZO_INGESTION_CLICKHOUSE_WRITE_BATCH_SIZE: z.coerce
    .number()
    .positive()
    .default(10000),
  HANZO_INGESTION_CLICKHOUSE_WRITE_INTERVAL_MS: z.coerce
    .number()
    .positive()
    .default(1000),
  HANZO_INGESTION_CLICKHOUSE_MAX_ATTEMPTS: z.coerce
    .number()
    .positive()
    .default(3),
  REDIS_HOST: z.string().nullish(),
  REDIS_PORT: z.coerce
    .number({
      description:
        ".env files convert numbers to strings, therefoore we have to enforce them to be numbers",
    })
    .positive()
    .max(65536, `options.port should be >= 0 and < 65536`)
    .default(6379)
    .nullable(),
  REDIS_AUTH: z.string().nullish(),
  REDIS_CONNECTION_STRING: z.string().nullish(),
  REDIS_ENABLE_AUTO_PIPELINING: z.enum(["true", "false"]).default("true"),

  CLICKHOUSE_URL: z.string().url(),
  CLICKHOUSE_USER: z.string(),
  CLICKHOUSE_CLUSTER_NAME: z.string().default("default"),
  CLICKHOUSE_DB: z.string().default("default"),
  CLICKHOUSE_PASSWORD: z.string(),
  HANZO_EVAL_CREATOR_WORKER_CONCURRENCY: z.coerce
    .number()
    .positive()
    .default(2),
  HANZO_TRACE_UPSERT_WORKER_CONCURRENCY: z.coerce
    .number()
    .positive()
    .default(25),
  HANZO_TRACE_DELETE_CONCURRENCY: z.coerce.number().positive().default(1),
  HANZO_PROJECT_DELETE_CONCURRENCY: z.coerce.number().positive().default(1),
  HANZO_EVAL_EXECUTION_WORKER_CONCURRENCY: z.coerce
    .number()
    .positive()
    .default(5),
  HANZO_EXPERIMENT_CREATOR_WORKER_CONCURRENCY: z.coerce
    .number()
    .positive()
    .default(5),
  STRIPE_SECRET_KEY: z.string().optional(),

  // Skip the read from ClickHouse within the Ingestion pipeline for the given
  // project ids. Applicable for projects that were created after the S3 write
  // was activated and which don't rely on historic updates.
  HANZO_SKIP_INGESTION_CLICKHOUSE_READ_PROJECT_IDS: z.string().default(""),
  // Set a date after which S3 was active. Projects created after this date do
  // perform a ClickHouse read as part of the ingestion pipeline.
  HANZO_SKIP_INGESTION_CLICKHOUSE_READ_MIN_PROJECT_CREATE_DATE: z
    .string()
    .date()
    .optional(),

  // Otel
  OTEL_EXPORTER_OTLP_ENDPOINT: z.string().default("http://localhost:4318"),
  OTEL_SERVICE_NAME: z.string().default("worker"),

  HANZO_ENABLE_BACKGROUND_MIGRATIONS: z
    .enum(["true", "false"])
    .default("true"),

  HANZO_ENABLE_REDIS_SEEN_EVENT_CACHE: z
    .enum(["true", "false"])
    .default("false"),

  // Flags to toggle queue consumers on or off.
  QUEUE_CONSUMER_CLOUD_USAGE_METERING_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_INGESTION_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_BATCH_EXPORT_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_BATCH_ACTION_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_EVAL_EXECUTION_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_TRACE_UPSERT_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_CREATE_EVAL_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_TRACE_DELETE_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_PROJECT_DELETE_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_DATASET_RUN_ITEM_UPSERT_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_EXPERIMENT_CREATE_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_POSTHOG_INTEGRATION_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_INGESTION_SECONDARY_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),
  QUEUE_CONSUMER_DATA_RETENTION_QUEUE_IS_ENABLED: z
    .enum(["true", "false"])
    .default("true"),

  // Core data S3 upload - Hanzo Cloud
  HANZO_S3_CORE_DATA_EXPORT_IS_ENABLED: z
    .enum(["true", "false"])
    .default("false"),
  HANZO_S3_CORE_DATA_UPLOAD_BUCKET: z.string().optional(),
  HANZO_S3_CORE_DATA_UPLOAD_PREFIX: z.string().default(""),
  HANZO_S3_CORE_DATA_UPLOAD_REGION: z.string().optional(),
  HANZO_S3_CORE_DATA_UPLOAD_ENDPOINT: z.string().optional(),
  HANZO_S3_CORE_DATA_UPLOAD_ACCESS_KEY_ID: z.string().optional(),
  HANZO_S3_CORE_DATA_UPLOAD_SECRET_ACCESS_KEY: z.string().optional(),
  HANZO_S3_CORE_DATA_UPLOAD_FORCE_PATH_STYLE: z
    .enum(["true", "false"])
    .default("false"),

  // Media upload
  HANZO_S3_MEDIA_UPLOAD_BUCKET: z.string().optional(),
  HANZO_S3_MEDIA_UPLOAD_PREFIX: z.string().default(""),
  HANZO_S3_MEDIA_UPLOAD_REGION: z.string().optional(),
  HANZO_S3_MEDIA_UPLOAD_ENDPOINT: z.string().optional(),
  HANZO_S3_MEDIA_UPLOAD_ACCESS_KEY_ID: z.string().optional(),
  HANZO_S3_MEDIA_UPLOAD_SECRET_ACCESS_KEY: z.string().optional(),
  HANZO_S3_MEDIA_UPLOAD_FORCE_PATH_STYLE: z
    .enum(["true", "false"])
    .default("false"),

  // Metering data Postgres export - HanzoCloudud Cloud
  HANZO_POSTGRES_METERING_DATA_EXPORT_IS_ENABLED: z
    .enum(["true", "false"])
    .default("false"),

  HANZO_S3_CONCURRENT_READS: z.coerce
    .number()
    .positive()
    .default(50),
});

export const env: z.infer<typeof EnvSchema> =
  process.env.DOCKER_BUILD === "1"
    ? (process.env as any)
    : EnvSchema.parse(removeEmptyEnvVariables(process.env));
