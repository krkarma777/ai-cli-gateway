#!/usr/bin/env python3

import os
import sys

os.environ.pop("OPENAI_LOG", None)

import openai
from openai import OpenAI


class MissingEnvironmentError(Exception):
    def __init__(self, name: str) -> None:
        self.name = name


class InvalidTimeoutError(Exception):
    pass


def require_env(name: str) -> str:
    value = os.environ.get(name)
    if value is None or value == "":
        raise MissingEnvironmentError(name)
    return value


def timeout_seconds() -> float:
    value = os.environ.get("AI_CLI_GATEWAY_TIMEOUT_SECONDS")
    if value is None:
        return 300.0
    if len(value) > 3:
        raise InvalidTimeoutError
    if not value.isascii() or not value.isdecimal():
        raise InvalidTimeoutError
    seconds = int(value)
    if seconds < 1 or seconds > 300:
        raise InvalidTimeoutError
    return float(seconds)


def assert_fields(value: object, expected: set[str]) -> None:
    assert getattr(value, "model_fields_set") == expected


def main() -> None:
    for name in (
        "AI_CLI_GATEWAY_BASE_URL",
        "AI_CLI_GATEWAY_API_KEY",
        "AI_CLI_GATEWAY_MODEL",
    ):
        require_env(name)
    request_timeout = timeout_seconds()

    client = OpenAI(
        **{
            "api_key": require_env("AI_CLI_GATEWAY_API_KEY"),
            "base_url": require_env("AI_CLI_GATEWAY_BASE_URL"),
            "timeout": request_timeout,
            "max_retries": 0,
        }
    )

    models = client.models.list()
    assert len(models.data) == 1
    model = models.data[0]
    assert_fields(model, {"id", "object", "created", "owned_by"})
    assert model.id == "codex-sdk-test"
    assert model.object == "model"
    assert model.created == 0
    assert model.owned_by == "local"

    model_name = require_env("AI_CLI_GATEWAY_MODEL")
    response = client.responses.create(
        model=model_name,
        instructions="SDK contract instruction.",
        input="SDK contract input.",
        text={"format": {"type": "text"}},
        stream=False,
        store=False,
        tools=[],
        tool_choice="none",
    )

    assert isinstance(response._request_id, str)
    assert response._request_id.startswith("req_")
    assert_fields(
        response,
        {
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
        },
    )
    assert isinstance(response.id, str) and response.id.startswith("resp_")
    assert response.object == "response"
    assert isinstance(response.created_at, int) and not isinstance(response.created_at, bool)
    assert isinstance(response.completed_at, int) and not isinstance(response.completed_at, bool)
    assert response.completed_at >= response.created_at
    assert response.status == "completed"
    assert response.background is False
    assert response.error is None
    assert response.incomplete_details is None
    assert response.instructions == "SDK contract instruction."
    assert response.model == model_name
    assert response.parallel_tool_calls is False
    assert response.previous_response_id is None
    assert response.store is False
    assert response.tools == []
    assert response.tool_choice == "none"

    assert_fields(response.text, {"format"})
    assert_fields(response.text.format, {"type"})
    assert response.text.format.type == "text"

    assert len(response.output) == 1
    message = response.output[0]
    assert_fields(message, {"id", "type", "status", "role", "content"})
    assert isinstance(message.id, str) and message.id.startswith("msg_")
    assert message.type == "message"
    assert message.status == "completed"
    assert message.role == "assistant"
    assert len(message.content) == 1

    content = message.content[0]
    assert_fields(content, {"type", "annotations", "text"})
    assert content.type == "output_text"
    assert content.annotations == []
    assert content.text == "SDK_GATEWAY_OK\n"


def api_status(error: openai.APIError) -> str:
    status = getattr(error, "status_code", None)
    if isinstance(status, int) and not isinstance(status, bool) and 400 <= status <= 599:
        return str(status)
    return "unknown"


def run() -> int:
    try:
        main()
    except MissingEnvironmentError as error:
        print(f"sdk_contract_error: missing {error.name}", file=sys.stderr)
        return 1
    except InvalidTimeoutError:
        print("sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS", file=sys.stderr)
        return 1
    except openai.APIError as error:
        print(f"sdk_contract_error: python_api {api_status(error)}", file=sys.stderr)
        return 1
    except Exception:
        print("sdk_contract_error: python_assertion", file=sys.stderr)
        return 1
    print("python_sdk_contract_ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(run())
