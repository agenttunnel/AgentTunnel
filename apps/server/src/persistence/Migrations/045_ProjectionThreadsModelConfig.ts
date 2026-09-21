import * as Effect from "effect/Effect";
import * as SqlClient from "effect/unstable/sql/SqlClient";

/**
 * Per-thread workflow model config (AgentTunnel roles + auto mode).
 *
 * NULL means "no snapshot": threads created before per-thread configs existed
 * (and threads from clients that do not send one) keep following the provider
 * instance's defaults, which is the behaviour they were created with.
 */
export default Effect.gen(function* () {
  const sql = yield* SqlClient.SqlClient;
  const columns = yield* sql<{ readonly name: string }>`
    PRAGMA table_info(projection_threads)
  `;

  if (!columns.some((column) => column.name === "model_config_json")) {
    yield* sql`
      ALTER TABLE projection_threads
      ADD COLUMN model_config_json TEXT
    `;
  }
});
