#!/bin/sh
# Render the Alertmanager configuration from environment variables and start
# Alertmanager (docker compose --profile monitoring up -d alertmanager).
#
# Alertmanager does not expand environment variables in its config file, so
# this entrypoint writes one at start-up. Secrets (webhook URLs, the SMTP
# password, bot tokens) are written to 0600 files next to the rendered config
# and referenced with *_file fields; nothing secret lives in the repository or
# in the rendered YAML. POSIX sh: it runs under the image's busybox and under
# scripts/check-alerts.sh.
#
# Receivers (docs/PRODUCTION_OPERATIONS.md 值班通知):
#   pager   severity=page    ALERT_WEBHOOK_URL, email to ALERT_EMAIL_TO, chat
#   ticket  severity=warning ALERT_WARNING_WEBHOOK_URL, email to
#                            ALERT_WARNING_EMAIL_TO (default ALERT_EMAIL_TO), chat
#   null    severity=info    nothing (visible in the Alertmanager / Prometheus UI)
# Chat (optional): ALERT_SLACK_WEBHOOK_URL (Slack or a Slack-compatible incoming
# webhook, e.g. Mattermost) and/or ALERT_TELEGRAM_BOT_TOKEN + ALERT_TELEGRAM_CHAT_ID.
# Email reuses the notifier's SMTP_HOST / SMTP_PORT / SMTP_USER / SMTP_PASSWORD
# and MAIL_FROM (ALERT_EMAIL_FROM overrides the sender).
#
# With nothing configured every receiver is empty: Alertmanager still starts,
# alerts are visible in its UI, and a warning is logged.
#
# Usage: render-config.sh [--render-only] [alertmanager flags...]
#   AM_CONFIG_DIR  where the config and secret files are written
#                  (default /run/alertmanager, a tmpfs in compose)
set -eu

out_dir="${AM_CONFIG_DIR:-/run/alertmanager}"
cfg="$out_dir/alertmanager.yml"
render_only=0
if [ "${1:-}" = "--render-only" ]; then
	render_only=1
	shift
fi

log() { printf '%s level=%s component=render-config msg="%s"\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$2" >&2; }

# YAML single-quoted scalar: double every single quote.
q() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"; }

# Write a secret to a 0600 file and print its path.
secret() {
	(umask 077 && printf '%s' "$2" >"$out_dir/$1")
	printf '%s' "$out_dir/$1"
}

mkdir -p "$out_dir"
umask 077

webhook_url="${ALERT_WEBHOOK_URL:-}"
warning_webhook_url="${ALERT_WARNING_WEBHOOK_URL:-}"
email_to="${ALERT_EMAIL_TO:-}"
warning_email_to="${ALERT_WARNING_EMAIL_TO:-$email_to}"
email_from="${ALERT_EMAIL_FROM:-${MAIL_FROM:-}}"
smtp_host="${SMTP_HOST:-}"
smtp_port="${SMTP_PORT:-587}"
slack_url="${ALERT_SLACK_WEBHOOK_URL:-}"
tg_token="${ALERT_TELEGRAM_BOT_TOKEN:-}"
tg_chat="${ALERT_TELEGRAM_CHAT_ID:-}"
external_url="${ALERTMANAGER_EXTERNAL_URL:-http://127.0.0.1:9096}"

if [ -n "$tg_token" ] && ! printf '%s' "$tg_chat" | grep -Eq '^-?[0-9]+$'; then
	log warn "ALERT_TELEGRAM_BOT_TOKEN is set but ALERT_TELEGRAM_CHAT_ID is not a numeric chat id; Telegram disabled"
	tg_token=""
fi

email_ok=0
if [ -n "$email_to$warning_email_to" ]; then
	if [ -n "$smtp_host" ] && [ -n "$email_from" ]; then
		email_ok=1
	else
		log warn "ALERT_EMAIL_TO is set but SMTP_HOST or MAIL_FROM/ALERT_EMAIL_FROM is empty; email disabled"
	fi
fi

# --- secrets -----------------------------------------------------------------
rm -f "$out_dir"/*.secret
[ -n "$webhook_url" ] && webhook_file="$(secret webhook.secret "$webhook_url")"
[ -n "$warning_webhook_url" ] && warning_webhook_file="$(secret warning-webhook.secret "$warning_webhook_url")"
[ -n "$slack_url" ] && slack_file="$(secret slack.secret "$slack_url")"
[ -n "$tg_token" ] && tg_file="$(secret telegram.secret "$tg_token")"
[ "$email_ok" = 1 ] && [ -n "${SMTP_PASSWORD:-}" ] && smtp_pw_file="$(secret smtp-password.secret "$SMTP_PASSWORD")"

# --- receiver blocks ---------------------------------------------------------
# $1 = receiver name, $2 = webhook secret file, $3 = email recipient,
# $4 = "page" or "ticket" (wording only)
receiver() {
	name="$1" hook="$2" to="$3" kind="$4"
	printf '  - name: %s\n' "$name"
	if [ -n "$hook" ]; then
		cat <<EOF
    webhook_configs:
      - url_file: $(q "$hook")
        send_resolved: true
        max_alerts: 20
EOF
	fi
	if [ "$email_ok" = 1 ] && [ -n "$to" ]; then
		cat <<EOF
    email_configs:
      - to: $(q "$to")
        send_resolved: true
        headers:
          Subject: '[avxxx $kind] {{ .Status | toUpper }}{{ if eq .Status "firing" }}:{{ .Alerts.Firing | len }}{{ end }} {{ .CommonLabels.alertname }}'
EOF
	fi
	if [ -n "$slack_url" ]; then
		cat <<EOF
    slack_configs:
      - api_url_file: $(q "$slack_file")
        send_resolved: true
        title: '[avxxx $kind] {{ .Status | toUpper }} {{ .CommonLabels.alertname }}'
        text: '{{ range .Alerts }}{{ .Annotations.summary }}{{ "\\n" }}{{ end }}'
EOF
	fi
	if [ -n "$tg_token" ]; then
		cat <<EOF
    telegram_configs:
      - bot_token_file: $(q "$tg_file")
        chat_id: $tg_chat
        send_resolved: true
        parse_mode: ''
        message: '[avxxx $kind] {{ .Status | toUpper }} {{ .CommonLabels.alertname }}{{ range .Alerts }}{{ "\\n" }}- {{ .Annotations.summary }}{{ end }}'
EOF
	fi
}

global_block() {
	echo "global:"
	echo "  resolve_timeout: 5m"
	if [ "$email_ok" = 1 ]; then
		echo "  smtp_smarthost: $(q "$smtp_host:$smtp_port")"
		echo "  smtp_from: $(q "$email_from")"
		echo "  smtp_hello: avxxx-alertmanager"
		if [ -n "${SMTP_USER:-}" ]; then
			echo "  smtp_auth_username: $(q "$SMTP_USER")"
			[ -n "${smtp_pw_file:-}" ] && echo "  smtp_auth_password_file: $(q "$smtp_pw_file")"
		fi
		# Port 465 is implicit TLS (handled by Alertmanager); anything else
		# must offer STARTTLS.
		echo "  smtp_require_tls: true"
	fi
}

tmp="$cfg.tmp"
{
	echo "# Rendered by deploy/monitoring/alertmanager/render-config.sh at $(date -u +%Y-%m-%dT%H:%M:%SZ). Do not edit."
	global_block
	cat <<'EOF'

route:
  # Unknown severities fall back to the warning receiver.
  receiver: ticket
  group_by: [alertname, service]
  group_wait: 1m
  group_interval: 5m
  repeat_interval: 12h
  routes:
    - matchers: ['severity="info"']
      receiver: "null"
    - matchers: ['severity="page"']
      receiver: pager
      group_wait: 30s
      group_interval: 5m
      repeat_interval: 2h
    - matchers: ['severity="warning"']
      receiver: ticket
      group_wait: 2m
      group_interval: 10m
      repeat_interval: 12h

# A target alert is muted while a matching source alert fires.
inhibit_rules:
  # The API (or its database) is down: the 5xx rate and latency follow.
  - source_matchers: ['alertname="ApiHealthProbeFailing"']
    target_matchers: ['alertname=~"ApiHigh5xxRate|ReleaseDetailP99High|RecsP99High|PlayConfigP99High"']
  # A worker is down: its own job alerts are consequences.
  - source_matchers: ['alertname="WorkerDown"', 'job="avxxx-scraper"']
    target_matchers: ['kind="crawl"', 'alertname=~"Jobs.*|JobLeaseLost"']
  - source_matchers: ['alertname="WorkerDown"', 'job="avxxx-scraper"']
    target_matchers: ['alertname="CrawlCheckpointStale"']
  - source_matchers: ['alertname="WorkerDown"', 'job="avxxx-audit"']
    target_matchers: ['kind=~"audit|source_health|recs_trending|recs_similarity|release_curves|search_stats|media_link|completeness_snapshot"', 'alertname=~"Jobs.*|JobLeaseLost"']
  - source_matchers: ['alertname="WorkerDown"', 'job="avxxx-audit"']
    target_matchers: ['alertname="SourceHealthStale"']
  - source_matchers: ['alertname="WorkerDown"', 'job="avxxx-notifier"']
    target_matchers: ['alertname="NotifierFailures"']
  - source_matchers: ['alertname="WorkerDown"', 'job="avxxx-scheduler"']
    target_matchers: ['alertname="DbStatsFailing"']
  # The paging disk alert supersedes the warning one for the same filesystem.
  - source_matchers: ['alertname="HostDiskSpaceCritical"']
    target_matchers: ['alertname="HostDiskSpaceLow"']
    equal: [instance, mountpoint]
  # node_exporter itself is down: the backup metric is gone with it.
  - source_matchers: ['alertname="NodeExporterDown"']
    target_matchers: ['alertname="BackupMetricMissing"']

receivers:
  - name: "null"
EOF
	receiver pager "${webhook_file:-}" "$email_to" page
	receiver ticket "${warning_webhook_file:-}" "$warning_email_to" warning
} >"$tmp"
mv "$tmp" "$cfg"
chmod 0600 "$cfg"

configured=""
[ -n "$webhook_url" ] && configured="$configured webhook"
[ -n "$warning_webhook_url" ] && configured="$configured warning-webhook"
[ "$email_ok" = 1 ] && configured="$configured email"
[ -n "$slack_url" ] && configured="$configured slack"
[ -n "$tg_token" ] && configured="$configured telegram"
if [ -z "$configured" ]; then
	log warn "no alert receiver configured (ALERT_WEBHOOK_URL, ALERT_EMAIL_TO + SMTP_*, ALERT_SLACK_WEBHOOK_URL, ALERT_TELEGRAM_*); alerts are only visible in the Alertmanager UI and nobody is paged"
elif [ -z "$webhook_url" ] && { [ "$email_ok" = 0 ] || [ -z "$email_to" ]; } && [ -z "$slack_url" ] && [ -z "$tg_token" ]; then
	log warn "receivers configured:$configured, but none for severity=page; page alerts are not delivered"
else
	log info "receivers configured:$configured"
fi

if [ "$render_only" = 1 ]; then
	printf '%s\n' "$cfg"
	exit 0
fi

exec /bin/alertmanager \
	--config.file="$cfg" \
	--storage.path=/alertmanager \
	--web.listen-address=:9093 \
	--web.external-url="$external_url" \
	--cluster.listen-address= \
	"$@"
