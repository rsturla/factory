"""Environment-based configuration."""

from __future__ import annotations

import os
from dataclasses import dataclass, field


@dataclass(frozen=True)
class Config:
    listen_addr: str = ":8082"
    matchdb_url: str = ""
    catalogdb_url: str = ""
    vulndb_url: str = ""
    receiver_url: str = ""
    match_queue: str = "vuln-match"

    # Agent
    vertex_project: str = ""
    vertex_region: str = "global"
    agent_model: str = "claude-haiku-4-5@20251001"
    agent_batch_size: int = 15
    agent_enabled: bool = True

    # Data paths
    cpe_index_path: str = "data/cpe_index.json"
    rpms_repo_path: str = ""
    vuln_api_url: str = ""

    @classmethod
    def from_env(cls) -> Config:
        return cls(
            listen_addr=os.getenv("LISTEN_ADDR", ":8082"),
            matchdb_url=os.getenv("DATABASE_URL", ""),
            catalogdb_url=os.getenv("CATALOG_DATABASE_URL", ""),
            vulndb_url=os.getenv("VULN_DATABASE_URL", ""),
            receiver_url=os.getenv("RECEIVER_URL", ""),
            match_queue=os.getenv("MATCH_QUEUE", "vuln-match"),
            vertex_project=os.getenv("GOOGLE_CLOUD_PROJECT", ""),
            vertex_region=os.getenv("CLOUD_ML_REGION", "global"),
            agent_model=os.getenv("AGENT_MODEL", "claude-haiku-4-5@20251001"),
            agent_batch_size=int(os.getenv("AGENT_BATCH_SIZE", "15")),
            agent_enabled=os.getenv("AGENT_ENABLED", "true").lower() == "true",
            cpe_index_path=os.getenv("CPE_INDEX_PATH", "data/cpe_index.json"),
            rpms_repo_path=os.getenv("RPMS_REPO_PATH", ""),
            vuln_api_url=os.getenv("VULN_API_URL", ""),
        )
