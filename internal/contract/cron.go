package contract

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed 5-field cron expression: minute, hour, day-of-month,
// month, day-of-week (0 and 7 are both Sunday). Resolution is one minute and
// matching is UTC (ADR 0007).
type Cron struct {
	minutes  uint64
	hours    uint64
	days     uint64
	months   uint64
	weekdays uint64
}

// ParseCron parses and validates a 5-field cron expression. The dialect is
// the usual one: 'field' is a comma-separated list of '*', 'N', 'A-B', or any
// of those with '/step'. Bad field counts and out-of-range values fail here,
// not at fire time.
func ParseCron(expr string) (Cron, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}
	var c Cron
	var err error
	if c.minutes, err = parseCronField(fields[0], 0, 59); err != nil {
		return Cron{}, fmt.Errorf("minute: %w", err)
	}
	if c.hours, err = parseCronField(fields[1], 0, 23); err != nil {
		return Cron{}, fmt.Errorf("hour: %w", err)
	}
	if c.days, err = parseCronField(fields[2], 1, 31); err != nil {
		return Cron{}, fmt.Errorf("day of month: %w", err)
	}
	if c.months, err = parseCronField(fields[3], 1, 12); err != nil {
		return Cron{}, fmt.Errorf("month: %w", err)
	}
	if c.weekdays, err = parseCronField(fields[4], 0, 7); err != nil {
		return Cron{}, fmt.Errorf("day of week: %w", err)
	}
	// 7 is Sunday, same as 0.
	if c.weekdays&(1<<7) != 0 {
		c.weekdays |= 1 << 0
		c.weekdays &^= 1 << 7
	}
	return c, nil
}

// Matches reports whether t matches the expression, to the minute. Times are
// interpreted in UTC. The standard day rules apply: when both day-of-month
// and day-of-week are restricted, either matching is enough; a '*' field
// leaves the decision to the other.
func (c Cron) Matches(t time.Time) bool {
	t = t.UTC()
	if c.minutes&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if c.hours&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if c.months&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	dom := c.days&(1<<uint(t.Day())) != 0
	dow := c.weekdays&(1<<uint(int(t.Weekday()))) != 0
	switch {
	case c.days == bitRange(1, 31):
		return dow
	case c.weekdays == bitRange(0, 6):
		return dom
	default:
		return dom || dow
	}
}

// parseCronField turns one field into a bitmask of matching values between
// min and max.
func parseCronField(field string, min, max int) (uint64, error) {
	var mask uint64
	for _, term := range strings.Split(field, ",") {
		if term == "" {
			return 0, fmt.Errorf("empty term in %q", field)
		}
		lo, hi := min, max
		step := 1
		if i := strings.IndexByte(term, '/'); i >= 0 {
			n, err := strconv.Atoi(term[i+1:])
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid step in %q", term)
			}
			step = n
			term = term[:i]
		}
		switch {
		case term == "*":
			// The whole range.
		case strings.Contains(term, "-"):
			parts := strings.SplitN(term, "-", 2)
			if strings.Contains(parts[1], "-") {
				return 0, fmt.Errorf("invalid range %q", term)
			}
			a, errA := strconv.Atoi(parts[0])
			b, errB := strconv.Atoi(parts[1])
			if errA != nil || errB != nil {
				return 0, fmt.Errorf("invalid range %q", term)
			}
			lo, hi = a, b
		default:
			n, err := strconv.Atoi(term)
			if err != nil {
				return 0, fmt.Errorf("invalid value %q", term)
			}
			lo, hi = n, n
			if step != 1 {
				hi = max // "N/step" runs from N to the end of the range
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, fmt.Errorf("range %d-%d is outside %d-%d", lo, hi, min, max)
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, nil
}

// bitRange returns a mask with every bit from lo to hi set.
func bitRange(lo, hi int) uint64 {
	var mask uint64
	for v := lo; v <= hi; v++ {
		mask |= 1 << uint(v)
	}
	return mask
}
