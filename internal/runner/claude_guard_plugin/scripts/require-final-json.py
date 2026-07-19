#!/usr/bin/env python3
import json
import os
import posixpath
import re
import sys


RESULT_TEMPLATE = """{
  "status": "completed",
  "summary": "One concise summary of the completed work.",
  "files_modified": ["path/to/file.ext"],
  "acceptance_criteria": [{"id": "AC1", "status": "satisfied", "evidence": ["Concrete evidence from changed files or verification output."], "notes": "Why this criterion is satisfied."}],
  "verification": [{"command": "command that was run", "status": "passed", "reason": "Why this status is correct.", "output_excerpt": "Relevant output excerpt."}],
  "scope_expansions": [],
  "decisions": [],
  "risks": []
}"""

SUPERVISOR_TEMPLATE = """{
  "status": "accepted",
  "summary": "One concise review summary.",
  "acceptance_passes": ["AC1"],
  "quality_passes": ["configured-dimension-id"],
  "findings": [],
  "discussion_items": []
}"""

CREATOR_TEMPLATE = """{
  "outputs": [
    {"ac_id": "AC1", "path": "tests/example.integration.test.ts", "kind": "integration", "purpose": "Verify the user-visible behavior required by AC1.", "satisfies": "AC1 observable outcome covered by this skeleton.", "integration_point": "Executor completes this skeleton while implementing the feature.", "implementation_required": true}
  ],
  "no_skeletons": []
}"""

SETUP_EXECUTOR_TEMPLATE = """{
  "status": "ready",
  "commands": [
    {"run": "setup command that was run", "why": "Why this command is part of setup", "source": "environment_commands", "exit_code": 0, "stdout_excerpt": "Short relevant output excerpt", "stderr_excerpt": ""}
  ],
  "successful_commands": [
    {"run": "setup command that should be persisted", "why": "Why this command makes the worktree ready"}
  ],
  "inspected_files": ["package.json"],
  "readiness_evidence": "The setup command and a representative required check passed.",
  "source": "environment_commands"
}"""


def block(reason):
    print(json.dumps({
        "decision": "block",
        "reason": reason,
        "systemMessage": "Return the Galley result as valid JSON.",
    }))


def assistant_text_from_transcript(transcript_path):
    if not transcript_path or not os.path.isfile(transcript_path):
        return ""
    try:
        with open(transcript_path, "r", encoding="utf-8") as file:
            lines = file.readlines()[-100:]
    except (OSError, UnicodeDecodeError):
        return ""

    entries = []
    for line in lines:
        try:
            entries.append(json.loads(line))
        except (json.JSONDecodeError, TypeError):
            continue

    for entry in reversed(entries):
        entry_type = entry.get("type", "")
        if entry_type == "user":
            break
        if entry_type != "assistant":
            continue
        texts = []
        for item in entry.get("message", {}).get("content", []):
            if isinstance(item, dict) and item.get("type") == "text" and item.get("text"):
                texts.append(item["text"])
        if texts:
            return "\n".join(texts)
    return ""


def final_text(hook_input):
    message = hook_input.get("last_assistant_message")
    if isinstance(message, str) and message.strip():
        return message
    return assistant_text_from_transcript(hook_input.get("transcript_path"))


def require_object(value, name):
    if not isinstance(value, dict):
        raise ValueError(f"{name} must be an object")


def require_array(value, name):
    if not isinstance(value, list):
        raise ValueError(f"{name} must be an array")


def require_clean_relative_path(value, name):
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} must be a non-empty string")
    clean = posixpath.normpath(value)
    if (
        value.startswith("/")
        or "\\" in value
        or "//" in value
        or value.endswith("/")
        or re.match(r"^[A-Za-z]:", value)
        or clean != value
        or clean == "."
        or clean == ".."
        or clean.startswith("../")
    ):
        raise ValueError(f"{name} must be a clean relative path")


def validate_result(result):
    require_object(result, "result")
    if result.get("status") not in {"completed", "completed_with_risks", "hard_stop"}:
        raise ValueError("status must be completed, completed_with_risks, or hard_stop")
    for field in ["summary", "files_modified", "acceptance_criteria", "verification", "scope_expansions", "decisions", "risks"]:
        if field not in result:
            raise ValueError(f"{field} is required")
    if not isinstance(result["summary"], str) or not result["summary"].strip():
        raise ValueError("summary must be a non-empty string")
    require_array(result["files_modified"], "files_modified")
    require_array(result["acceptance_criteria"], "acceptance_criteria")
    require_array(result["verification"], "verification")
    require_array(result["scope_expansions"], "scope_expansions")
    require_array(result["decisions"], "decisions")
    require_array(result["risks"], "risks")

    for index, criterion in enumerate(result["acceptance_criteria"]):
        require_object(criterion, f"acceptance_criteria[{index}]")
        for field in ["id", "status", "evidence", "notes"]:
            if field not in criterion:
                raise ValueError(f"acceptance_criteria[{index}].{field} is required")
        if criterion.get("status") not in {"satisfied", "partially_satisfied", "not_satisfied"}:
            raise ValueError(f"acceptance_criteria[{index}].status is invalid")
        require_array(criterion["evidence"], f"acceptance_criteria[{index}].evidence")

    for index, verification in enumerate(result["verification"]):
        require_object(verification, f"verification[{index}]")
        for field in ["command", "status", "reason", "output_excerpt"]:
            if field not in verification:
                raise ValueError(f"verification[{index}].{field} is required")
        if verification.get("status") not in {"passed", "failed", "skipped"}:
            raise ValueError(f"verification[{index}].status is invalid")

    for index, expansion in enumerate(result["scope_expansions"]):
        require_object(expansion, f"scope_expansions[{index}]")
        for field in ["path", "reason", "linked_requirement", "minimality"]:
            if field not in expansion:
                raise ValueError(f"scope_expansions[{index}].{field} is required")
            if not isinstance(expansion[field], str) or not expansion[field].strip():
                raise ValueError(f"scope_expansions[{index}].{field} must be a non-empty string")
        require_clean_relative_path(expansion["path"], f"scope_expansions[{index}].path")

    for index, decision in enumerate(result["decisions"]):
        require_object(decision, f"decisions[{index}]")
        for field in ["question", "chosen", "rationale", "reversibility", "needs_human_review"]:
            if field not in decision:
                raise ValueError(f"decisions[{index}].{field} is required")
        if decision.get("reversibility") not in {"high", "medium", "low"}:
            raise ValueError(f"decisions[{index}].reversibility is invalid")

    for index, risk in enumerate(result["risks"]):
        require_object(risk, f"risks[{index}]")
        for field in ["type", "detail", "mitigation", "needs_human_review"]:
            if field not in risk:
                raise ValueError(f"risks[{index}].{field} is required")
        if risk.get("type") not in {"ambiguous_requirement", "partial_verification", "external_dependency", "technical_debt", "other"}:
            raise ValueError(f"risks[{index}].type is invalid")

    if result["status"] == "hard_stop":
        hard_stop = result.get("hard_stop")
        require_object(hard_stop, "hard_stop")
        for field in ["reason", "attempted", "needed_to_continue"]:
            if field not in hard_stop:
                raise ValueError(f"hard_stop.{field} is required")
        require_array(hard_stop["attempted"], "hard_stop.attempted")
        require_array(hard_stop["needed_to_continue"], "hard_stop.needed_to_continue")
    elif "hard_stop" in result:
        raise ValueError("hard_stop is only valid when status is hard_stop")


def validate_supervisor_verdict(verdict):
    require_object(verdict, "verdict")
    if verdict.get("status") not in {"accepted", "needs_revision", "needs_supervisor_review", "hard_stop"}:
        raise ValueError("status must be accepted, needs_revision, needs_supervisor_review, or hard_stop")
    required = [
        "status",
        "summary",
        "acceptance_passes",
        "quality_passes",
        "findings",
        "discussion_items",
    ]
    for field in required:
        if field not in verdict:
            raise ValueError(f"{field} is required")
    if not isinstance(verdict["summary"], str) or not verdict["summary"].strip():
        raise ValueError("summary must be a non-empty string")
    for field in ["acceptance_passes", "quality_passes", "findings", "discussion_items"]:
        require_array(verdict[field], field)
        for index, value in enumerate(verdict[field]):
            if not isinstance(value, str) or not value.strip():
                raise ValueError(f"{field}[{index}] must be a non-empty string")


def validate_creator_manifest(manifest):
    require_object(manifest, "manifest")
    for field in ["outputs", "no_skeletons"]:
        if field not in manifest:
            raise ValueError(f"{field} is required")
    require_array(manifest["outputs"], "outputs")
    require_array(manifest["no_skeletons"], "no_skeletons")

    for index, output in enumerate(manifest["outputs"]):
        require_object(output, f"outputs[{index}]")
        required = [
            "ac_id",
            "path",
            "kind",
            "purpose",
            "satisfies",
            "integration_point",
            "implementation_required",
        ]
        for field in required:
            if field not in output:
                raise ValueError(f"outputs[{index}].{field} is required")
        for field in ["ac_id", "path", "kind", "purpose", "satisfies", "integration_point"]:
            if not isinstance(output[field], str) or not output[field].strip():
                raise ValueError(f"outputs[{index}].{field} must be a non-empty string")
        if not isinstance(output["implementation_required"], bool):
            raise ValueError(f"outputs[{index}].implementation_required must be a boolean")

    for index, item in enumerate(manifest["no_skeletons"]):
        require_object(item, f"no_skeletons[{index}]")
        for field in ["ac_id", "reason"]:
            if field not in item:
                raise ValueError(f"no_skeletons[{index}].{field} is required")
            if not isinstance(item[field], str) or not item[field].strip():
                raise ValueError(f"no_skeletons[{index}].{field} must be a non-empty string")


def validate_setup_executor_result(result):
    require_object(result, "setup_result")
    status = result.get("status")
    if status not in {"ready", "failed"}:
        raise ValueError("status must be ready or failed")
    if "commands" not in result:
        raise ValueError("commands is required")
    require_array(result["commands"], "commands")
    if len(result["commands"]) > 50:
        raise ValueError("commands must contain at most 50 entries")
    successful_command_runs = set()
    for index, command in enumerate(result["commands"]):
        require_object(command, f"commands[{index}]")
        for field in ["run", "source", "exit_code"]:
            if field not in command:
                raise ValueError(f"commands[{index}].{field} is required")
        if not isinstance(command["run"], str) or not command["run"].strip():
            raise ValueError(f"commands[{index}].run must be a non-empty string")
        if len(command["run"]) > 4096:
            raise ValueError(f"commands[{index}].run is too long")
        if command.get("source") not in {"environment_setup", "environment_commands", "discovered", "readiness_check"}:
            raise ValueError(f"commands[{index}].source is invalid")
        if not isinstance(command["exit_code"], int):
            raise ValueError(f"commands[{index}].exit_code must be an integer")
        if command["exit_code"] == 0 and command.get("source") != "readiness_check":
            successful_command_runs.add(command["run"].strip())
    if "successful_commands" in result:
        require_array(result["successful_commands"], "successful_commands")
        if len(result["successful_commands"]) > 50:
            raise ValueError("successful_commands must contain at most 50 entries")
        for index, command in enumerate(result["successful_commands"]):
            require_object(command, f"successful_commands[{index}]")
            if not isinstance(command.get("run"), str) or not command["run"].strip():
                raise ValueError(f"successful_commands[{index}].run must be a non-empty string")
            if len(command["run"]) > 4096:
                raise ValueError(f"successful_commands[{index}].run is too long")
            if command["run"].strip() not in successful_command_runs:
                raise ValueError(f"successful_commands[{index}].run must match a setup commands[].run that exited 0")
    if "source" in result and result.get("source") not in {"environment_setup", "environment_commands", "discovered"}:
        raise ValueError("source is invalid")
    if status == "ready":
        if result.get("source") not in {"environment_setup", "environment_commands", "discovered"}:
            raise ValueError("source is required for ready setup results")
        if "successful_commands" not in result or not result["successful_commands"]:
            raise ValueError("successful_commands is required for ready setup results")
        if not isinstance(result.get("readiness_evidence"), str) or not result["readiness_evidence"].strip():
            raise ValueError("readiness_evidence is required for ready setup results")
    if status == "failed":
        if not isinstance(result.get("error"), str) or not result["error"].strip():
            raise ValueError("error is required for failed setup results")
        if not isinstance(result.get("repair_guidance"), str) or not result["repair_guidance"].strip():
            raise ValueError("repair_guidance is required for failed setup results")


def main():
    try:
        hook_input = json.load(sys.stdin)
    except (json.JSONDecodeError, ValueError) as err:
        block(
            "The final response guard could not read the Stop hook input.\n\n"
            f"Hook input error: {err}\n\n"
            "Respond again with only the Galley executor JSON object."
        )
        return

    text = final_text(hook_input).strip()
    if not text:
        block(
            "The final assistant response was empty or unavailable to the Stop hook.\n\n"
            "Respond again with only the Galley executor JSON object."
        )
        return

    mode = os.environ.get("GALLEY_CLAUDE_GUARD_MODE", "executor")
    try:
        result = json.loads(text)
        if mode == "supervisor":
            validate_supervisor_verdict(result)
        elif mode == "acceptance_skeleton_creator":
            validate_creator_manifest(result)
        elif mode == "setup_executor":
            validate_setup_executor_result(result)
        else:
            validate_result(result)
    except (json.JSONDecodeError, ValueError, TypeError) as err:
        if mode == "supervisor":
            block(
                "The final assistant response must be exactly one JSON object matching the Galley supervisor verdict contract.\n\n"
                f"Validation error: {err}\n\n"
                "Respond again with only the JSON object. Use this shape and fill it with the actual review evidence:\n\n"
                f"{SUPERVISOR_TEMPLATE}"
            )
        elif mode == "acceptance_skeleton_creator":
            block(
                "The final assistant response must be exactly one JSON object matching the Galley acceptance skeleton manifest contract.\n\n"
                f"Validation error: {err}\n\n"
                "Respond again with only the JSON object. Use this shape and fill it with the actual generated skeleton files:\n\n"
                f"{CREATOR_TEMPLATE}"
            )
        elif mode == "setup_executor":
            block(
                "The final assistant response must be exactly one JSON object matching the Galley setup executor result contract.\n\n"
                f"Validation error: {err}\n\n"
                "Respond again with only the JSON object. Use this shape and fill it with the actual setup evidence:\n\n"
                f"{SETUP_EXECUTOR_TEMPLATE}"
            )
        else:
            block(
                "The final assistant response must be exactly one JSON object matching the Galley executor result contract.\n\n"
                f"Validation error: {err}\n\n"
                "Respond again with only the JSON object. Use this shape and fill it with the actual task evidence:\n\n"
                f"{RESULT_TEMPLATE}"
            )


if __name__ == "__main__":
    main()
