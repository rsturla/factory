use std::time::Duration;

use crate::error::Error;

pub fn parse_duration(s: &str) -> std::result::Result<Duration, Error> {
    if s.is_empty() || s == "0" || s == "0s" {
        return Ok(Duration::ZERO);
    }

    let s = s.strip_prefix('-').unwrap_or(s);
    let mut total_nanos: u128 = 0;
    let mut pos = 0;
    let bytes = s.as_bytes();

    while pos < bytes.len() {
        let num_start = pos;
        while pos < bytes.len() && (bytes[pos].is_ascii_digit() || bytes[pos] == b'.') {
            pos += 1;
        }
        if pos == num_start {
            return Err(Error::InvalidDuration(s.to_string()));
        }

        let num_str = &s[num_start..pos];
        let value: f64 = num_str
            .parse()
            .map_err(|_| Error::InvalidDuration(s.to_string()))?;

        let unit_start = pos;
        while pos < bytes.len() && !bytes[pos].is_ascii_digit() && bytes[pos] != b'.' {
            pos += 1;
        }
        let unit = &s[unit_start..pos];

        let nanos_per_unit: f64 = match unit {
            "ns" => 1.0,
            "us" | "µs" => 1_000.0,
            "ms" => 1_000_000.0,
            "s" => 1_000_000_000.0,
            "m" => 60_000_000_000.0,
            "h" => 3_600_000_000_000.0,
            _ => return Err(Error::InvalidDuration(format!("unknown unit: {unit}"))),
        };

        total_nanos += (value * nanos_per_unit) as u128;
    }

    Ok(Duration::from_nanos(total_nanos as u64))
}

pub fn format_duration(d: Duration) -> String {
    let total_nanos = d.as_nanos();
    if total_nanos == 0 {
        return "0s".to_string();
    }

    let mut remaining = total_nanos;
    let mut parts = String::new();

    let hours = remaining / 3_600_000_000_000;
    if hours > 0 {
        parts.push_str(&format!("{hours}h"));
        remaining %= 3_600_000_000_000;
    }

    let minutes = remaining / 60_000_000_000;
    if minutes > 0 || hours > 0 {
        parts.push_str(&format!("{minutes}m"));
        remaining %= 60_000_000_000;
    }

    let has_higher = hours > 0 || minutes > 0;

    if remaining > 0 || !has_higher {
        if !has_higher && remaining < 1_000 {
            parts.push_str(&format!("{remaining}ns"));
        } else if !has_higher && remaining < 1_000_000 {
            let us = remaining as f64 / 1_000.0;
            parts.push_str(&format!("{}µs", format_number(us)));
        } else if !has_higher && remaining < 1_000_000_000 {
            let ms = remaining as f64 / 1_000_000.0;
            parts.push_str(&format!("{}ms", format_number(ms)));
        } else {
            let secs = remaining as f64 / 1_000_000_000.0;
            parts.push_str(&format!("{}s", format_number(secs)));
        }
    } else if has_higher {
        parts.push_str("0s");
    }

    parts
}

fn format_number(v: f64) -> String {
    if v == v.floor() {
        format!("{}", v as u64)
    } else {
        format!("{v}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_zero() {
        assert_eq!(parse_duration("0s").unwrap(), Duration::ZERO);
        assert_eq!(parse_duration("0").unwrap(), Duration::ZERO);
        assert_eq!(parse_duration("").unwrap(), Duration::ZERO);
    }

    #[test]
    fn test_parse_seconds() {
        assert_eq!(parse_duration("30s").unwrap(), Duration::from_secs(30));
    }

    #[test]
    fn test_parse_minutes() {
        assert_eq!(parse_duration("5m0s").unwrap(), Duration::from_secs(300));
    }

    #[test]
    fn test_parse_compound() {
        assert_eq!(
            parse_duration("1h30m15s").unwrap(),
            Duration::from_secs(5415)
        );
    }

    #[test]
    fn test_parse_milliseconds() {
        assert_eq!(
            parse_duration("500ms").unwrap(),
            Duration::from_millis(500)
        );
    }

    #[test]
    fn test_format_zero() {
        assert_eq!(format_duration(Duration::ZERO), "0s");
    }

    #[test]
    fn test_format_seconds() {
        assert_eq!(format_duration(Duration::from_secs(30)), "30s");
    }

    #[test]
    fn test_format_minutes() {
        assert_eq!(format_duration(Duration::from_secs(300)), "5m0s");
    }

    #[test]
    fn test_format_hours() {
        assert_eq!(format_duration(Duration::from_secs(7200)), "2h0m0s");
    }

    #[test]
    fn test_format_compound() {
        assert_eq!(format_duration(Duration::from_secs(5415)), "1h30m15s");
    }

    #[test]
    fn test_format_milliseconds() {
        assert_eq!(format_duration(Duration::from_millis(500)), "500ms");
    }

    #[test]
    fn test_roundtrip() {
        for s in &["30s", "5m0s", "1h0m0s", "1h30m15s", "500ms"] {
            let d = parse_duration(s).unwrap();
            assert_eq!(&format_duration(d), s, "roundtrip failed for {s}");
        }
    }
}
