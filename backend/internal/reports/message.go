package reports

import (
	"fmt"
	"strings"
	"time"
)

const maxWeComMarkdownBytes = 3500

func buildAlertMarkdown(evaluation Evaluation, startedAt, windowStart, windowEnd time.Time, location *time.Location, thresholdMS int64, triggerCount int) string {
	threshold := formatThreshold(thresholdMS)
	var builder strings.Builder
	builder.WriteString("## 渠道使用报告：首 T 高延迟告警\n")
	fmt.Fprintf(&builder, "> 执行时间：%s\n", formatReportTime(startedAt, location))
	fmt.Fprintf(&builder, "> 统计窗口：%s 至 %s\n", formatReportTime(windowStart, location), formatReportTime(windowEnd, location))
	fmt.Fprintf(&builder, "> 高延迟总数：**%d** 条（首 T > %s，触发条数 > %d）\n\n", evaluation.HighLatencyCount, threshold, triggerCount)
	builder.WriteString("| 分组 | 账号 | 首 T 超阈值 / 总记录 |\n|---|---|---:|\n")
	visible := 0
	for _, row := range evaluation.Rows {
		if row.HighLatencyCount <= 0 {
			continue
		}
		if visible >= 40 {
			break
		}
		fmt.Fprintf(&builder, "| %s | %s | %d / %d |\n", markdownCell(row.GroupName), markdownCell(row.AccountName), row.HighLatencyCount, row.TotalRecords)
		visible++
	}
	highRows := 0
	for _, row := range evaluation.Rows {
		if row.HighLatencyCount > 0 {
			highRows++
		}
	}
	if highRows > visible {
		fmt.Fprintf(&builder, "\n> 还有 %d 条明细未展开，完整聚合结果请查看运行记录。\n", highRows-visible)
	}
	return limitMarkdown(builder.String())
}

func buildFailureMarkdown(startedAt time.Time, location *time.Location, err error) string {
	message := "报告查询失败"
	if err != nil {
		message = truncateText(err.Error(), 600)
	}
	return limitMarkdown(fmt.Sprintf("## 渠道使用报告执行失败\n> 执行时间：%s\n> 错误：%s\n", formatReportTime(startedAt, location), markdownCell(message)))
}

func buildTestMarkdown(now time.Time, location *time.Location) string {
	return fmt.Sprintf("## 渠道使用报告企微测试\n> 发送时间：%s\n> 当前配置可以发送 Markdown 消息。", formatReportTime(now, location))
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

func markdownCell(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ", "|", "／", "`", "'").Replace(value)
	if value == "" {
		return "-"
	}
	return value
}

func limitMarkdown(value string) string {
	if len([]byte(value)) <= maxWeComMarkdownBytes {
		return value
	}
	suffix := "\n> 消息内容已截断，完整聚合结果请查看运行记录。"
	runes := []rune(value)
	for len([]byte(string(runes)))+len([]byte(suffix)) > maxWeComMarkdownBytes && len(runes) > 0 {
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
