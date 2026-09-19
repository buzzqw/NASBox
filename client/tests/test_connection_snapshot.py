from __future__ import annotations

import threading
import unittest

from sync_client import rsync_ops
from sync_client.transfer_worker import TransferWorker


class TransferWorkerConnectionTests(unittest.TestCase):
    def _worker(self) -> TransferWorker:
        worker = TransferWorker.__new__(TransferWorker)
        worker._conn = None
        worker._connection_lock = threading.RLock()
        worker._operation_conn = None
        worker._operation_conn_active = False
        worker._deferred_connection = None
        worker._connection_update_pending = False
        return worker

    def test_connection_change_is_deferred_during_operation(self) -> None:
        worker = self._worker()
        original = rsync_ops.NasConnection("nas-a")
        worker.set_connection(original)

        worker._begin_operation_connection()
        replacement = rsync_ops.NasConnection("nas-b")
        worker.set_connection(replacement)

        self.assertIs(worker._conn, original)
        self.assertIs(worker._connection_for_operation(), original)

        worker._end_operation_connection()
        self.assertIs(worker._conn, replacement)

    def test_disconnect_is_deferred_without_losing_current_connection(self) -> None:
        worker = self._worker()
        original = rsync_ops.NasConnection("nas-a")
        worker.set_connection(original)

        worker._begin_operation_connection()
        worker.set_connection(None)

        self.assertIs(worker._connection_for_operation(), original)
        worker._end_operation_connection()
        self.assertIsNone(worker._conn)


if __name__ == "__main__":
    unittest.main()
