from datetime import timedelta

import pytest

from factory_workqueue._duration import format_duration, parse_duration


class TestParseDuration:
    def test_zero(self):
        assert parse_duration("0s") == timedelta()
        assert parse_duration("0") == timedelta()

    def test_seconds(self):
        assert parse_duration("30s") == timedelta(seconds=30)

    def test_minutes(self):
        assert parse_duration("5m0s") == timedelta(minutes=5)

    def test_hours(self):
        assert parse_duration("1h0m0s") == timedelta(hours=1)

    def test_compound(self):
        assert parse_duration("1h30m15s") == timedelta(hours=1, minutes=30, seconds=15)

    def test_milliseconds(self):
        assert parse_duration("500ms") == timedelta(milliseconds=500)

    def test_microseconds(self):
        assert parse_duration("100us") == timedelta(microseconds=100)
        assert parse_duration("100µs") == timedelta(microseconds=100)

    def test_nanoseconds(self):
        assert parse_duration("1000ns") == timedelta(microseconds=1)

    def test_fractional(self):
        assert parse_duration("1.5s") == timedelta(seconds=1.5)

    def test_negative(self):
        assert parse_duration("-30s") == timedelta(seconds=-30)

    def test_invalid(self):
        with pytest.raises(ValueError):
            parse_duration("bad")

    def test_empty(self):
        assert parse_duration("") == timedelta()


class TestFormatDuration:
    def test_zero(self):
        assert format_duration(timedelta()) == "0s"

    def test_seconds(self):
        assert format_duration(timedelta(seconds=30)) == "30s"

    def test_minutes(self):
        assert format_duration(timedelta(minutes=5)) == "5m0s"

    def test_hours(self):
        assert format_duration(timedelta(hours=2)) == "2h0m0s"

    def test_compound(self):
        assert format_duration(timedelta(hours=1, minutes=30, seconds=15)) == "1h30m15s"

    def test_hours_minutes_seconds(self):
        assert format_duration(timedelta(minutes=5, seconds=30)) == "5m30s"

    def test_negative(self):
        assert format_duration(timedelta(seconds=-30)) == "-30s"


class TestRoundTrip:
    @pytest.mark.parametrize("s,expected", [
        ("30s", "30s"),
        ("5m", "5m0s"),
        ("5m0s", "5m0s"),
        ("1h", "1h0m0s"),
        ("1h30m15s", "1h30m15s"),
        ("500ms", "500ms"),
    ])
    def test_roundtrip(self, s, expected):
        assert format_duration(parse_duration(s)) == expected
