"""Signature-suite lifecycle registry.

Mirrors internal/crypto/suites. Deprecated suites verify historical signatures
but cannot register new accounts; disabled suites never verify.
"""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

ACTIVE = "active"
DEPRECATED = "deprecated"
DISABLED = "disabled"

_STATUSES = {ACTIVE, DEPRECATED, DISABLED}


class SuiteError(Exception):
    pass


class UnknownSuite(SuiteError):
    pass


class DeprecatedSuite(SuiteError):
    pass


class DisabledSuite(SuiteError):
    pass


@dataclass
class Suite:
    id: str
    algorithm: str
    status: str
    verify: Callable[[bytes, bytes, bytes], bool] | None = None


class Registry:
    def __init__(self, entries: list[Suite]) -> None:
        self._suites: dict[str, Suite] = {}
        for index, suite in enumerate(entries):
            suite.id = suite.id.strip()
            suite.algorithm = suite.algorithm.strip()
            if not suite.id or not suite.algorithm:
                raise SuiteError(f"entries[{index}] needs an id and algorithm")
            if suite.status not in _STATUSES:
                raise SuiteError(f"{suite.id} has status {suite.status!r}")
            if suite.status != DISABLED and suite.verify is None:
                raise SuiteError(f"{suite.id} needs a verifier")
            if suite.id in self._suites:
                raise SuiteError(f"duplicate suite {suite.id!r}")
            self._suites[suite.id] = suite

    def status(self, suite_id: str) -> str | None:
        suite = self._suites.get(suite_id)
        return suite.status if suite else None

    def can_register(self, suite_id: str) -> None:
        suite = self._suites.get(suite_id)
        if suite is None:
            raise UnknownSuite(suite_id)
        if suite.status == DEPRECATED:
            raise DeprecatedSuite(suite_id)
        if suite.status == DISABLED:
            raise DisabledSuite(suite_id)

    def verify(self, suite_id: str, public_key: bytes, message: bytes, signature: bytes) -> None:
        suite = self._suites.get(suite_id)
        if suite is None:
            raise UnknownSuite(suite_id)
        if suite.status == DISABLED:
            raise DisabledSuite(suite_id)
        assert suite.verify is not None
        if not suite.verify(public_key, message, signature):
            raise SuiteError(f"{suite_id} signature")

    def ids(self) -> list[str]:
        return sorted(self._suites)
