package utils

import (
	"fmt"
	"github.com/dustin/go-humanize"
	"github.com/hako/durafmt"
	"math"
	"strings"
	"time"
)

func FormatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return ""
	case v == 0:
		return "0"
	case v >= 1:
		return fmt.Sprintf("%.0f", v)
	case v >= 0.1:
		return fmt.Sprintf("%.1f", v)
	case v >= 0.01:
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.3f", v)
}

func FormatDuration(d time.Duration, limitFirstN int) string {
	return durafmt.Parse(d).LimitFirstN(limitFirstN).String()
}

func FormatBytes(b float64) (string, string) {
	s := humanize.Bytes(uint64(b))
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func HumanBits(v float64) string {
	if math.IsNaN(v) {
		return ""
	}
	return strings.Replace(humanize.Bytes(uint64(v)), "B", "b", -1) + "ps"
}

func FormatLatency(v float64) string {
	if v < 1 {
		return fmt.Sprintf(`%.f ms`, v*1000)
	}
	return fmt.Sprintf(`%.f s`, v)
}
