"""Stage 3: Agent enrichment with tool-use conversation loop."""

from __future__ import annotations

import json
import logging
import re
from dataclasses import dataclass, field

from .prompts import SYSTEM_PROMPT, build_review_prompt
from .tools import TOOL_DEFINITIONS, ToolExecutor

logger = logging.getLogger(__name__)

MAX_TOOL_ROUNDS = 5


@dataclass
class AgentAssessment:
    cve_id: str
    status: str  # affected|not-affected|under-review
    confidence: str  # high|medium|low
    reasoning: str = ""


@dataclass
class EnrichmentResult:
    assessments: list[AgentAssessment] = field(default_factory=list)
    proposed_mappings: list[dict] = field(default_factory=list)
    input_tokens: int = 0
    output_tokens: int = 0


def create_client(project_id: str, region: str = "global"):
    import anthropic
    return anthropic.AnthropicVertex(region=region, project_id=project_id)


def enrich_batch(
    client,
    package: str,
    upstream_version: str,
    rpm_version: str,
    cve_details: list[dict],
    tool_executor: ToolExecutor,
    prior_decisions: list[dict] | None = None,
    model: str = "claude-haiku-4-5@20251001",
) -> EnrichmentResult:
    """Run agent enrichment on a batch of CVEs with tool-use loop."""
    result = EnrichmentResult()

    if not cve_details:
        return result

    prompt = build_review_prompt(
        package=package,
        upstream_version=upstream_version,
        rpm_version=rpm_version,
        cve_details=cve_details,
        prior_decisions=prior_decisions,
    )

    messages = [{"role": "user", "content": prompt}]

    for round_num in range(MAX_TOOL_ROUNDS + 1):
        response = client.messages.create(
            model=model,
            max_tokens=8192,
            system=SYSTEM_PROMPT,
            tools=TOOL_DEFINITIONS,
            messages=messages,
        )

        result.input_tokens += response.usage.input_tokens
        result.output_tokens += response.usage.output_tokens

        if response.stop_reason == "end_turn":
            text = _extract_text(response)
            result.assessments = _parse_assessments(text)
            result.proposed_mappings = _extract_mappings(text, package)
            break

        if response.stop_reason == "tool_use":
            tool_results = []
            for block in response.content:
                if block.type == "tool_use":
                    logger.info("agent tool call: %s(%s)", block.name, json.dumps(block.input)[:100])
                    tool_output = tool_executor.execute(block.name, block.input)
                    tool_results.append(
                        {"type": "tool_result", "tool_use_id": block.id, "content": tool_output}
                    )

            messages.append({"role": "assistant", "content": response.content})
            messages.append({"role": "user", "content": tool_results})
        else:
            logger.warning("unexpected stop_reason: %s", response.stop_reason)
            text = _extract_text(response)
            result.assessments = _parse_assessments(text)
            break

    if not result.assessments:
        logger.warning("agent returned no assessments for %s", package)

    return result


def _extract_text(response) -> str:
    parts = []
    for block in response.content:
        if hasattr(block, "text"):
            parts.append(block.text)
    return "\n".join(parts)


def _parse_assessments(text: str) -> list[AgentAssessment]:
    # Strip markdown code fences
    text = re.sub(r"```(?:json)?\s*\n?", "", text)

    start = text.find("[")
    if start < 0:
        return []

    end = text.rfind("]") + 1
    if end > start:
        try:
            items = json.loads(text[start:end])
        except json.JSONDecodeError:
            items = None
        if items is not None:
            return _items_to_assessments(items)

    # Salvage truncated JSON — find last complete object
    last_brace = text.rfind("}")
    if last_brace > start:
        try:
            items = json.loads(text[start : last_brace + 1] + "]")
        except json.JSONDecodeError:
            logger.warning("could not parse agent response")
            return []
        return _items_to_assessments(items)

    return []


def _items_to_assessments(items: list) -> list[AgentAssessment]:
    assessments = []
    for item in items:
        if not isinstance(item, dict):
            continue
        cve_id = item.get("cve_id", "")
        status = item.get("status", "under-review")
        if status not in ("affected", "not-affected", "under-review"):
            status = "under-review"

        confidence = item.get("confidence", "low")
        if confidence not in ("high", "medium", "low"):
            confidence = "low"

        assessments.append(
            AgentAssessment(
                cve_id=cve_id,
                status=status,
                confidence=confidence,
                reasoning=item.get("reasoning", ""),
            )
        )

    return assessments


def _extract_mappings(text: str, package: str) -> list[dict]:
    """Extract name mapping proposals from agent reasoning.

    Looks for patterns like:
    - "RPM httpd maps to upstream http_server"
    - "The CPE product for httpd is http_server"
    - "httpd corresponds to Apache HTTP Server (http_server in NVD)"
    """
    mappings = []
    patterns = [
        r"(?:RPM|package)\s+['\"]?(\w[\w.-]*)['\"]?\s+(?:maps?|corresponds?|equals?)\s+(?:to\s+)?(?:upstream\s+)?['\"]?(\w[\w.-]*)['\"]?",
        r"['\"]?(\w[\w.-]*)['\"]?\s+(?:in\s+)?(?:NVD|CPE|vuln)\s+(?:is|=)\s+['\"]?(\w[\w.-]*)['\"]?",
    ]

    for pattern in patterns:
        for match in re.finditer(pattern, text, re.IGNORECASE):
            rpm_name = match.group(1).lower()
            vuln_name = match.group(2).lower()
            if rpm_name != vuln_name:
                mappings.append({"rpm_name": rpm_name, "vuln_name": vuln_name})

    return mappings
