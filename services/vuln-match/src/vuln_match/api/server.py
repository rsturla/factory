"""Advisory query and review API."""

from __future__ import annotations

import json
import logging
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any
from urllib.parse import parse_qs, urlparse

from ..store.postgres import AdvisoryStore

logger = logging.getLogger(__name__)


def create_server(pool: Any, host: str = "0.0.0.0", port: int = 8080) -> HTTPServer:
    store = AdvisoryStore(pool)

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            parsed = urlparse(self.path)
            path = parsed.path.rstrip("/")
            params = parse_qs(parsed.query)

            if path == "/healthz":
                self._json(200, {"status": "ok", "db": store.ping()})
            elif path == "/v1/feed/osv.json":
                self._serve_osv_feed(store, params)
            elif path.startswith("/v1/advisories/") and path.count("/") == 4:
                parts = path.split("/")
                pkg, cve = parts[3], parts[4]
                self._get_advisory(store, pkg, cve)
            elif path.startswith("/v1/advisories/") and path.count("/") == 3:
                pkg = path.split("/")[3]
                self._list_advisories(store, source_package=pkg, params=params)
            elif path == "/v1/advisories":
                self._list_advisories(store, params=params)
            elif path == "/v1/mappings":
                self._list_mappings(store)
            else:
                self._json(404, {"error": "not found"})

        def do_POST(self) -> None:
            parsed = urlparse(self.path)
            path = parsed.path.rstrip("/")

            try:
                length = int(self.headers.get("Content-Length", 0))
            except (ValueError, TypeError):
                self._json(400, {"error": "invalid Content-Length"})
                return
            if length > 65536:
                self._json(413, {"error": "request too large"})
                return
            try:
                body = json.loads(self.rfile.read(length)) if length else {}
            except (json.JSONDecodeError, UnicodeDecodeError):
                self._json(400, {"error": "invalid JSON body"})
                return

            if path.startswith("/v1/advisories/") and path.endswith("/review"):
                parts = path.split("/")
                if len(parts) == 6:
                    pkg, cve = parts[3], parts[4]
                    self._review_advisory(store, pkg, cve, body)
                else:
                    self._json(400, {"error": "invalid path"})
            elif path.startswith("/v1/mappings/") and path.endswith("/review"):
                rpm_name = path.split("/")[3]
                self._review_mapping(store, rpm_name, body)
            else:
                self._json(404, {"error": "not found"})

        def _get_advisory(self, store: AdvisoryStore, pkg: str, cve: str) -> None:
            adv = store.get_advisory(pkg, cve)
            if not adv:
                self._json(404, {"error": "not found"})
                return
            self._json(200, _advisory_to_dict(adv))

        def _list_advisories(
            self, store: AdvisoryStore, source_package: str | None = None, params: dict | None = None
        ) -> None:
            params = params or {}
            status = params.get("status", [None])[0]
            try:
                limit = min(max(int(params.get("limit", ["100"])[0]), 1), 10000)
                offset = max(int(params.get("offset", ["0"])[0]), 0)
            except (ValueError, TypeError):
                self._json(400, {"error": "invalid limit or offset"})
                return

            advisories = store.list_advisories(
                source_package=source_package, status=status, limit=limit, offset=offset
            )
            total = store.count_advisories(source_package=source_package, status=status)

            self._json(
                200,
                {
                    "advisories": [_advisory_to_dict(a) for a in advisories],
                    "total": total,
                    "limit": limit,
                    "offset": offset,
                },
            )

        _VALID_STATUSES = {"affected", "not-affected", "fixed", "under-review", "detected"}

        def _review_advisory(self, store: AdvisoryStore, pkg: str, cve: str, body: dict) -> None:
            status = body.get("status", "")
            reviewed_by = body.get("reviewed_by", "")
            if not status or not reviewed_by:
                self._json(400, {"error": "status and reviewed_by required"})
                return
            if status not in self._VALID_STATUSES:
                self._json(400, {"error": f"invalid status, must be one of: {self._VALID_STATUSES}"})
                return

            ok = store.review_advisory(
                source_package=pkg,
                vuln_id=cve,
                status=status,
                reviewed_by=reviewed_by,
                distro_fixed_version=body.get("distro_fixed_version", ""),
                notes=body.get("notes", ""),
            )
            if ok:
                self._json(200, {"ok": True})
            else:
                self._json(404, {"error": "advisory not found"})

        def _list_mappings(self, store: AdvisoryStore) -> None:
            mappings = store.get_all_mappings()
            self._json(200, {"mappings": mappings})

        def _review_mapping(self, store: AdvisoryStore, rpm_name: str, body: dict) -> None:
            reviewed = body.get("reviewed", True)
            ok = store.review_mapping(rpm_name, reviewed=reviewed)
            if ok:
                self._json(200, {"ok": True})
            else:
                self._json(404, {"error": "mapping not found"})

        def _serve_osv_feed(self, store: AdvisoryStore, params: dict) -> None:
            from ..feed.osv import generate_osv_feed

            advisories = store.list_advisories(status="affected", limit=10000)
            fixed = store.list_advisories(status="fixed", limit=10000)
            feed = generate_osv_feed(advisories + fixed)
            self._json(200, feed)

        def _json(self, status: int, data: Any) -> None:
            body = json.dumps(data, default=str).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, format: str, *args: Any) -> None:
            logger.debug(format, *args)

    return HTTPServer((host, port), Handler)


def _advisory_to_dict(adv) -> dict:
    return {
        "id": adv.id,
        "source_package": adv.source_package,
        "vuln_id": adv.vuln_id,
        "status": adv.status,
        "confidence": adv.confidence,
        "match_type": adv.match_type,
        "upstream_version": adv.upstream_version,
        "rpm_version": adv.rpm_version,
        "upstream_fixed_version": adv.upstream_fixed_version,
        "distro_fixed_version": adv.distro_fixed_version,
        "severity": adv.severity,
        "cvss_score": adv.cvss_score,
        "epss_score": adv.epss_score,
        "in_kev": adv.in_kev,
        "flags": adv.flags,
        "notes": adv.notes,
        "agent_reasoning": adv.agent_reasoning,
        "reviewed_by": adv.reviewed_by,
        "reviewed_at": str(adv.reviewed_at) if adv.reviewed_at else None,
        "created_at": str(adv.created_at) if adv.created_at else None,
        "updated_at": str(adv.updated_at) if adv.updated_at else None,
    }
