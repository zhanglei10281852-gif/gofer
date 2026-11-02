// Package recurrence computes the local-calendar occurrences of daily and
// weekly send schedules. It is pure: no database, network, or process state,
// so every timezone and daylight-saving edge case is unit testable with a
// fixed clock.
package recurrence

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Recurrence string

const (
	Daily  Recurrence = "daily"
	Weekly Recurrence = "weekly"
)

// CivilDate is a date in a schedule's local timezone.
type CivilDate struct {
	Year  int
	Month time.Month
	Day   int
}

// ParseCivilDate parses a strict YYYY-MM-DD date.
func ParseCivilDate(value string) (CivilDate, error) {
	value = strings.TrimSpace(value)
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return CivilDate{}, fmt.Errorf("use a valid date")
	}
	year, err1 := strconv.Atoi(value[0:4])
	month, err2 := strconv.Atoi(value[5:7])
	day, err3 := strconv.Atoi(value[8:10])
	if err1 != nil || err2 != nil || err3 != nil {
		return CivilDate{}, fmt.Errorf("use a valid date")
	}
	d := CivilDate{Year: year, Month: time.Month(month), Day: day}
	if !d.valid() {
		return CivilDate{}, fmt.Errorf("use a valid date")
	}
	return d, nil
}

func (d CivilDate) valid() bool {
	if d.Year < 1970 || d.Year > 9999 || d.Month < time.January || d.Month > time.December || d.Day < 1 {
		return false
	}
	// time.Date normalizes impossible days into the next month.
	back := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
	return back.Year() == d.Year && back.Month() == d.Month && back.Day() == d.Day
}

func (d CivilDate) IsZero() bool {
	return d.Year == 0 && d.Month == 0 && d.Day == 0
}

// String renders YYYY-MM-DD.
func (d CivilDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

func (d CivilDate) Before(other CivilDate) bool {
	return d.compare(other) < 0
}

func (d CivilDate) After(other CivilDate) bool {
	return d.compare(other) > 0
}

func (d CivilDate) compare(other CivilDate) int {
	switch {
	case d.Year != other.Year:
		if d.Year < other.Year {
			return -1
		}
		return 1
	case d.Month != other.Month:
		if d.Month < other.Month {
			return -1
		}
		return 1
	case d.Day != other.Day:
		if d.Day < other.Day {
			return -1
		}
		return 1
	}
	return 0
}

// AddDays returns the date n days later, normalizing over month and year
// boundaries in UTC (civil calendar arithmetic only).
func (d CivilDate) AddDays(n int) CivilDate {
	t := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
	return CivilDate{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// At returns the date observable in loc at the given instant.
func CivilDateAt(t time.Time, loc *time.Location) CivilDate {
	local := t.In(loc)
	return CivilDate{Year: local.Year(), Month: local.Month(), Day: local.Day()}
}

// Rule is a validated daily/weekly recurrence specification.
type Rule struct {
	Recurrence Recurrence
	Timezone   string
	Hour       int
	Minute     int
	StartDate  CivilDate
	// EndDate zero means the rule has no end. The end date is inclusive: an
	// occurrence on the end date itself still sends.
	EndDate  CivilDate
	Weekdays []time.Weekday
}

// Location loads the rule's IANA timezone.
func (r Rule) Location() (*time.Location, error) {
	loc, err := time.LoadLocation(strings.TrimSpace(r.Timezone))
	if err != nil {
		return nil, fmt.Errorf("use a valid timezone")
	}
	return loc, nil
}

// Validate checks the rule fields without resolving any occurrence.
func (r Rule) Validate() error {
	if r.Recurrence != Daily && r.Recurrence != Weekly {
		return fmt.Errorf("choose how the schedule repeats")
	}
	if _, err := r.Location(); err != nil {
		return err
	}
	if r.Hour < 0 || r.Hour > 23 || r.Minute < 0 || r.Minute > 59 {
		return fmt.Errorf("use a valid schedule time")
	}
	if !r.StartDate.valid() {
		return fmt.Errorf("use a valid start date")
	}
	if !r.EndDate.IsZero() {
		if !r.EndDate.valid() || r.EndDate.Before(r.StartDate) {
			return fmt.Errorf("the end date cannot be before the start date")
		}
	}
	if r.Recurrence == Weekly {
		if len(r.Weekdays) == 0 {
			return fmt.Errorf("choose at least one weekday")
		}
		for _, weekday := range r.Weekdays {
			if weekday < time.Sunday || weekday > time.Saturday {
				return fmt.Errorf("use a valid weekday")
			}
		}
	}
	return nil
}

// MatchesDate reports whether the local date is a scheduled calendar day.
func (r Rule) MatchesDate(date CivilDate) bool {
	if date.Before(r.StartDate) {
		return false
	}
	if !r.EndDate.IsZero() && date.After(r.EndDate) {
		return false
	}
	if r.Recurrence == Daily {
		return true
	}
	t := time.Date(date.Year, date.Month, date.Day, 0, 0, 0, 0, time.UTC)
	for _, weekday := range r.Weekdays {
		if t.Weekday() == weekday {
			return true
		}
	}
	return false
}

// Occurrence is one local calendar day's resolved sending instant.
type Occurrence struct {
	Date      CivilDate
	Instant   time.Time // UTC; for a gap it is the clock's normalized instant
	Gap       bool      // the local wall time does not exist (spring forward)
	Ambiguous bool      // the local wall time occurs twice (fall back)
}

// probeShifts covers every modern daylight-saving offset change (30m on Lord
// Howe Island, 1h almost everywhere, 2h in a few historical zones).
var probeShifts = []time.Duration{
	-2 * time.Hour, -time.Hour, -30 * time.Minute,
	30 * time.Minute, time.Hour, 2 * time.Hour,
}

// Resolve maps one local date to an absolute instant. A wall time the zone
// skips comes back as a Gap; a wall time that occurs twice comes back
// Ambiguous with Instant set to the earlier of the two absolute moments, so
// the date is always delivered at most once.
func (r Rule) Resolve(date CivilDate, loc *time.Location) Occurrence {
	t := time.Date(date.Year, date.Month, date.Day, r.Hour, r.Minute, 0, 0, loc)
	wall := t.In(loc)
	if wall.Year() != date.Year || wall.Month() != date.Month || wall.Day() != date.Day ||
		wall.Hour() != r.Hour || wall.Minute() != r.Minute {
		return Occurrence{Date: date, Instant: t.UTC(), Gap: true}
	}
	matches := []time.Time{t.UTC()}
	for _, shift := range probeShifts {
		other := t.Add(shift).In(loc)
		if other.Year() == date.Year && other.Month() == date.Month && other.Day() == date.Day &&
			other.Hour() == r.Hour && other.Minute() == r.Minute {
			instant := t.Add(shift).UTC()
			matches = appendIfDistinct(matches, instant)
		}
	}
	if len(matches) > 1 {
		sort.Slice(matches, func(i, j int) bool { return matches[i].Before(matches[j]) })
		return Occurrence{Date: date, Instant: matches[0], Ambiguous: true}
	}
	return Occurrence{Date: date, Instant: t.UTC()}
}

func appendIfDistinct(times []time.Time, candidate time.Time) []time.Time {
	for _, existing := range times {
		if existing.Equal(candidate) {
			return times
		}
	}
	return append(times, candidate)
}

// maxScanDays bounds the calendar walk: a weekly rule matches within seven
// days; the extra margin only protects pathological end-date boundaries.
const maxScanDays = 8

// NextMatch returns the first matching date on or after from, truncated by
// the start/end window. False means no matching date remains.
func (r Rule) NextMatch(from CivilDate) (CivilDate, bool) {
	if from.Before(r.StartDate) {
		from = r.StartDate
	}
	if !r.EndDate.IsZero() && from.After(r.EndDate) {
		return CivilDate{}, false
	}
	for i := 0; i <= maxScanDays; i++ {
		date := from.AddDays(i)
		if !r.EndDate.IsZero() && date.After(r.EndDate) {
			return CivilDate{}, false
		}
		if r.MatchesDate(date) {
			return date, true
		}
	}
	return CivilDate{}, false
}

// NextOccurrence returns the resolved occurrence for the first matching date
// on or after from. False means the rule has no occurrence left.
func (r Rule) NextOccurrence(from CivilDate, loc *time.Location) (Occurrence, bool) {
	date, ok := r.NextMatch(from)
	if !ok {
		return Occurrence{}, false
	}
	return r.Resolve(date, loc), true
}

// EncodeWeekdays renders a weekday set as the sorted comma list stored on a
// schedule ("0".."6", Sunday first).
func EncodeWeekdays(weekdays []time.Weekday) string {
	seen := map[time.Weekday]bool{}
	for _, weekday := range weekdays {
		if weekday >= time.Sunday && weekday <= time.Saturday {
			seen[weekday] = true
		}
	}
	parts := make([]string, 0, len(seen))
	for day := time.Sunday; day <= time.Saturday; day++ {
		if seen[day] {
			parts = append(parts, strconv.Itoa(int(day)))
		}
	}
	return strings.Join(parts, ",")
}

// ParseWeekdays parses the stored weekday encoding. An empty string is an
// empty set (invalid for weekly rules).
func ParseWeekdays(value string) ([]time.Weekday, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	seen := map[time.Weekday]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 6 {
			return nil, fmt.Errorf("use a valid weekday")
		}
		seen[time.Weekday(n)] = true
	}
	out := make([]time.Weekday, 0, len(seen))
	for day := time.Sunday; day <= time.Saturday; day++ {
		if seen[day] {
			out = append(out, day)
		}
	}
	return out, nil
}
