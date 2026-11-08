"""Metric helpers with the suites' existing empty-group semantics.

``mean`` returns None for an empty group: an unmeasured metric is unknown, not
zero. ``rate`` returns 0.0 for an empty denominator to preserve the graph
suite's public report shape; callers that need "unknown" must check the group
size before calling it.
"""

from __future__ import annotations

import math


def mean(values: list[float]) -> float | None:
    return round(sum(values) / len(values), 4) if values else None


def rate(hits: int, total: int) -> float:
    return round(hits / total, 4) if total else 0.0


def percentile(values: list[float], fraction: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, math.ceil(fraction * len(ordered)) - 1))
    return round(ordered[index], 1)
