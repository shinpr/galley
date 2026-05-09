#!/usr/bin/env python3
import json
import os
import sys


RESULT_TEMPLATE = """{
  "status": "completed",
  "summary": "One concise summary of the completed work.",
  "files_modified": ["path/to/file.ext"],
  "acceptance_criteria": [
    {
      "id": "AC1",
      "status": "satisfied",
      "evidence": ["Concrete evidence from changed files or verification output."],
      "notes": "Why this criterion is satisfied."
    }
  ],
  "verification": [
    {
      "command": "command that was run",
      "status": "passed",
      "reason": "Why this status is correct.",
      "output_excerpt": "Relevant output excerpt."
    }
  ],
  "decisions": [],
  "risks": []
}"""


def block(reason):
    print(json.dumps({
        "decision": "block",
        "reason": reason,
        "systemMessage": "Return the Galley executor result as valid JSON.",
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


def validate_result(result):
    require_object(result, "result")
    if result.get("status") not in {"completed", "completed_with_risks", "hard_stop"}:
        raise ValueError("status must be completed, completed_with_risks, or hard_stop")
    for field in ["summary", "files_modified", "acceptance_criteria", "verification", "decisions", "risks"]:
        if field not in result:
            raise ValueError(f"{field} is required")
    if not isinstance(result["summary"], str) or not result["summary"].strip():
        raise ValueError("summary must be a non-empty string")
    require_array(result["files_modified"], "files_modified")
    require_array(result["acceptance_criteria"], "acceptance_criteria")
    require_array(result["verification"], "verification")
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

    try:
        result = json.loads(text)
        validate_result(result)
    except Exception as err:
        block(
            "The final assistant response must be exactly one JSON object matching the Galley executor result contract.\n\n"
            f"Validation error: {err}\n\n"
            "Respond again with only the JSON object. Use this shape and fill it with the actual task evidence:\n\n"
            f"{RESULT_TEMPLATE}"
        )


if __name__ == "__main__":
    main()
