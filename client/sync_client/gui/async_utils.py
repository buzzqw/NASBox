"""Runs a single blocking call off the GUI thread.

Several buttons (Rileva dal NAS, Riavvia demone, Chiedi al NAS di pulire ora)
shell out to `ssh`/`subprocess.run` with timeouts up to 120s. Calling that
straight from a button's clicked slot -- as this app originally did -- runs
it on the Qt GUI thread, which means the *entire window* stops repainting
and responding to input for as long as the remote call takes (seen for real:
"Chiedi al NAS di pulire ora" against a NAS with a large .sync-trash pegged
the UI for minutes). BackgroundCall moves the blocking part to a QThread and
hands the result back via a signal, which Qt safely queues onto the GUI
thread regardless of which thread emitted it.
"""
from __future__ import annotations

from typing import Any, Callable, Optional

from PyQt6.QtCore import QThread, pyqtSignal


class BackgroundCall(QThread):
    # (result, exception) -- exactly one of the two is None. Exceptions are
    # forwarded rather than swallowed so the caller decides how to surface
    # them (this thread has no widgets of its own to show anything with).
    done = pyqtSignal(object, object)
    progress = pyqtSignal(object)

    def __init__(self, fn: Callable[[], Any], parent=None) -> None:
        super().__init__(parent)
        self._fn = fn

    def run(self) -> None:
        try:
            result = self._fn()
        except Exception as exc:
            self.done.emit(None, exc)
        else:
            self.done.emit(result, None)


def run_in_background(
    owner: Any, attr: str, fn: Callable[..., Any], on_done: Callable[[Any, Optional[Exception]], None],
    on_progress: Optional[Callable[[Any], None]] = None,
) -> None:
    """Starts fn() on a BackgroundCall, keeping the thread alive as owner.<attr>
    (a bare local variable would let Python garbage-collect the QThread out
    from under itself the moment this function returns). Call this from a
    button handler; do the actual widget updates in on_done, which always
    runs back on the GUI thread."""
    # The closure runs only after ``call`` has been assigned, so workers can
    # report progress through a queued Qt signal without touching widgets.
    call: BackgroundCall
    call = BackgroundCall(
        lambda: fn(call.progress.emit) if on_progress is not None else fn(),
        parent=owner,
    )
    setattr(owner, attr, call)
    call.done.connect(on_done)
    if on_progress is not None:
        call.progress.connect(on_progress)
    call.start()
