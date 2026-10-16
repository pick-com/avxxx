package notify

// Transactional account mail (password reset). These outbox rows bypass
// notify prefs and quiet hours, are never merged, and carry no secret: the
// payload is {"kind":"password_reset"} and the single-use token is minted
// (and only its hash stored) right before sending, so a retry sends a fresh
// link and nothing in the outbox can be replayed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	htmltemplate "html/template"
	"net/url"
	"strings"
	texttemplate "text/template"
	"time"

	"avxxx/internal/mailer"
	"avxxx/internal/store"
)

// ResetPath is the web page that consumes a reset token (in the fragment, so
// it never reaches a server log or a Referer header).
const ResetPath = "/reset-password"

// ResetIssuer mints password reset tokens (*store.Store).
type ResetIssuer interface {
	IssuePasswordReset(ctx context.Context, userID int64) (string, time.Time, error)
}

type resetData struct {
	Name, Link string
	Minutes    int
}

type accountTemplate struct {
	subject string
	text    *texttemplate.Template
	html    *htmltemplate.Template
}

var resetTemplates = map[string]accountTemplate{
	"zh-CN": {
		subject: "重置你的 REEL 密码",
		text: texttemplate.Must(texttemplate.New("t").Parse(`你好{{if .Name}}，{{.Name}}{{end}}：

我们收到了重置 REEL 账号密码的请求。点击下面的链接设置新密码（{{.Minutes}} 分钟内有效，只能使用一次）：

{{.Link}}

如果不是你本人操作，忽略这封邮件即可，你的密码不会改变。
`)),
		html: htmltemplate.Must(htmltemplate.New("h").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>重置你的 REEL 密码</title></head>
<body style="font-family:-apple-system,Helvetica,Arial,sans-serif;color:#222;line-height:1.6">
<p>你好{{if .Name}}，{{.Name}}{{end}}：</p>
<p>我们收到了重置 REEL 账号密码的请求。点击下面的按钮设置新密码（{{.Minutes}} 分钟内有效，只能使用一次）：</p>
<p><a href="{{.Link}}" style="display:inline-block;padding:10px 18px;background:#e50914;color:#fff;border-radius:6px;text-decoration:none">设置新密码</a></p>
<p style="color:#888;font-size:12px">如果不是你本人操作，忽略这封邮件即可，你的密码不会改变。</p>
</body></html>`)),
	},
	"en": {
		subject: "Reset your REEL password",
		text: texttemplate.Must(texttemplate.New("t").Parse(`Hi{{if .Name}} {{.Name}}{{end}},

We received a request to reset the password of your REEL account. Use this link to choose a new one (valid for {{.Minutes}} minutes, single use):

{{.Link}}

If you did not ask for this, ignore this email; your password stays the same.
`)),
		html: htmltemplate.Must(htmltemplate.New("h").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Reset your REEL password</title></head>
<body style="font-family:-apple-system,Helvetica,Arial,sans-serif;color:#222;line-height:1.6">
<p>Hi{{if .Name}} {{.Name}}{{end}},</p>
<p>We received a request to reset the password of your REEL account. Use the button below to choose a new one (valid for {{.Minutes}} minutes, single use):</p>
<p><a href="{{.Link}}" style="display:inline-block;padding:10px 18px;background:#e50914;color:#fff;border-radius:6px;text-decoration:none">Choose a new password</a></p>
<p style="color:#888;font-size:12px">If you did not ask for this, ignore this email; your password stays the same.</p>
</body></html>`)),
	},
}

// RenderPasswordReset renders the reset mail for a ui_lang (zh* -> zh-CN,
// everything else English).
func (r *Renderer) RenderPasswordReset(uiLang, name, token string, ttl time.Duration) (mailer.Message, error) {
	lang := "en"
	if strings.HasPrefix(uiLang, "zh") {
		lang = "zh-CN"
	}
	t := resetTemplates[lang]
	data := resetData{Name: name, Link: r.link(ResetPath + "#token=" + url.QueryEscape(token)), Minutes: int(ttl / time.Minute)}
	var tb, hb bytes.Buffer
	if err := t.text.Execute(&tb, data); err != nil {
		return mailer.Message{}, err
	}
	if err := t.html.Execute(&hb, data); err != nil {
		return mailer.Message{}, err
	}
	return mailer.Message{Subject: t.subject, Text: tb.String(), HTML: hb.String()}, nil
}

// payloadKind reads payload.kind without requiring items.
func payloadKind(raw []byte) string {
	var p struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Kind
}

func (w *Worker) deliverPasswordReset(ctx context.Context, it store.OutboxItem) store.OutboxResult {
	if it.Channel != "email" {
		return failed("password reset on channel " + it.Channel)
	}
	// A reset requested long ago is no longer wanted.
	if age := w.now().Sub(it.CreatedAt); age > store.PasswordResetTTL {
		return cancelled("stale password reset request")
	}
	if it.Email == "" {
		return cancelled("user has no email address")
	}
	if w.Mail == nil {
		return w.retry(it, "email not configured", 0)
	}
	issuer, ok := w.Store.(ResetIssuer)
	if !ok {
		return failed("store cannot issue reset tokens")
	}
	token, _, err := issuer.IssuePasswordReset(ctx, it.UserID)
	if err != nil {
		return w.retry(it, "issue reset token: "+err.Error(), 0)
	}
	msg, err := w.Renderer.RenderPasswordReset(it.Settings.UILang, it.DisplayName, token, store.PasswordResetTTL)
	if err != nil {
		return failed("render: " + err.Error())
	}
	msg.To = it.Email
	err = w.Mail.Send(ctx, msg)
	switch {
	case err == nil:
		return store.OutboxResult{Status: store.OutboxSent}
	case mailer.IsPermanent(err):
		return failed("smtp: " + err.Error())
	case errors.Is(err, context.Canceled):
		return w.retry(it, "interrupted", 0)
	}
	return w.retry(it, "smtp: "+err.Error(), 0)
}
