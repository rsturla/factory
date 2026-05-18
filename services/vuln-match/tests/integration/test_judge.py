"""LLM-as-judge: validate agent CVE assessments against known-correct answers.

Requires GOOGLE_CLOUD_PROJECT env var for Vertex AI access.
Skip in CI unless explicitly enabled.
"""

import json
import os

import pytest

VERTEX_PROJECT = os.getenv("GOOGLE_CLOUD_PROJECT", "")

pytestmark = pytest.mark.skipif(
    not VERTEX_PROJECT,
    reason="GOOGLE_CLOUD_PROJECT not set — skipping LLM integration tests",
)

# Golden test set: known-correct CVE/package assessments
# Built from prototype validation against Grype findings
GOLDEN_SET = [
    {
        "cve": "CVE-2024-6119",
        "package": "openssl",
        "version": "3.5.6",
        "expected": "not-affected",
        "reason": "Fixed in OpenSSL 3.3.2, 3.2.3, 3.1.7. Version 3.5.6 is well past the fix.",
    },
    {
        "cve": "CVE-2024-9143",
        "package": "openssl",
        "version": "3.5.6",
        "expected": "not-affected",
        "reason": "Fixed in OpenSSL 3.4.0, 3.3.3. Version 3.5.6 > 3.4.0.",
    },
    {
        "cve": "CVE-2024-12797",
        "package": "openssl",
        "version": "3.5.6",
        "expected": "not-affected",
        "reason": "Fixed in 3.4.1, 3.3.3. Version 3.5.6 is past the fix.",
    },
    {
        "cve": "CVE-2025-26423",
        "package": "vim",
        "version": "9.1.1",
        "expected": "under-review",
        "reason": "Need to verify if distro patches address this.",
    },
    {
        "cve": "CVE-2023-45853",
        "package": "zlib",
        "version": "1.3.1",
        "expected": "not-affected",
        "reason": "Fixed in 1.3.1. Version 1.3.1 equals the fix version.",
    },
]


def _create_client():
    import anthropic
    return anthropic.AnthropicVertex(
        region=os.getenv("CLOUD_ML_REGION", "global"),
        project_id=VERTEX_PROJECT,
    )


def _assess_single(client, case: dict) -> dict:
    """Ask the agent to assess a single CVE."""
    from vuln_match.agent.prompts import SYSTEM_PROMPT

    prompt = f"""Review this single CVE for the given package:

Package: {case["package"]} version {case["version"]}
CVE: {case["cve"]}

Determine if this CVE affects version {case["version"]}.

Respond with ONLY a JSON object:
{{"cve_id": "{case["cve"]}", "status": "affected|not-affected|under-review", "confidence": "high|medium|low", "reasoning": "brief explanation"}}"""

    response = client.messages.create(
        model="claude-haiku-4-5@20251001",
        max_tokens=1024,
        system=SYSTEM_PROMPT,
        messages=[{"role": "user", "content": prompt}],
    )

    text = response.content[0].text
    start = text.find("{")
    end = text.rfind("}") + 1
    if start >= 0 and end > start:
        return json.loads(text[start:end])
    return {"status": "error", "reasoning": "could not parse response"}


def _judge_result(client, case: dict, result: dict) -> dict:
    """Use a judge LLM to evaluate if the agent's answer is acceptable."""
    prompt = f"""You are evaluating an AI security analyst's CVE assessment.

KNOWN CORRECT ANSWER:
- CVE: {case["cve"]}
- Package: {case["package"]} v{case["version"]}
- Expected status: {case["expected"]}
- Reason: {case["reason"]}

AGENT'S ANSWER:
- Status: {result.get("status", "unknown")}
- Reasoning: {result.get("reasoning", "none")}

An answer is ACCEPTABLE if:
1. It exactly matches the expected status, OR
2. It is MORE CONSERVATIVE (e.g., "under-review" when expected "not-affected" is OK)
3. The reasoning is factually sound even if the conclusion differs

An answer is UNACCEPTABLE if:
1. It dismisses a genuinely affected CVE as "not-affected" (false negative)
2. The reasoning contains factual errors about version numbers

Respond with ONLY a JSON object:
{{"acceptable": true|false, "explanation": "brief explanation"}}"""

    response = client.messages.create(
        model="claude-haiku-4-5@20251001",
        max_tokens=512,
        messages=[{"role": "user", "content": prompt}],
    )

    text = response.content[0].text
    start = text.find("{")
    end = text.rfind("}") + 1
    if start >= 0 and end > start:
        return json.loads(text[start:end])
    return {"acceptable": False, "explanation": "could not parse judge response"}


class TestAgentAccuracy:
    """Validates agent CVE assessment accuracy against golden test set.

    Threshold: 90% of assessments must be acceptable.
    Conservative errors (under-review instead of not-affected) are acceptable.
    False negatives (not-affected for genuinely affected) are NOT acceptable.
    """

    def test_golden_set(self):
        client = _create_client()
        correct = 0
        results = []

        for case in GOLDEN_SET:
            result = _assess_single(client, case)
            agent_status = result.get("status", "error")

            if agent_status == case["expected"]:
                correct += 1
                results.append({"case": case["cve"], "match": True})
            else:
                # Use judge to evaluate
                judge = _judge_result(client, case, result)
                if judge.get("acceptable", False):
                    correct += 1
                    results.append({
                        "case": case["cve"],
                        "match": False,
                        "acceptable": True,
                        "agent": agent_status,
                        "expected": case["expected"],
                    })
                else:
                    results.append({
                        "case": case["cve"],
                        "match": False,
                        "acceptable": False,
                        "agent": agent_status,
                        "expected": case["expected"],
                        "judge": judge.get("explanation", ""),
                    })

        accuracy = correct / len(GOLDEN_SET)
        print(f"\nAgent accuracy: {accuracy:.0%} ({correct}/{len(GOLDEN_SET)})")
        for r in results:
            status = "PASS" if r.get("match") or r.get("acceptable") else "FAIL"
            print(f"  {status}: {r['case']} — agent={r.get('agent', 'match')}, expected={r.get('expected', 'match')}")

        assert accuracy >= 0.90, f"Agent accuracy {accuracy:.0%} below 90% threshold"
