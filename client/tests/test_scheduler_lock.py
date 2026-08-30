from __future__ import annotations

import threading
import time
import unittest
from unittest.mock import patch

from sync_client.lock_coordinator import LockCoordinator
from sync_client.transfer_scheduler import AGING_SECONDS, TransferScheduler


class TransferSchedulerTests(unittest.TestCase):
    def test_priority_waiter_runs_before_older_preview(self) -> None:
        scheduler = TransferScheduler()
        holder = scheduler.permit("mirror")
        self.assertTrue(holder.acquire())
        order: list[str] = []
        preview_ready = threading.Event()
        push_ready = threading.Event()

        def wait_and_record(kind: str, ready: threading.Event) -> None:
            permit = scheduler.permit(kind)
            ready.set()
            with permit:
                order.append(kind)
                time.sleep(0.01)

        preview = threading.Thread(target=wait_and_record, args=("preview", preview_ready))
        push = threading.Thread(target=wait_and_record, args=("push", push_ready))
        preview.start()
        self.assertTrue(preview_ready.wait(1))
        push.start()
        self.assertTrue(push_ready.wait(1))
        deadline = time.monotonic() + 1
        while time.monotonic() < deadline:
            with scheduler._condition:
                if len(scheduler._waiting) == 2:
                    break
            time.sleep(0.001)
        holder.release()
        preview.join(1)
        push.join(1)
        self.assertFalse(preview.is_alive())
        self.assertFalse(push.is_alive())
        self.assertEqual(order, ["push", "preview"])

    def test_nonblocking_and_timeout_do_not_leave_waiters_behind(self) -> None:
        scheduler = TransferScheduler()
        holder = scheduler.permit("push")
        self.assertTrue(holder.acquire())
        self.assertFalse(scheduler.permit("preview").acquire(blocking=False))
        self.assertFalse(scheduler.permit("preview").acquire(timeout=0.01))
        with scheduler._condition:
            self.assertEqual(scheduler._waiting, [])
        holder.release()

    def test_unknown_kind_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            TransferScheduler().permit("backup")

    def test_aging_prevents_starvation_of_old_preview(self) -> None:
        scheduler = TransferScheduler()
        holder = scheduler.permit("push")
        self.assertTrue(holder.acquire())
        order: list[str] = []

        def wait_and_record(kind: str) -> None:
            with scheduler.permit(kind):
                order.append(kind)

        preview_thread = threading.Thread(target=wait_and_record, args=("preview",))
        preview_thread.start()
        deadline = time.monotonic() + 1
        while time.monotonic() < deadline:
            with scheduler._condition:
                if scheduler._waiting:
                    scheduler._waiting[0].queued_at -= AGING_SECONDS * 4
                    break
            time.sleep(0.001)
        push_thread = threading.Thread(target=wait_and_record, args=("push",))
        push_thread.start()
        holder.release()
        preview_thread.join(1)
        push_thread.join(1)
        self.assertEqual(order, ["preview", "push"])

    def test_snapshot_reports_active_and_waiting_operations(self) -> None:
        scheduler = TransferScheduler()
        holder = scheduler.permit("push")
        self.assertTrue(holder.acquire())
        waiting = scheduler.permit("pull")
        thread = threading.Thread(target=waiting.acquire)
        thread.start()
        deadline = time.monotonic() + 1
        while time.monotonic() < deadline:
            if scheduler.snapshot()["count"] == 1:
                break
            time.sleep(0.001)
        snapshot = scheduler.snapshot()
        self.assertEqual(snapshot["active"], "push")
        self.assertEqual(snapshot["count"], 1)
        self.assertEqual(snapshot["waiting"][0]["kind"], "pull")
        holder.release()
        thread.join(1)
        waiting.release()


class LockCoordinatorTests(unittest.TestCase):
    def test_defer_exponentially_backs_off_and_acquired_resets_state(self) -> None:
        coordinator = LockCoordinator(initial_delay=5, max_delay=20)
        with patch("sync_client.lock_coordinator.random.uniform", return_value=1.0), patch(
            "sync_client.lock_coordinator.time.time", return_value=100.0
        ):
            self.assertEqual(coordinator.defer(), 5)
        self.assertFalse(coordinator.can_attempt(104))
        self.assertEqual(coordinator.retry_after(104), 1)

        with patch("sync_client.lock_coordinator.random.uniform", return_value=1.0), patch(
            "sync_client.lock_coordinator.time.time", return_value=200.0
        ):
            self.assertEqual(coordinator.defer(), 10)
        self.assertEqual(coordinator.retry_after(201), 9)
        coordinator.acquired()
        self.assertTrue(coordinator.can_attempt(0))
        self.assertEqual(coordinator.retry_after(0), 0)

    def test_jitter_is_clamped_to_at_least_one_second(self) -> None:
        coordinator = LockCoordinator(initial_delay=0, max_delay=1)
        with patch("sync_client.lock_coordinator.random.uniform", return_value=0.0), patch(
            "sync_client.lock_coordinator.time.time", return_value=10.0
        ):
            self.assertEqual(coordinator.defer(), 1)


if __name__ == "__main__":
    unittest.main()
