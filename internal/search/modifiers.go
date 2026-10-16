package search

import (
	"regexp"
	"sort"
	"strings"
)

// Modifiers are the common words users add around a title that never appear
// in catalog data (PRD 4.5 第 4 条: 全集 / 第 N 集 / 在线看 / 高清 / 演员表 …).
// They are stripped only for the retry after a zero-result search. CJK
// modifiers are removed wherever they occur; Latin ones only as whole
// words (so "hd" never eats "hdka-123"). All entries are in NormalizeQuery
// form; longer entries win over their substrings.
var Modifiers = []string{
	// 集数 / 全集
	"全集", "全集完整版", "完整版", "完整", "未删减", "未删減", "无删减", "無刪減", "全部",
	"大结局", "大結局", "结局", "結局",
	// 观看方式
	"在线观看", "在線觀看", "在线看", "在線看", "在线播放", "在線播放", "线上看", "線上看", "在线",
	"免费观看", "免費觀看", "免费看", "免費看", "免费", "免費", "观看", "觀看", "播放",
	"下载", "下載", "种子", "種子", "磁力", "迅雷", "网盘", "網盤", "百度云", "百度雲",
	// 清晰度 / 字幕
	"高清", "超清", "蓝光", "藍光", "无水印", "無水印",
	"中文字幕", "中字", "字幕版", "字幕", "中文", "国语", "國語", "日语", "日語",
	// 其他
	"演员表", "演員表", "演员", "演員", "剧情", "劇情", "简介", "簡介", "资源", "資源", "合集",
	"最新", "视频", "視頻", "影片",
	// 日本語
	"無料", "動画", "フル", "高画質", "字幕付き",
	// English / romanized (whole words only)
	"4k", "8k", "1080p", "720p", "hd", "fhd", "uhd", "full", "full movie", "online", "watch", "watch online",
	"free", "stream", "streaming", "download", "eng sub", "english sub", "english subtitles", "sub", "subbed",
	"uncut", "complete", "cast",
}

// episodePattern matches 第 N 集 / 第N話 / 第三季 / EP3 / E03 style suffixes.
var episodePattern = regexp.MustCompile(`第\s*[0-9零一二三四五六七八九十百两兩]+\s*[集话話部季期回章]|(?:^|\s)(?:ep|e|episode|part|season|s)\s?[0-9]{1,3}(?:\s|$)|[0-9]{1,3}\s*[集话話]`)

var (
	cjkModifiers   []string
	latinModifiers []string
)

func init() {
	for _, m := range Modifiers {
		if isLatin(m) {
			latinModifiers = append(latinModifiers, m)
		} else {
			cjkModifiers = append(cjkModifiers, m)
		}
	}
	byLen := func(s []string) {
		sort.SliceStable(s, func(i, j int) bool { return len(s[i]) > len(s[j]) })
	}
	byLen(cjkModifiers)
	byLen(latinModifiers)
}

func isLatin(s string) bool {
	for _, r := range s {
		if r > 0x7f {
			return false
		}
	}
	return true
}

// StripModifiers removes modifier words and episode markers from a query
// and returns the remaining normalized query and whether anything was
// removed. When nothing meaningful is left (fewer than MinQueryRunes runes)
// it returns ("", false): a retry with an empty query makes no sense.
func StripModifiers(q string) (string, bool) {
	q = NormalizeQuery(q)
	if q == "" {
		return "", false
	}
	out := episodePattern.ReplaceAllString(q, " ")
	for _, m := range cjkModifiers {
		out = strings.ReplaceAll(out, m, " ")
	}
	// Latin modifiers: whole space-separated words (multi-word ones first).
	padded := " " + strings.Join(strings.Fields(out), " ") + " "
	for _, m := range latinModifiers {
		for strings.Contains(padded, " "+m+" ") {
			padded = strings.ReplaceAll(padded, " "+m+" ", " ")
		}
	}
	out = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch r {
		case '+', '|', '/', ',', '，', '、', '·', '・', '(', ')', '（', '）', '[', ']', '【', '】', '《', '》', '"', '“', '”':
			return ' '
		}
		return r
	}, padded)), " ")
	if out == q || !Meaningful(out) {
		return "", false
	}
	return out, true
}
