"""Payment URI validation beyond the shared vectors."""

from __future__ import annotations

import pytest
from arne import paymenturi


def test_uri_amount_mismatch() -> None:

    payment = paymenturi.parse(
        "arce:arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t"
        "?network=arne%3Adevnet&asset=arce%3Adevnet&amount=12.5"
    )
    with pytest.raises(paymenturi.URIError):
        payment.check_amount("13")
    payment.check_amount("12.5")
    with pytest.raises(paymenturi.URIError):
        payment.check_recipient("arce-devnet1other")
    with pytest.raises(paymenturi.URIError):
        payment.check_network("arne:mainnet")
