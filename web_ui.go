package main

import "fmt"

// humanSize 把字节数格式化为可读字符串
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// stateText 把 qB 状态翻译为中文
func stateText(s string) string {
	m := map[string]string{
		"downloading": "下载中", "metaDL": "获取元数据", "forcedMetaDL": "获取元数据",
		"stalledDL": "等待下载", "forcedDL": "下载中",
		"uploading": "做种中", "stalledUP": "做种中", "forcedUP": "做种中",
		"pausedDL": "已暂停", "pausedUP": "已暂停", "stoppedDL": "已暂停", "stoppedUP": "已暂停",
		"queuedDL": "排队中", "queuedUP": "排队中",
		"checkingDL": "校验中", "checkingUP": "校验中", "checkingResumeData": "校验中",
		"moving": "移动中", "error": "错误", "missingFiles": "文件丢失",
	}
	if t, ok := m[s]; ok {
		return t
	}
	return s
}

// prioText 把文件优先级数值翻译为中文
func prioText(p int) string {
	switch p {
	case prioSkip:
		return "跳过"
	case prioNormal:
		return "正常"
	case prioHigh:
		return "高"
	case prioMax:
		return "最高"
	default:
		return fmt.Sprintf("%d", p)
	}
}
