package recurrence

import (
	"testing"
	"time"
)

func mustCivil(t *testing.T, value string) CivilDate {
	t.Helper()
	date, err := ParseCivilDate(value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return date
}

func TestParseCivilDate(t *testing.T) {
	good := mustCivil(t, "2026-05-20")
	if good.Year != 2026 || good.Month != time.May || good.Day != 20 {
		t.Fatalf("got %+v", good)
	}
	if got := good.String(); got != "2026-05-20" {
		t.Fatalf("String = %q", got)
	}
	for _, bad := range []string{"", "2026-13-01", "2026-02-30", "2026-1-1", "not-a-date", "2026/05/20"} {
		if _, err := ParseCivilDate(bad); err == nil {
			t.Fatalf("ParseCivilDate(%q) expected error", bad)
		}
	}
	a := mustCivil(t, "2026-05-20")
	b := mustCivil(t, "2026-05-21")
	if !a.Before(b) || b.Before(a) || a.After(a) {
		t.Fatal("civil date comparison is wrong")
	}
	if got := mustCivil(t, "2026-01-31").AddDays(1); got.String() != "2026-02-01" {
		t.Fatalf("AddDays across month = %s", got)
	}
	if got := mustCivil(t, "2026-12-31").AddDays(1); got.String() != "2027-01-01" {
		t.Fatalf("AddDays across year = %s", got)
	}
}

func TestDailyEnumerationUTC(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	rule := Rule{Recurrence: Daily, Timezone: "UTC", Hour: 9, Minute: 30, StartDate: mustCivil(t, "2026-05-20")}
	from := mustCivil(t, "2026-05-20")
	for i := 0; i < 5; i++ {
		occ, ok := rule.NextOccurrence(from, loc)
		if !ok {
			t.Fatalf("occurrence %d missing", i)
		}
		wantDate := mustCivil(t, "2026-05-20").AddDays(i)
		if occ.Date.String() != wantDate.String() {
			t.Fatalf("occurrence %d date = %s, want %s", i, occ.Date, wantDate)
		}
		if got := occ.Instant.Format(time.RFC3339); got != wantDate.String()+"T09:30:00Z" {
			t.Fatalf("occurrence %d instant = %s", i, got)
		}
		if occ.Gap || occ.Ambiguous {
			t.Fatalf("occurrence %d unexpectedly gap/ambiguous", i)
		}
		from = occ.Date.AddDays(1)
	}
}

func TestDailyEnumerationKolkataHalfHourOffset(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Kolkata")
	rule := Rule{Recurrence: Daily, Timezone: "Asia/Kolkata", Hour: 9, Minute: 30, StartDate: mustCivil(t, "2026-05-20")}
	occ, ok := rule.NextOccurrence(mustCivil(t, "2026-05-20"), loc)
	if !ok {
		t.Fatal("missing occurrence")
	}
	want := time.Date(2026, 5, 20, 4, 0, 0, 0, time.UTC)
	if !occ.Instant.Equal(want) {
		t.Fatalf("instant = %s, want %s", occ.Instant.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestWeeklyWeekdayFilterAndEndDate(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	// Monday=1 and Wednesday=3, between 2026-05-18 (Mon) and 2026-05-31 (Sun).
	rule := Rule{
		Recurrence: Weekly, Timezone: "UTC", Hour: 8, Minute: 0,
		StartDate: mustCivil(t, "2026-05-18"),
		EndDate:   mustCivil(t, "2026-05-27"),
		Weekdays:  []time.Weekday{time.Monday, time.Wednesday},
	}
	var got []string
	from := rule.StartDate
	for {
		occ, ok := rule.NextOccurrence(from, loc)
		if !ok {
			break
		}
		got = append(got, occ.Date.String())
		from = occ.Date.AddDays(1)
	}
	want := []string{"2026-05-18", "2026-05-20", "2026-05-25", "2026-05-27"}
	if len(got) != len(want) {
		t.Fatalf("dates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dates = %v, want %v", got, want)
		}
	}
	// End date is inclusive; after it, nothing remains.
	if _, ok := rule.NextMatch(mustCivil(t, "2026-05-28")); ok {
		t.Fatal("weekly rule produced an occurrence after its end date")
	}
	// End date equal to the start date still allows that day.
	single := rule
	single.StartDate = mustCivil(t, "2026-05-20")
	single.EndDate = mustCivil(t, "2026-05-20")
	date, ok := single.NextMatch(mustCivil(t, "2026-05-19"))
	if !ok || date.String() != "2026-05-20" {
		t.Fatalf("inclusive end date resolved to %v %v", date, ok)
	}
}

func TestWeeklyEnumerationArizonaNoDST(t *testing.T) {
	loc, _ := time.LoadLocation("America/Phoenix")
	rule := Rule{Recurrence: Weekly, Timezone: "America/Phoenix", Hour: 9, Minute: 30,
		StartDate: mustCivil(t, "2026-03-01"), Weekdays: []time.Weekday{time.Sunday}}
	occ, ok := rule.NextOccurrence(mustCivil(t, "2026-03-01"), loc)
	if !ok {
		t.Fatal("missing occurrence")
	}
	if occ.Date.String() != "2026-03-01" || !occ.Instant.Equal(time.Date(2026, 3, 1, 16, 30, 0, 0, time.UTC)) {
		t.Fatalf("got %s %s", occ.Date, occ.Instant.Format(time.RFC3339))
	}
}

func TestSpringForwardGapIsDetected(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	rule := Rule{Recurrence: Daily, Timezone: "America/New_York", Hour: 2, Minute: 30,
		StartDate: mustCivil(t, "2026-03-08")}
	occ := rule.Resolve(mustCivil(t, "2026-03-08"), loc)
	if !occ.Gap {
		t.Fatalf("expected gap, got %+v", occ)
	}
	if occ.Ambiguous {
		t.Fatal("gap must not be reported ambiguous")
	}
	// An hour that survives the transition is not a gap.
	rule.Hour = 9
	if occ := rule.Resolve(mustCivil(t, "2026-03-08"), loc); occ.Gap || occ.Ambiguous {
		t.Fatalf("09:30 must resolve normally, got %+v", occ)
	}
}

func TestFallBackAmbiguousResolvesOnceToEarlierInstant(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	rule := Rule{Recurrence: Daily, Timezone: "America/New_York", Hour: 1, Minute: 30,
		StartDate: mustCivil(t, "2026-11-01")}
	date := mustCivil(t, "2026-11-01")
	first := rule.Resolve(date, loc)
	if !first.Ambiguous || first.Gap {
		t.Fatalf("expected ambiguous, got %+v", first)
	}
	// 01:30 repeats; the earlier copy is 05:30Z (EDT), the later is 06:30Z (EST).
	want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	if !first.Instant.Equal(want) {
		t.Fatalf("instant = %s, want earlier %s", first.Instant.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	// Resolution is deterministic across repeated wakeups.
	for i := 0; i < 5; i++ {
		again := rule.Resolve(date, loc)
		if !again.Instant.Equal(first.Instant) {
			t.Fatalf("ambiguous resolution not deterministic: %s vs %s", again.Instant, first.Instant)
		}
	}
}

func TestValidate(t *testing.T) {
	base := Rule{Recurrence: Daily, Timezone: "UTC", Hour: 9, Minute: 30, StartDate: mustCivil(t, "2026-05-20")}
	if err := base.Validate(); err != nil {
		t.Fatalf("base rule invalid: %v", err)
	}
	invalid := []Rule{
		{Recurrence: "monthly", Timezone: "UTC", Hour: 9, Minute: 0, StartDate: base.StartDate},
		{Recurrence: Daily, Timezone: "Not/AZone", Hour: 9, Minute: 0, StartDate: base.StartDate},
		{Recurrence: Daily, Timezone: "UTC", Hour: 24, Minute: 0, StartDate: base.StartDate},
		{Recurrence: Daily, Timezone: "UTC", Hour: 9, Minute: 60, StartDate: base.StartDate},
		{Recurrence: Weekly, Timezone: "UTC", Hour: 9, Minute: 0, StartDate: base.StartDate, Weekdays: nil},
		{Recurrence: Daily, Timezone: "UTC", Hour: 9, Minute: 0, StartDate: mustCivil(t, "2026-05-20"), EndDate: mustCivil(t, "2026-05-19")},
	}
	for i, rule := range invalid {
		if err := rule.Validate(); err == nil {
			t.Fatalf("invalid rule %d accepted: %+v", i, rule)
		}
	}
	weekly := base
	weekly.Recurrence = Weekly
	weekly.Weekdays = []time.Weekday{time.Monday}
	if err := weekly.Validate(); err != nil {
		t.Fatalf("weekly rule invalid: %v", err)
	}
}

func TestWeekdayEncoding(t *testing.T) {
	encoded := EncodeWeekdays([]time.Weekday{time.Wednesday, time.Monday, time.Monday, time.Sunday})
	if encoded != "0,1,3" {
		t.Fatalf("encoded = %q", encoded)
	}
	decoded, err := ParseWeekdays("3,1")
	if err != nil || len(decoded) != 2 || decoded[0] != time.Monday || decoded[1] != time.Wednesday {
		t.Fatalf("decoded = %v, %v", decoded, err)
	}
	if days, err := ParseWeekdays(""); err != nil || len(days) != 0 {
		t.Fatalf("empty weekdays = %v, %v", days, err)
	}
	if _, err := ParseWeekdays("7"); err == nil {
		t.Fatal("invalid weekday accepted")
	}
}

func TestCivilDateAtUsesLocation(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Kolkata")
	// 2026-05-20 02:00Z is still 2026-05-20 07:30 in Kolkata.
	got := CivilDateAt(time.Date(2026, 5, 20, 2, 0, 0, 0, time.UTC), loc)
	if got.String() != "2026-05-20" {
		t.Fatalf("civil date = %s", got)
	}
	// 2026-05-19 20:00Z is already 2026-05-20 in Kolkata.
	got = CivilDateAt(time.Date(2026, 5, 19, 20, 0, 0, 0, time.UTC), loc)
	if got.String() != "2026-05-20" {
		t.Fatalf("civil date across midnight = %s", got)
	}
}
