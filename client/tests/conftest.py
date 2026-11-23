"""Shared pytest setup for the NASBox client test suite.

Make the Qt binding choice explicit before pytest-qt imports it. The value is a
default, so an explicit PYTEST_QT_API in the environment still wins.
"""
from __future__ import annotations

import os

os.environ.setdefault("PYTEST_QT_API", "pyqt6")

# Headless CI has no X server; offscreen keeps the widgets constructible.
# An explicit QT_QPA_PLATFORM in the environment still wins.
os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")
