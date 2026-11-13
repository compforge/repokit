import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { RepoKitError, ValidationHistoryError, listValidationRuns } from "../dist/index.js";

const cases = JSON.parse(await readFile(new URL("../../../conformance/errors.json", import.meta.url), "utf8"));
for (const row of cases) test(`shared error: ${row.wire.code}`, () => {
  const {code, message, ...options} = row.wire;
  const cause = new Error("private-cause-detail");
  const error = new RepoKitError(code, message, {...options, cause});
  assert.equal(error.cause, cause);
  assert.equal(error.message, message);
  assert.deepEqual(JSON.parse(JSON.stringify(error)), row.wire);
  assert.equal(error.text(), row.text);
  assert.ok(!error.text().includes(cause.message));
});
// @case A public history failure is catchable as both a RepoKit error and a history error.
test("history errors share the public base", async () => {
  await assert.rejects(listValidationRuns({repository: "/unused", limit: -1}), error => {
    assert.ok(error instanceof RepoKitError);
    assert.ok(error instanceof ValidationHistoryError);
    assert.equal(error.name, "ValidationHistoryError");
    assert.deepEqual(JSON.parse(JSON.stringify(error)), {
      code: "validation.invalid_query", message: error.message, op: "validation.history.list", subject: "/unused"
    });
    return true;
  });
  const cause = new Error("private-cause-detail");
  const error = new ValidationHistoryError("validation.history_failed", "failed", "validation.history.list", "repository", cause);
  assert.equal(error.cause, cause);
});

test("domain errors retain specific catches and share the public base", async () => {
  const {ConfigError, ForgeError, ForgeAuthError, ForgeNotFound, ForgeCoverageError, ForgeRateLimitError} = await import("../dist/index.js");
  const {ObservationStorageError} = await import("../dist/state/store.js");
  const {PullRequestStatusUnavailable} = await import("../dist/forge/observation/service.js");
  const key = {repository:{forge:{name:"github"},path:"acme/repo"},number:1};
  const errors = [new ConfigError("invalid"), new ForgeError("failed"), new ForgeAuthError("auth"),
    new ForgeNotFound("absent"), new ForgeCoverageError("incomplete"), new ForgeRateLimitError("limited",1234),
    new ObservationStorageError("storage"), new PullRequestStatusUnavailable(key)];
  for (const error of errors) {
    assert.ok(error instanceof RepoKitError, error.constructor.name);
    assert.ok(error instanceof Error);
    assert.equal(JSON.parse(JSON.stringify(error)).message,error.message);
  }
  assert.ok(errors[2] instanceof ForgeError);
  assert.equal(errors[5].retryAfterMs,1234);
  const cause = new Error("storage cause");
  assert.equal(new ObservationStorageError("failed",{cause}).cause,cause);
});
