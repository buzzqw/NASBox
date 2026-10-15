from __future__ import annotations

import unittest
from datetime import datetime

from sync_client.config import (
    Config,
    normalize_schedule,
    normalize_schedule_window,
    schedule_state,
)

from tests.support import ClientEnvironment


# 2026-10-15 is a Thursday -> weekday() == 3 (Monday == 0).
THU = datetime(2026, 10, 15)
FRI = datetime(2026, 10, 16)


class ScheduleWindowTests(unittest.TestCase):
    def test_same_day_window_is_active_inside_and_inactive_outside(self) -> None:
        window = {"days": [3], "start": "09:00", "end": "18:00"}
        self.assertTrue(schedule_state([window], THU.replace(hour=12))[0])
        self.assertTrue(schedule_state([window], THU.replace(hour=9, minute=0))[0])
        self.assertFalse(schedule_state([window], THU.replace(hour=18, minute=0))[0])
        self.assertFalse(schedule_state([window], THU.replace(hour=8, minute=59))[0])

    def test_window_only_matches_selected_days(self) -> None:
        window = {"days": [3], "start": "09:00", "end": "18:00"}
        self.assertTrue(schedule_state([window], THU.replace(hour=12))[0])
        self.assertFalse(schedule_state([window], FRI.replace(hour=12))[0])

    def test_overnight_window_wraps_past_midnight(self) -> None:
        window = {"days": [3], "start": "22:00", "end": "07:00"}
        # Thursday evening.
        self.assertTrue(schedule_state([window], THU.replace(hour=23))[0])
        # Friday early morning still belongs to Thursday's window.
        self.assertTrue(schedule_state([window], FRI.replace(hour=2))[0])
        # Friday evening is a different (unselected) start day.
        self.assertFalse(schedule_state([window], FRI.replace(hour=23))[0])
        # Thursday daytime is outside.
        self.assertFalse(schedule_state([window], THU.replace(hour=12))[0])

    def test_remaining_seconds_before_window_end(self) -> None:
        window = {"days": [3], "start": "09:00", "end": "18:00"}
        active, remaining = schedule_state([window], THU.replace(hour=17, minute=30), want_remaining=True)
        self.assertTrue(active)
        self.assertEqual(remaining, 30 * 60)

    def test_remaining_seconds_across_midnight(self) -> None:
        window = {"days": [3], "start": "22:00", "end": "07:00"}
        active, remaining = schedule_state([window], FRI.replace(hour=6, minute=30), want_remaining=True)
        self.assertTrue(active)
        self.assertEqual(remaining, 30 * 60)

    def test_multiple_windows_take_the_soonest_end(self) -> None:
        windows = [
            {"days": [3], "start": "08:00", "end": "20:00"},
            {"days": [3], "start": "12:00", "end": "13:00"},
        ]
        active, remaining = schedule_state(windows, THU.replace(hour=12, minute=30), want_remaining=True)
        self.assertTrue(active)
        self.assertEqual(remaining, 30 * 60)

    def test_normalization_rejects_bad_windows(self) -> None:
        self.assertIsNone(normalize_schedule_window({"days": [1], "start": "09:00", "end": "09:00"}))
        self.assertIsNone(normalize_schedule_window({"days": [], "start": "09:00", "end": "10:00"}))
        self.assertIsNone(normalize_schedule_window({"days": [1], "start": "25:00", "end": "10:00"}))
        self.assertIsNone(normalize_schedule_window({"days": [1], "start": "09:60", "end": "10:00"}))
        self.assertEqual(
            normalize_schedule_window({"days": [1], "start": "9:00", "end": "10:00"}),
            {"days": [1], "start": "09:00", "end": "10:00"},
        )
        self.assertEqual(
            normalize_schedule([{"days": [1, 1, 2], "start": "09:00", "end": "18:00"}]),
            [{"days": [1, 2], "start": "09:00", "end": "18:00"}],
        )


class ConfigScheduleTests(unittest.TestCase):
    def setUp(self) -> None:
        self.environment = ClientEnvironment()
        self.environment.__enter__()
        self.cfg = Config()

    def tearDown(self) -> None:
        self.environment.__exit__(None, None, None)

    def test_schedule_is_off_by_default(self) -> None:
        self.assertFalse(self.cfg.schedule_active_at(THU.replace(hour=12)))
        self.assertFalse(self.cfg.is_paused())

    def test_is_paused_follows_the_schedule(self) -> None:
        self.cfg.set("pause_schedule", [{"days": [3], "start": "09:00", "end": "18:00"}], persist=False)
        self.cfg.set("pause_schedule_enabled", True, persist=False)
        self.assertTrue(self.cfg.is_paused())
        # The manual toggle must not claim the user paused it.
        self.assertFalse(self.cfg.is_manually_paused())

    def test_manual_pause_and_schedule_are_independent(self) -> None:
        self.cfg.pause_indefinitely()
        self.assertTrue(self.cfg.is_manually_paused())
        self.cfg.resume()
        self.assertFalse(self.cfg.is_manually_paused())

    def test_schedule_remaining_uses_the_active_window(self) -> None:
        self.cfg.set("pause_schedule", [{"days": [3], "start": "09:00", "end": "18:00"}], persist=False)
        self.cfg.set("pause_schedule_enabled", True, persist=False)
        remaining = self.cfg.schedule_remaining_seconds(THU.replace(hour=9, minute=15))
        self.assertEqual(remaining, 8 * 60 * 60 + 45 * 60)


if __name__ == "__main__":
    unittest.main()
