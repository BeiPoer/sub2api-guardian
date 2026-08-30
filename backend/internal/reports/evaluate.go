package reports

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/upstream"
)

type summaryKey struct {
	group   string
	account string
}

type summaryCounter struct {
	key   summaryKey
	total int
	high  int
}

// Evaluate 按闭区间过滤 usage records，并按分组名和账号名汇总。
func Evaluate(records []upstream.UsageRecord, start, end time.Time, location *time.Location, thresholdMS float64, triggerCount int) Evaluation {
	counters := make(map[summaryKey]*summaryCounter)
	totalRecords := 0
	highLatencyCount := 0
	for _, record := range records {
		createdAt, ok := upstream.ParseUsageTime(record.CreatedAt, location)
		if !ok || createdAt.Before(start) || createdAt.After(end) {
			continue
		}
		key := summaryKey{
			group:   usageName(record.Group, record.GroupID, "未知分组"),
			account: usageName(record.Account, record.AccountID, "未知账号"),
		}
		counter := counters[key]
		if counter == nil {
			counter = &summaryCounter{key: key}
			counters[key] = counter
		}
		counter.total++
		totalRecords++
		if firstTokenMS, ok := finiteNumber(record.FirstTokenMS); ok && firstTokenMS > thresholdMS {
			counter.high++
			highLatencyCount++
		}
	}

	rows := make([]SummaryRow, 0, len(counters))
	for _, counter := range counters {
		rows = append(rows, SummaryRow{
			GroupName: counter.key.group, AccountName: counter.key.account,
			HighLatencyCount: counter.high, TotalRecords: counter.total,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].HighLatencyCount != rows[j].HighLatencyCount {
			return rows[i].HighLatencyCount > rows[j].HighLatencyCount
		}
		if rows[i].GroupName != rows[j].GroupName {
			return rows[i].GroupName < rows[j].GroupName
		}
		return rows[i].AccountName < rows[j].AccountName
	})
	return Evaluation{
		TotalRecords: totalRecords, HighLatencyCount: highLatencyCount,
		Alert: highLatencyCount > triggerCount, Rows: rows,
	}
}

func usageName(value, fallbackID any, unknown string) string {
	if record, ok := value.(map[string]any); ok {
		for _, key := range []string{"name", "Name", "label", "title"} {
			if name := strings.TrimSpace(stringValue(record[key])); name != "" {
				return name
			}
		}
		if fallbackID == nil {
			fallbackID = record["id"]
		}
	} else if name := strings.TrimSpace(stringValue(value)); name != "" {
		return name
	}
	if id := strings.TrimSpace(stringValue(fallbackID)); id != "" && id != "0" {
		return id
	}
	return unknown
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == math.Trunc(typed) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		if typed == float32(math.Trunc(float64(typed))) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(typed)
	}
}

func finiteNumber(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(stringValue(value)), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}
