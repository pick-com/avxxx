package search

import (
	"regexp"
	"strings"
)

// Class is the AdminSearch triage bucket of a zero-result term.
type Class string

const (
	// ClassNotInLibrary (库里没有): nothing in the catalog resembles the term;
	// the fix is content (补片 / 建预约页).
	ClassNotInLibrary Class = "not_in_library"
	// ClassMissed (库里有没搜到): a title, alias or translation nearly matches,
	// or the modifier-stripped retry finds results; the fix is metadata
	// (同义词 / 别名 / 英文标题).
	ClassMissed Class = "in_library_missed"
	// ClassNotVideo (不是找视频): the user is looking for help, not content;
	// the fix is a redirect to a help page (跳帮助页).
	ClassNotVideo Class = "not_video"
)

// Label is the Chinese label the AdminSearch board shows.
func (c Class) Label() string {
	switch c {
	case ClassMissed:
		return "库里有没搜到"
	case ClassNotVideo:
		return "不是找视频"
	}
	return "库里没有"
}

// Actions are the AdminSearch fixes offered for a class.
func (c Class) Actions() []string {
	switch c {
	case ClassMissed:
		return []string{"add_synonym", "add_alias", "add_english_title"}
	case ClassNotVideo:
		return []string{"add_redirect"}
	}
	return []string{"add_release", "create_upcoming", "add_synonym"}
}

// helpWords mark queries about using the site rather than finding a video.
var helpWords = []string{
	"怎么", "怎樣", "怎样", "如何", "为什么", "為什麼", "为啥", "為啥",
	"下载不了", "下載不了", "怎么下载", "怎麼下載", "看不了", "打不开", "打不開", "播放不了", "加载", "載入", "卡顿", "卡頓", "黑屏", "没声音", "沒聲音",
	"登录", "登錄", "登入", "注册", "註冊", "密码", "密碼", "账号", "帳號", "账户", "帳戶",
	"会员", "會員", "充值", "退款", "付款", "支付", "订阅", "訂閱", "取消订阅",
	"客服", "联系", "聯繫", "投诉", "投訴", "举报", "舉報", "反馈", "反饋", "帮助", "幫助",
	"app", "apk", "客户端", "客戶端", "官网", "官網", "网址", "網址", "地址", "域名", "翻墙", "翻牆", "vpn",
	"设置", "設置", "设定", "字幕怎么", "清晰度怎么", "倍速怎么", "广告", "廣告", "去广告",
}

var helpLatin = regexp.MustCompile(`(^|\s)(how to|how do i|why|login|log in|sign in|sign up|signup|register|password|account|subscription|subscribe|cancel|refund|payment|contact|support|help|faq|vpn|apk|app|ads?|not working|can'?t|cannot)(\s|$)`)

// IsHelpQuery reports whether a normalized query reads like a help request
// (怎么下载, 会员充值, how to download …) rather than a content search.
func IsHelpQuery(q string) bool {
	q = NormalizeQuery(q)
	if q == "" {
		return false
	}
	for _, w := range helpWords {
		if isLatin(w) {
			if strings.Contains(" "+q+" ", " "+w+" ") {
				return true
			}
			continue
		}
		if strings.Contains(q, w) {
			return true
		}
	}
	return helpLatin.MatchString(q)
}

// Classify triages a zero-result term: help-like terms are ClassNotVideo;
// terms with a catalog near-match (title / alias / translation) or whose
// modifier-stripped retry finds results are ClassMissed; the rest are
// ClassNotInLibrary.
func Classify(term string, nearMatch, wouldRescue bool) Class {
	switch {
	case IsHelpQuery(term):
		return ClassNotVideo
	case nearMatch || wouldRescue:
		return ClassMissed
	}
	return ClassNotInLibrary
}
