package reports

import (
	"fmt"
	"strings"
	"time"
)

const maxWeComTextBytes = 3500

func buildAlertText(evaluation Evaluation, startedAt, windowStart, windowEnd time.Time, location *time.Location, thresholdMS int64, triggerCount int) string {
	threshold := formatThreshold(thresholdMS)
	var builder strings.Builder
	builder.WriteString("渠道使用报告：首 T 高延迟告警\n")
	fmt.Fprintf(&builder, "执行时间：%s\n", formatReportTime(startedAt, location))
	fmt.Fprintf(&builder, "统计窗口：%s 至 %s\n", formatReportTime(windowStart, location), formatReportTime(windowEnd, location))
	fmt.Fprintf(&builder, "高延迟总数：%d 条\n", evaluation.HighLatencyCount)
	fmt.Fprintf(&builder, "首 T 阈值：%s\n", threshold)
	fmt.Fprintf(&builder, "触发条数：超过 %d 条\n\n", triggerCount)
	builder.WriteString("高延迟明细（高延迟数 / 总记录数）：\n")
	visible := 0
	for _, row := range evaluation.Rows {
		if row.HighLatencyCount <= 0 {
			continue
		}
		if visible >= 40 {
			break
		}
		visible++
		fmt.Fprintf(&builder, "%d. %s / %s：%d / %d\n", visible, textCell(row.GroupName), textCell(row.AccountName), row.HighLatencyCount, row.TotalRecords)
	}
	highRows := 0
	for _, row := range evaluation.Rows {
		if row.HighLatencyCount > 0 {
			highRows++
		}
	}
	if highRows > visible {
		fmt.Fprintf(&builder, "\n还有 %d 条明细未展开，完整聚合结果请查看运行记录。\n", highRows-visible)
	}
	return limitText(builder.String())
}

func buildFailureText(startedAt time.Time, location *time.Location, err error) string {
	message := "报告查询失败"
	if err != nil {
		message = truncateText(err.Error(), 600)
	}
	return limitText(fmt.Sprintf("渠道使用报告执行失败\n执行时间：%s\n错误：%s\n", formatReportTime(startedAt, location), textCell(message)))
}

func buildTestText(now time.Time, location *time.Location) string {
	return fmt.Sprintf("渠道使用报告企微测试\n发送时间：%s\n当前配置可以发送普通文本消息。", formatReportTime(now, location))
}

func formatReportTime(value time.Time, location *time.Location) string {
	if location != nil {
		value = value.In(location)
	}
	return value.Format("2006-01-02 15:04:05 MST")
}

func formatThreshold(value int64) string {
	if value%1000 == 0 {
		return fmt.Sprintf("%d 秒", value/1000)
	}
	return fmt.Sprintf("%.3g 秒", float64(value)/1000)
}

func textCell(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(strings.TrimSpace(value))
	if value == "" {
		return "-"
	}
	return value
}

func limitText(value string) string {
	if len([]byte(value)) <= maxWeComTextBytes {
		return value
	}
	suffix := "\n消息内容已截断，完整聚合结果请查看运行记录。"
	runes := []rune(value)
	for len([]byte(string(runes)))+len([]byte(suffix)) > maxWeComTextBytes && len(runes) > 0 {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + suffix
}

func truncateText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
