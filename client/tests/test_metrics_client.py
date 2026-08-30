from __future__ import annotations

import unittest
from unittest.mock import patch

from sync_client.metrics_worker import MetricsCollector, MetricsProtocolError, parse_metrics
from sync_client import metrics_worker, rsync_ops
from sync_client.config import Config

from tests.support import ClientEnvironment


def metrics_payload(*, cpu_total: int = 100, cpu_idle: int = 40, cpu_iowait: int = 5) -> str:
    return "\n".join([
        "NASBOX_METRICS_V1",
        "COLLECTED_AT_EPOCH=1700000000",
        "UPTIME_SECONDS=1000",
        "LOAD_1=0.50",
        "LOAD_5=0.40",
        "LOAD_15=0.30",
        f"CPU_TOTAL_TICKS={cpu_total}",
        f"CPU_IDLE_TICKS={cpu_idle}",
        f"CPU_IOWAIT_TICKS={cpu_iowait}",
        "MEM_TOTAL_BYTES=1000",
        "MEM_AVAILABLE_BYTES=400",
        "SWAP_TOTAL_BYTES=0",
        "SWAP_FREE_BYTES=0",
        "DISK_TOTAL_BYTES=10000",
        "DISK_USED_BYTES=6000",
        "DISK_AVAILABLE_BYTES=4000",
        "DISK_USAGE_PERCENT=60",
        "DISK_READ_BYTES=2000",
        "DISK_WRITE_BYTES=3000",
        "NET_RX_BYTES=4000",
        "NET_TX_BYTES=5000",
        "NET_RX_PACKETS=40",
        "NET_TX_PACKETS=50",
        "NET_INTERFACE_COUNT=2",
        "",
    ])


class MetricsClientTests(unittest.TestCase):
    def test_parser_validates_extended_metrics(self) -> None:
        parsed = parse_metrics(metrics_payload())
        self.assertEqual(parsed.cpu_total_ticks, 100)
        self.assertEqual(parsed.disk_write_bytes, 3000)

    def test_parser_rejects_missing_cpu_or_disk_counters(self) -> None:
        payload = metrics_payload().replace("CPU_TOTAL_TICKS=100\n", "")
        with self.assertRaises(MetricsProtocolError):
            parse_metrics(payload)

    def test_collector_derives_cpu_and_disk_rates(self) -> None:
        with ClientEnvironment():
            cfg = Config()
            cfg.set("remote_server_script", "/tmp/server.sh", persist=False)
            collector = MetricsCollector()
            conn = rsync_ops.NasConnection("nas")
            responses = [
                (True, metrics_payload(), ""),
                (True, metrics_payload(cpu_total=200, cpu_idle=80, cpu_iowait=15).replace(
                    "DISK_READ_BYTES=2000", "DISK_READ_BYTES=4000"
                ).replace("DISK_WRITE_BYTES=3000", "DISK_WRITE_BYTES=5000"), ""),
            ]
            with patch.object(metrics_worker.rsync_ops, "run_remote_script", side_effect=responses), \
                    patch.object(metrics_worker.time, "monotonic", side_effect=[1.0, 3.0]):
                collector.collect(cfg, conn)
                result = collector.collect(cfg, conn)
        self.assertIsNotNone(result.metrics)
        assert result.metrics is not None
        self.assertEqual(result.metrics.cpu_usage_percent, 60.0)
        self.assertEqual(result.metrics.cpu_iowait_percent, 10.0)
        self.assertEqual(result.metrics.disk_read_rate, 1000.0)
        self.assertEqual(result.metrics.disk_write_rate, 1000.0)


if __name__ == "__main__":
    unittest.main()
