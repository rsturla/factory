package parser

import "time"

var timeFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02",
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, fmt := range timeFormats {
		if t, err := time.Parse(fmt, s); err == nil {
			return &t
		}
	}
	return nil
}
