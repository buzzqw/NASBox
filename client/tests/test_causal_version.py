from __future__ import annotations

import pytest

from sync_client.sync_state import CausalVersion


def test_causal_version_parse_and_encode():
    cv = CausalVersion.parse("devA:3,devB:1")
    assert cv is not None
    assert cv.counter("devA") == 3
    assert cv.counter("devB") == 1
    assert cv.counter("devC") == 0
    assert cv.encode() == "devA:3,devB:1"

    # Invalid cases
    assert CausalVersion.parse(None) is None
    assert CausalVersion.parse("") is None
    assert CausalVersion.parse("invalid") is None
    assert CausalVersion.parse("devA:0") is None  # counter < 1 is invalid


def test_causal_version_increment_and_merge():
    cv1 = CausalVersion.parse("devA:1")
    assert cv1 is not None
    cv1_next = cv1.increment("devA")
    assert cv1_next.counter("devA") == 2

    cv2 = CausalVersion.parse("devB:3")
    merged = cv1_next.merge(cv2)
    assert merged.counter("devA") == 2
    assert merged.counter("devB") == 3


def test_causal_version_dominates():
    cv_base = CausalVersion.parse("devA:1,devB:1")
    cv_advanced = CausalVersion.parse("devA:2,devB:1")
    cv_diverged = CausalVersion.parse("devA:1,devB:2")

    assert cv_advanced.dominates(cv_base)
    assert not cv_base.dominates(cv_advanced)

    # Diverged concurrent versions: neither dominates the other
    assert not cv_advanced.dominates(cv_diverged)
    assert not cv_diverged.dominates(cv_advanced)
