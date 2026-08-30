package store

import (
	"context"
	"time"
)

// BuildReport aggregates finished time entries by project and counts time-off days
// within [fromMs, toMs). Running timers are not included.
//
// tzOffsetMin is the caller's offset from UTC in minutes, used only to turn the
// millisecond window into the calendar days the time-off rows are stored as. Entries
// are filtered by timestamp and need no timezone at all; time off is a date range, and
// deciding which dates a window covers is impossible without one. Zero means UTC.
func (s *Store) BuildReport(ctx context.Context, userID string, fromMs, toMs int64, tzOffsetMin int) (Report, error) {
	report := Report{Projects: []ProjectReport{}, TimeOff: []TimeOffReport{}}

	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, SUM(MAX(0, stopped_at - started_at)) AS total_ms
		FROM time_entries
		WHERE user_id = ? AND deleted_at IS NULL AND stopped_at IS NOT NULL
		  AND started_at >= ? AND started_at < ?
		GROUP BY project_id ORDER BY total_ms DESC`, userID, fromMs, toMs)
	if err != nil {
		return Report{}, err
	}
	for rows.Next() {
		var item ProjectReport
		if err := rows.Scan(&item.ProjectID, &item.TotalMs); err != nil {
			rows.Close()
			return Report{}, err
		}
		report.Projects = append(report.Projects, item)
	}
	if err := closeRows(rows); err != nil {
		return Report{}, err
	}

	// The window is half-open, but date_from/date_to are inclusive, so the last date is
	// the one containing the final millisecond of the window. Formatting toMs itself
	// would reach one day too far and count a vacation that starts the day after the
	// range as a day inside it.
	offset := time.Duration(tzOffsetMin) * time.Minute
	fromDate := time.UnixMilli(fromMs).UTC().Add(offset).Format(time.DateOnly)
	toDate := time.UnixMilli(toMs - 1).UTC().Add(offset).Format(time.DateOnly)
	offRows, err := s.db.QueryContext(ctx, `
		SELECT kind, date_from, date_to FROM time_off
		WHERE user_id = ? AND deleted_at IS NULL AND date_to >= ? AND date_from <= ?`,
		userID, fromDate, toDate)
	if err != nil {
		return Report{}, err
	}
	daysByKind := map[string]int{}
	for offRows.Next() {
		var kind, rangeFrom, rangeTo string
		if err := offRows.Scan(&kind, &rangeFrom, &rangeTo); err != nil {
			offRows.Close()
			return Report{}, err
		}
		daysByKind[kind] += overlapDays(rangeFrom, rangeTo, fromDate, toDate)
	}
	if err := closeRows(offRows); err != nil {
		return Report{}, err
	}
	for _, kind := range []string{"vacation", "sick", "dayoff"} {
		if days := daysByKind[kind]; days > 0 {
			report.TimeOff = append(report.TimeOff, TimeOffReport{Kind: kind, Days: days})
		}
	}
	return report, nil
}

// overlapDays counts inclusive days in the intersection of two YYYY-MM-DD ranges.
// Dates are already validated on write, so parse errors collapse the range to zero.
func overlapDays(fromA, toA, fromB, toB string) int {
	from := maxDate(fromA, fromB)
	to := minDate(toA, toB)
	fromTime, errFrom := time.Parse(time.DateOnly, from)
	toTime, errTo := time.Parse(time.DateOnly, to)
	if errFrom != nil || errTo != nil || toTime.Before(fromTime) {
		return 0
	}
	return int(toTime.Sub(fromTime).Hours()/24) + 1
}

func maxDate(left, right string) string {
	if left > right {
		return left
	}
	return right
}

func minDate(left, right string) string {
	if left < right {
		return left
	}
	return right
}
