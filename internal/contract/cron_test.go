package contract_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
)

func TestParseCronAcceptsTheDocumentedDialect(t *testing.T) {
	valid := []string{
		"0 2 * * *",
		"*/15 * * * *",
		"0 9-17 * * 1-5",
		"0 0 1,15 * *",
		"30 6 * * 7",
		"0 0 * * 0",
		"5/10 * * * *",
		"0 0-23/2 * * *",
	}
	for _, expr := range valid {
		if _, err := contract.ParseCron(expr); err != nil {
			t.Errorf("ParseCron(%q) = %v, want nil", expr, err)
		}
	}
}

func TestParseCronRejectsBadFields(t *testing.T) {
	bad := map[string]string{
		"four fields":         "0 2 * *",
		"six fields":          "0 2 * * * *",
		"empty":               "",
		"minute out of range": "60 2 * * *",
		"hour out of range":   "0 24 * * *",
		"day zero":            "0 2 0 * *",
		"day 32":              "0 2 32 * *",
		"month zero":          "0 2 * 0 *",
		"month 13":            "0 2 * 13 *",
		"weekday 8":           "0 2 * * 8",
		"reversed range":      "30-10 2 * * *",
		"zero step":           "*/0 * * * *",
		"negative step":       "*/ -1 * * *",
		"garbage":             "abc * * * *",
		"empty term":          "1,,2 * * * *",
		"bad range":           "1-2-3 * * * *",
	}
	for name, expr := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := contract.ParseCron(expr); err == nil {
				t.Fatalf("ParseCron(%q) should fail", expr)
			}
		})
	}
}

func TestCronMatchesMinuteResolutionUTC(t *testing.T) {
	c, err := contract.ParseCron("30 2 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if !c.Matches(time.Date(2026, time.October, 5, 2, 30, 0, 0, time.UTC)) {
		t.Fatal("02:30 UTC should match '30 2 * * *'")
	}
	if c.Matches(time.Date(2026, time.October, 5, 2, 31, 0, 0, time.UTC)) {
		t.Fatal("02:31 does not match a minute-30 expression")
	}
	if c.Matches(time.Date(2026, time.October, 5, 3, 30, 0, 0, time.UTC)) {
		t.Fatal("03:30 does not match hour 2")
	}
}

func TestCronMatchesStepsRangesAndSundaySeven(t *testing.T) {
	step, err := contract.ParseCron("*/15 * * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	for _, minute := range []int{0, 15, 30, 45} {
		at := time.Date(2026, time.October, 5, 10, minute, 0, 0, time.UTC)
		if !step.Matches(at) {
			t.Fatalf("minute %d should match */15", minute)
		}
	}
	if step.Matches(time.Date(2026, time.October, 5, 10, 7, 0, 0, time.UTC)) {
		t.Fatal("minute 7 should not match */15")
	}

	weekday, err := contract.ParseCron("0 0 * * 7")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	sunday := time.Date(2026, time.October, 4, 0, 0, 0, 0, time.UTC) // a Sunday
	if sunday.Weekday() != time.Sunday || !weekday.Matches(sunday) {
		t.Fatal("weekday 7 should match Sunday")
	}
}

func TestCronDayRules(t *testing.T) {
	// Both day-of-month and day-of-week restricted: either match qualifies.
	either, err := contract.ParseCron("0 0 1 * 1")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	firstOfMonth := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC) // Monday the 1st
	if !either.Matches(firstOfMonth) {
		t.Fatal("the 1st should match even though it is also a Monday")
	}
	firstNotMonday := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC) // Wednesday the 1st
	if !either.Matches(firstNotMonday) {
		t.Fatal("the 1st should match the day-of-month rule")
	}
	mondayNotFirst := time.Date(2026, time.June, 8, 0, 0, 0, 0, time.UTC)
	if !either.Matches(mondayNotFirst) {
		t.Fatal("a Monday should match the day-of-week rule")
	}
	other := time.Date(2026, time.June, 9, 0, 0, 0, 0, time.UTC)
	if either.Matches(other) {
		t.Fatal("the 9th (a Tuesday) matches neither rule")
	}

	// A '*' day-of-month leaves only the weekday rule.
	dowOnly, err := contract.ParseCron("0 0 * * 1")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if !dowOnly.Matches(mondayNotFirst) {
		t.Fatal("Monday should match '0 0 * * 1'")
	}
	if dowOnly.Matches(firstNotMonday) {
		t.Fatal("Wednesday should not match '0 0 * * 1'")
	}
}

func TestParseCronErrorMentionsTheField(t *testing.T) {
	_, err := contract.ParseCron("0 25 * * *")
	if err == nil {
		t.Fatal("ParseCron should fail")
	}
	if !strings.Contains(err.Error(), "hour") {
		t.Fatalf("error = %q, want it to name the hour field", err)
	}
}
