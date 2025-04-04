import "./initialize";

import express from "express";
import cors from "cors";
import * as middlewares from "./middlewares";
import api from "./api";
import MessageResponse from "./interfaces/MessageResponse";

require("dotenv").config();

import {
  evalJobCreatorQueueProcessor,
  evalJobDatasetCreatorQueueProcessor,
  evalJobExecutorQueueProcessor,
  evalJobTraceCreatorQueueProcessor,
} from "./queues/evalQueue";
import { batchExportQueueProcessor } from "./queues/batchExportQueue";
import { onShutdown } from "./utils/shutdown";

import helmet from "helmet";
import { cloudUsageMeteringQueueProcessor } from "./queues/cloudUsageMeteringQueue";
import { WorkerManager } from "./queues/workerManager";
import {
  CoreDataS3ExportQueue,
  DataRetentionQueue,
  MeteringDataPostgresExportQueue,
  PostHogIntegrationQueue,
  QueueName,
  logger,
} from "@hanzo/shared/src/server";
import { env } from "./env";
import { ingestionQueueProcessorBuilder } from "./queues/ingestionQueue";
import { BackgroundMigrationManager } from "./backgroundMigrations/backgroundMigrationManager";
import { experimentCreateQueueProcessor } from "./queues/experimentQueue";
import { traceDeleteProcessor } from "./queues/traceDelete";
import { projectDeleteProcessor } from "./queues/projectDelete";
import {
  postHogIntegrationProcessingProcessor,
  postHogIntegrationProcessor,
} from "./queues/postHogIntegrationQueue";
import { coreDataS3ExportProcessor } from "./queues/coreDataS3ExportQueue";
import { meteringDataPostgresExportProcessor } from "./ee/meteringDataPostgresExport/handleMeteringDataPostgresExportJob";
import {
  dataRetentionProcessingProcessor,
  dataRetentionProcessor,
} from "./queues/dataRetentionQueue";
import { batchActionQueueProcessor } from "./queues/batchActionQueue";

const app = express();

app.use(helmet());
app.use(cors());
app.use(express.json());
app.get<{}, MessageResponse>("/", (req, res) => {
  res.json({
    message: "Hanzo Cloud Worker API 🚀",
  });
});

app.use("/api", api);

app.use(middlewares.notFound);
app.use(middlewares.errorHandler);

if (env.HANZO_ENABLE_BACKGROUND_MIGRATIONS === "true") {
  // Will start background migrations without blocking the queue workers
  BackgroundMigrationManager.run().catch((err) => {
    logger.error("Error running background migrations", err);
  });
}

if (env.QUEUE_CONSUMER_TRACE_UPSERT_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.TraceUpsert,
    evalJobTraceCreatorQueueProcessor,
    {
      concurrency: env.HANZO_TRACE_UPSERT_WORKER_CONCURRENCY,
    },
  );
}

if (env.QUEUE_CONSUMER_CREATE_EVAL_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.CreateEvalQueue,
    evalJobCreatorQueueProcessor,
    {
      concurrency: env.HANZO_EVAL_CREATOR_WORKER_CONCURRENCY,
    },
  );
}

if (env.HANZO_S3_CORE_DATA_EXPORT_IS_ENABLED === "true") {
  // Instantiate the queue to trigger scheduled jobs
  CoreDataS3ExportQueue.getInstance();
  WorkerManager.register(
    QueueName.CoreDataS3ExportQueue,
    coreDataS3ExportProcessor,
  );
}

if (env.HANZO_POSTGRES_METERING_DATA_EXPORT_IS_ENABLED === "true") {
  // Instantiate the queue to trigger scheduled jobs
  MeteringDataPostgresExportQueue.getInstance();
  WorkerManager.register(
    QueueName.MeteringDataPostgresExportQueue,
    async (job) => meteringDataPostgresExportProcessor.process(),
    {
      limiter: {
        // Process at most `max` jobs per 30 seconds
        max: 1,
        duration: 30_000,
      },
    },
  );
}

if (env.QUEUE_CONSUMER_TRACE_DELETE_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(QueueName.TraceDelete, traceDeleteProcessor, {
    concurrency: env.HANZO_TRACE_DELETE_CONCURRENCY,
    limiter: {
      // Process at most `max` delete jobs per 15 seconds
      max: env.HANZO_TRACE_DELETE_CONCURRENCY,
      duration: 15_000,
    },
  });
}

if (env.QUEUE_CONSUMER_PROJECT_DELETE_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(QueueName.ProjectDelete, projectDeleteProcessor, {
    concurrency: env.HANZO_PROJECT_DELETE_CONCURRENCY,
    limiter: {
      // Process at most `max` delete jobs per 3 seconds
      max: env.HANZO_PROJECT_DELETE_CONCURRENCY,
      duration: 3_000,
    },
  });
}

if (env.QUEUE_CONSUMER_DATASET_RUN_ITEM_UPSERT_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.DatasetRunItemUpsert,
    evalJobDatasetCreatorQueueProcessor,
    {
      concurrency: env.HANZO_EVAL_CREATOR_WORKER_CONCURRENCY,
    },
  );
}

if (env.QUEUE_CONSUMER_EVAL_EXECUTION_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.EvaluationExecution,
    evalJobExecutorQueueProcessor,
    {
      concurrency: env.HANZO_EVAL_EXECUTION_WORKER_CONCURRENCY,
    },
  );
}

if (env.QUEUE_CONSUMER_BATCH_EXPORT_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(QueueName.BatchExport, batchExportQueueProcessor, {
    concurrency: 1, // only 1 job at a time
    limiter: {
      // execute 1 batch export in 5 seconds to avoid overloading the DB
      max: 1,
      duration: 5_000,
    },
  });
}

if (env.QUEUE_CONSUMER_BATCH_ACTION_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.BatchActionQueue,
    batchActionQueueProcessor,
    {
      concurrency: 1, // only 1 job at a time
      limiter: {
        max: 1,
        duration: 5_000,
      },
    },
  );
}

if (env.QUEUE_CONSUMER_INGESTION_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.IngestionQueue,
    ingestionQueueProcessorBuilder(true), // this might redirect to secondary queue
    {
      concurrency: env.HANZO_INGESTION_QUEUE_PROCESSING_CONCURRENCY,
    },
  );
}

if (env.QUEUE_CONSUMER_INGESTION_SECONDARY_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.IngestionSecondaryQueue,
    ingestionQueueProcessorBuilder(false),
    {
      concurrency:
        env.HANZO_INGESTION_SECONDARY_QUEUE_PROCESSING_CONCURRENCY,
    },
  );
}

if (
  env.QUEUE_CONSUMER_CLOUD_USAGE_METERING_QUEUE_IS_ENABLED === "true" &&
  env.STRIPE_SECRET_KEY
) {
  WorkerManager.register(
    QueueName.CloudUsageMeteringQueue,
    cloudUsageMeteringQueueProcessor,
    {
      concurrency: 1,
      limiter: {
        // Process at most `max` jobs per 30 seconds
        max: 1,
        duration: 30_000,
      },
    },
  );
}

if (env.QUEUE_CONSUMER_EXPERIMENT_CREATE_QUEUE_IS_ENABLED === "true") {
  WorkerManager.register(
    QueueName.ExperimentCreate,
    experimentCreateQueueProcessor,
    {
      concurrency: env.HANZO_EXPERIMENT_CREATOR_WORKER_CONCURRENCY,
    },
  );
}

if (env.QUEUE_CONSUMER_POSTHOG_INTEGRATION_QUEUE_IS_ENABLED === "true") {
  // Instantiate the queue to trigger scheduled jobs
  PostHogIntegrationQueue.getInstance();

  WorkerManager.register(
    QueueName.PostHogIntegrationQueue,
    postHogIntegrationProcessor,
    {
      concurrency: 1,
    },
  );

  WorkerManager.register(
    QueueName.PostHogIntegrationProcessingQueue,
    postHogIntegrationProcessingProcessor,
    {
      concurrency: 1,
    },
  );
}

if (env.QUEUE_CONSUMER_DATA_RETENTION_QUEUE_IS_ENABLED === "true") {
  // Instantiate the queue to trigger scheduled jobs
  DataRetentionQueue.getInstance();

  WorkerManager.register(QueueName.DataRetentionQueue, dataRetentionProcessor, {
    concurrency: 1,
  });

  WorkerManager.register(
    QueueName.DataRetentionProcessingQueue,
    dataRetentionProcessingProcessor,
    {
      concurrency: 1,
    },
  );
}

process.on("SIGINT", () => onShutdown("SIGINT"));
process.on("SIGTERM", () => onShutdown("SIGTERM"));

export default app;
