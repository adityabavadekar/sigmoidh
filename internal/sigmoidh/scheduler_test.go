package sigmoidh

import (
	"testing"
	"time"
)

func TestParseCronStepFromSingleValue(t *testing.T) {
	spec, err := parseCron("1/15 * * * *")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}
	for _, v := range []int{1, 16, 31, 46} {
		if !spec[0].values[v] {
			t.Fatalf("minute %d not matched by 1/15", v)
		}
	}
	for _, v := range []int{0, 2, 15, 17, 45, 47, 59} {
		if spec[0].values[v] {
			t.Fatalf("minute %d unexpectedly matched by 1/15", v)
		}
	}
}

func TestParseCronStepFromStar(t *testing.T) {
	spec, err := parseCron("*/15 * * * *")
	if err != nil {
		t.Fatalf("parseCron: %v", err)
	}
	for _, v := range []int{0, 15, 30, 45} {
		if !spec[0].values[v] {
			t.Fatalf("minute %d not matched by */15", v)
		}
	}
}

func TestCronMatchesStepFromSingleValue(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 46, 0, 0, time.Local)
	ok, err := cronMatches("1/15 * * * *", at)
	if err != nil || !ok {
		t.Fatalf("cronMatches 1/15 at :46 = %v, %v", ok, err)
	}
	ok, err = cronMatches("1/15 * * * *", time.Date(2026, 10, 17, 9, 47, 0, 0, time.Local))
	if err != nil || ok {
		t.Fatalf("cronMatches 1/15 at :47 = %v, %v", ok, err)
	}
}

func TestAddCronWithoutOccurrenceIsRejected(t *testing.T) {
	ts := newTaskScheduler()
	id := ts.AddCron("0 0 31 4 *", "impossible", func(string) {})
	if id != 0 {
		t.Fatalf("AddCron accepted an expression with no occurrence (id %d)", id)
	}
}
