"""Entry points for vuln-match service components."""

from __future__ import annotations

import logging
import sys

from .config import Config

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
    stream=sys.stderr,
)


def run_matcher() -> None:
    from http.server import HTTPServer
    from .db.pool import create_pool, run_migrations
    from .match.reconciler import make_reconciler
    from factory_workqueue.reconciler import reconciler_http_handler

    cfg = Config.from_env()
    run_migrations(cfg.matchdb_url)

    pool = create_pool(cfg.matchdb_url, min_size=1, max_size=2)
    reconcile = make_reconciler(cfg, pool)

    base_handler = reconciler_http_handler(reconcile)

    class Handler(base_handler):
        def do_GET(self) -> None:
            if self.path in ("/healthz", "/healthz/"):
                self._reply(200, '{"status":"ok"}')
            else:
                self._reply(404, '{"error":"not found"}')

        def _reply(self, status: int, body: str) -> None:
            encoded = body.encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

    host, port = _parse_addr(cfg.listen_addr)
    logger = logging.getLogger(__name__)
    logger.info("matcher serving on %s:%d", host, port)
    server = HTTPServer((host, port), Handler)
    server.serve_forever()


def run_syncer() -> None:
    from .syncer.syncer import run_sync

    cfg = Config.from_env()
    run_sync(cfg)


def run_triage() -> None:
    from .syncer.triage import run_triage

    cfg = Config.from_env()
    run_triage(cfg)


def run_api() -> None:
    from .db.pool import create_pool, run_migrations
    from .api.server import create_server

    cfg = Config.from_env()
    run_migrations(cfg.matchdb_url)

    pool = create_pool(cfg.matchdb_url)
    host, port = _parse_addr(cfg.listen_addr)

    server = create_server(pool, host=host, port=port)
    logging.getLogger(__name__).info("api serving on %s:%d", host, port)
    server.serve_forever()


def _parse_addr(addr: str) -> tuple[str, int]:
    if addr.startswith(":"):
        return "0.0.0.0", int(addr[1:])
    parts = addr.rsplit(":", 1)
    return parts[0], int(parts[1])
