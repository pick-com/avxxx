package adminauth

import (
	"slices"
	"sort"
)

// Role is one of the seven back-office roles of artboard AdminUsers (tech
// plan 3.13 admin_roles: 超级管理员、内容编辑、入库运维、审核员、运营、广告销售、
// 外部译者). PRD 11 listed five; DESIGN_ALIGNMENT #1 settles on seven.
type Role string

const (
	RoleSuperAdmin Role = "super_admin" // 超级管理员
	RoleEditor     Role = "content_editor"
	RoleIngestOps  Role = "ingest_ops"
	RoleModerator  Role = "moderator"
	RoleOperations Role = "operations"
	RoleAdSales    Role = "ad_sales"
	RoleTranslator Role = "translator"
)

// Roles lists every role in display order.
var Roles = []Role{RoleSuperAdmin, RoleEditor, RoleIngestOps, RoleModerator, RoleOperations, RoleAdSales, RoleTranslator}

// RoleLabels are the 中文 names shown in the back office.
var RoleLabels = map[Role]string{
	RoleSuperAdmin: "超级管理员",
	RoleEditor:     "内容编辑",
	RoleIngestOps:  "入库运维",
	RoleModerator:  "审核员",
	RoleOperations: "运营",
	RoleAdSales:    "广告销售",
	RoleTranslator: "外部译者",
}

// ValidRole reports whether r is one of the seven roles.
func ValidRole(r string) bool { return slices.Contains(Roles, Role(r)) }

// Group is a set of admin routes that share an access rule (one row of the
// role matrix). Routes are mapped to a group and an access level in
// internal/api/admin_perms.go.
type Group string

const (
	GroupDashboard  Group = "dashboard"   // 总览、片源健康、完整性报告、SEO 报告
	GroupCatalog    Group = "catalog"     // 视频、演员、重复与归并、字段冲突、拆分、播放曲线
	GroupQuality    Group = "quality"     // 数据质量（夜间审计问题）
	GroupIngest     Group = "ingest"      // 来源、任务、抓取策略、字段优先级、片源映射、图片、推荐重建
	GroupModeration Group = "moderation"  // 举报与审核、敏感词、禁言
	GroupSiteUsers  Group = "site_users"  // 前台用户检索与封禁
	GroupCompliance Group = "compliance"  // 版权投诉（takedowns）、用户反馈
	GroupLegal      Group = "legal"       // 法律文本（条款、隐私政策）起草与发布
	GroupOps        Group = "ops"         // 首页排期与公告
	GroupSearch     Group = "search"      // 搜索词情报
	GroupI18n       Group = "i18n"        // 翻译、术语表、界面文字
	GroupAds        Group = "ads"         // 广告（后台尚无路由，先占位）
	GroupAdminUsers Group = "admin_users" // 后台账号与角色
	GroupAuditLog   Group = "audit_log"   // 操作日志
	// GroupSelf is every signed-in admin: own session, the role matrix and
	// the approvals queue (approving re-checks the target route's group).
	GroupSelf Group = "self"
)

// Groups lists the matrix rows in display order (GroupSelf excluded).
var Groups = []Group{GroupDashboard, GroupCatalog, GroupQuality, GroupIngest, GroupModeration, GroupSiteUsers,
	GroupCompliance, GroupLegal, GroupOps, GroupSearch, GroupI18n, GroupAds, GroupAdminUsers, GroupAuditLog}

// GroupLabels are the 中文 row names of the matrix view.
var GroupLabels = map[Group]string{
	GroupDashboard:  "总览与报告",
	GroupCatalog:    "视频与演员",
	GroupQuality:    "数据质量",
	GroupIngest:     "入库与数据治理",
	GroupModeration: "举报与审核",
	GroupSiteUsers:  "前台用户与处罚",
	GroupCompliance: "版权投诉与反馈",
	GroupLegal:      "法律文本",
	GroupOps:        "首页排期与公告",
	GroupSearch:     "搜索词情报",
	GroupI18n:       "翻译管理",
	GroupAds:        "广告",
	GroupAdminUsers: "后台账号与角色",
	GroupAuditLog:   "操作日志",
}

// Access is a level on a group: none < read < write.
type Access int

const (
	None Access = iota
	Read
	Write
)

func (a Access) String() string {
	switch a {
	case Read:
		return "read"
	case Write:
		return "write"
	}
	return "none"
}

// matrix is role → group → access. super_admin is implicit Write on every
// group, including groups added later.
var matrix = map[Role]map[Group]Access{
	RoleEditor: {
		GroupDashboard: Read, GroupCatalog: Write, GroupQuality: Write, GroupIngest: Read,
		GroupOps: Read, GroupSearch: Write, GroupI18n: Write,
	},
	RoleIngestOps: {
		GroupDashboard: Read, GroupCatalog: Read, GroupQuality: Write, GroupIngest: Write,
	},
	RoleModerator: {
		GroupDashboard: Read, GroupCatalog: Read, GroupModeration: Write, GroupSiteUsers: Write,
		GroupCompliance: Write, GroupLegal: Read,
	},
	RoleOperations: {
		GroupDashboard: Read, GroupCatalog: Read, GroupModeration: Read, GroupOps: Write,
		GroupSearch: Write, GroupI18n: Read, GroupAds: Read, GroupCompliance: Read, GroupLegal: Read,
	},
	RoleAdSales: {
		GroupDashboard: Read, GroupOps: Read, GroupAds: Write,
	},
	RoleTranslator: {
		GroupI18n: Write,
	},
}

// RoleAccess is the access a single role has on a group.
func RoleAccess(r Role, g Group) Access {
	if r == RoleSuperAdmin || g == GroupSelf {
		return Write
	}
	return matrix[r][g]
}

// AccessOf is the highest access any of roles has on g. Unknown role names
// grant nothing.
func AccessOf(roles []string, g Group) Access {
	best := None
	for _, r := range roles {
		if !ValidRole(r) {
			continue
		}
		if a := RoleAccess(Role(r), g); a > best {
			best = a
		}
	}
	return best
}

// IsSuper reports whether roles include super_admin.
func IsSuper(roles []string) bool { return slices.Contains(roles, string(RoleSuperAdmin)) }

// Permissions lists "group:read" / "group:write" for roles (sorted), for the
// UI to hide what the admin cannot open.
func Permissions(roles []string) []string {
	var out []string
	for _, g := range Groups {
		switch AccessOf(roles, g) {
		case Read:
			out = append(out, string(g)+":read")
		case Write:
			out = append(out, string(g)+":read", string(g)+":write")
		}
	}
	sort.Strings(out)
	return out
}

// MatrixRow is one group of the matrix view: access per role.
type MatrixRow struct {
	Group  Group             `json:"group"`
	Label  string            `json:"label"`
	Access map[string]string `json:"access"`
}

// Matrix returns the full role matrix for the UI.
func Matrix() []MatrixRow {
	out := make([]MatrixRow, 0, len(Groups))
	for _, g := range Groups {
		row := MatrixRow{Group: g, Label: GroupLabels[g], Access: map[string]string{}}
		for _, r := range Roles {
			row.Access[string(r)] = RoleAccess(r, g).String()
		}
		out = append(out, row)
	}
	return out
}

// NormalizeRoles validates, dedupes and orders a role list.
func NormalizeRoles(in []string) ([]string, bool) {
	seen := map[string]bool{}
	for _, r := range in {
		if !ValidRole(r) {
			return nil, false
		}
		seen[r] = true
	}
	out := []string{}
	for _, r := range Roles {
		if seen[string(r)] {
			out = append(out, string(r))
		}
	}
	return out, true
}
