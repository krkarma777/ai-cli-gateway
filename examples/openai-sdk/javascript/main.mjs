#!/usr/bin/env node

import assert from "node:assert/strict";
import OpenAI from "openai";

class MissingEnvironmentError extends Error {
  constructor(name) {
    super();
    this.name = name;
  }
}

class InvalidTimeoutError extends Error {}

function requireEnv(name) {
  const value = process.env[name];
  if (value === undefined || value === "") {
    throw new MissingEnvironmentError(name);
  }
  return value;
}

function timeoutMilliseconds() {
  const value = process.env.AI_CLI_GATEWAY_TIMEOUT_SECONDS;
  if (value === undefined) {
    return 300_000;
  }
  if (!/^[0-9]+$/.test(value)) {
    throw new InvalidTimeoutError();
  }
  const seconds = Number(value);
  if (!Number.isSafeInteger(seconds) || seconds < 1 || seconds > 300) {
    throw new InvalidTimeoutError();
  }
  return seconds * 1_000;
}

function assertFields(value, expected) {
  assert.deepEqual(Object.keys(value).sort(), [...expected].sort());
}

async function main() {
  for (const name of [
    "AI_CLI_GATEWAY_BASE_URL",
    "AI_CLI_GATEWAY_API_KEY",
    "AI_CLI_GATEWAY_MODEL",
  ]) {
    requireEnv(name);
  }
  const requestTimeout = timeoutMilliseconds();

  const client = new OpenAI({
    apiKey: requireEnv("AI_CLI_GATEWAY_API_KEY"),
    baseURL: requireEnv("AI_CLI_GATEWAY_BASE_URL"),
    timeout: requestTimeout,
    maxRetries: 0,
    logLevel: "off",
  });

  const models = await client.models.list();
  assert.equal(models.data.length, 1);
  const model = models.data[0];
  assertFields(model, ["id", "object", "created", "owned_by"]);
  assert.equal(model.id, "codex-sdk-test");
  assert.equal(model.object, "model");
  assert.equal(model.created, 0);
  assert.equal(model.owned_by, "local");

  const modelName = requireEnv("AI_CLI_GATEWAY_MODEL");
  const response = await client.responses.create({
    model: modelName,
    instructions: "SDK contract instruction.",
    input: "SDK contract input.",
    text: { format: { type: "text" } },
    stream: false,
    store: false,
    tools: [],
    tool_choice: "none",
  });

  assert.equal(typeof response._request_id, "string");
  assert.match(response._request_id, /^req_/);
  assertFields(response, [
    "id",
    "object",
    "created_at",
    "completed_at",
    "status",
    "background",
    "error",
    "incomplete_details",
    "instructions",
    "model",
    "output",
    "parallel_tool_calls",
    "previous_response_id",
    "store",
    "text",
    "tools",
    "tool_choice",
  ]);
  assert.equal(typeof response.id, "string");
  assert.match(response.id, /^resp_/);
  assert.equal(response.object, "response");
  assert.equal(Number.isSafeInteger(response.created_at), true);
  assert.equal(Number.isSafeInteger(response.completed_at), true);
  assert.equal(response.completed_at >= response.created_at, true);
  assert.equal(response.status, "completed");
  assert.equal(response.background, false);
  assert.equal(response.error, null);
  assert.equal(response.incomplete_details, null);
  assert.equal(response.instructions, "SDK contract instruction.");
  assert.equal(response.model, modelName);
  assert.equal(response.parallel_tool_calls, false);
  assert.equal(response.previous_response_id, null);
  assert.equal(response.store, false);
  assert.deepEqual(response.tools, []);
  assert.equal(response.tool_choice, "none");

  assertFields(response.text, ["format"]);
  assertFields(response.text.format, ["type"]);
  assert.equal(response.text.format.type, "text");

  assert.equal(response.output.length, 1);
  const message = response.output[0];
  assertFields(message, ["id", "type", "status", "role", "content"]);
  assert.equal(typeof message.id, "string");
  assert.match(message.id, /^msg_/);
  assert.equal(message.type, "message");
  assert.equal(message.status, "completed");
  assert.equal(message.role, "assistant");
  assert.equal(message.content.length, 1);

  const content = message.content[0];
  assertFields(content, ["type", "annotations", "text"]);
  assert.equal(content.type, "output_text");
  assert.deepEqual(content.annotations, []);
  assert.equal(content.text, "SDK_GATEWAY_OK\n");
}

function apiStatus(error) {
  if (Number.isSafeInteger(error.status) && error.status >= 400 && error.status <= 599) {
    return String(error.status);
  }
  return "unknown";
}

try {
  await main();
  console.log("javascript_sdk_contract_ok");
} catch (error) {
  if (error instanceof MissingEnvironmentError) {
    console.error(`sdk_contract_error: missing ${error.name}`);
  } else if (error instanceof InvalidTimeoutError) {
    console.error("sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS");
  } else if (error instanceof OpenAI.APIError) {
    console.error(`sdk_contract_error: javascript_api ${apiStatus(error)}`);
  } else {
    console.error("sdk_contract_error: javascript_assertion");
  }
  process.exitCode = 1;
}
