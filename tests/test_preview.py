"""Transaction preview parity with internal/preview."""

from __future__ import annotations

from arne import preview


def _base() -> preview.Input:
    return preview.Input(
        network="arne:devnet", signer="arce-devnet1sender", to="arce-devnet1recipient",
        asset="arce:devnet", symbol="ARCE", decimals=8,
        amount=1_250_000_000, fee=1_000, asset_known=True,
    )


def test_clean_preview() -> None:
    result = preview.build(_base())
    assert result.ok
    assert result.warnings == []
    assert result.amount == "12.5 ARCE"
    assert result.fee == "0.00001 ARCE"


def test_unknown_asset() -> None:
    data = _base()
    data.asset_known, data.symbol = False, ""
    result = preview.build(data)
    assert not result.ok
    assert len(result.warnings) == 1


def test_warnings() -> None:
    data = _base()
    data.to = data.signer
    assert len(preview.build(data).warnings) == 1

    data = _base()
    data.known = ["arce-devnet1AAAAAAxyzABC"]
    data.to = "arce-devnet1BBBBBBxyzABC"
    assert len(preview.build(data).warnings) == 1

    data = _base()
    data.memo, data.public_memo = "invoice 42", True
    assert len(preview.build(data).warnings) == 1

    data = _base()
    data.simulated, data.code = True, "INSUFFICIENT_FUNDS"
    result = preview.build(data)
    assert not result.ok
    assert len(result.warnings) == 1


def test_exact_contact_not_warned() -> None:
    data = _base()
    data.known = [data.to]
    assert preview.build(data).warnings == []
