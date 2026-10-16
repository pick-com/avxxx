#!/bin/sh
# Render the Alertmanager config with no receivers and with every receiver
# configured (dummy, non-secret values), validate both with amtool and check
# the routing tree. Run by scripts/check-alerts.sh, normally inside the
# prom/alertmanager image (busybox sh + amtool).
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
amtool="${AMTOOL:-amtool}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

route_is() {
	want="$1"
	shift
	"$amtool" config routes test --config.file="$cfg" --verify.receivers="$want" "$@" >/dev/null ||
		{ echo "route $* -> expected $want, got $("$amtool" config routes test --config.file="$cfg" "$@")" >&2; exit 1; }
}

check_routes() {
	route_is pager severity=page alertname=ApiHealthProbeFailing service=api
	route_is ticket severity=warning alertname=HostDiskSpaceLow service=host
	route_is null severity=info alertname=CrawlSourceBlocked
	route_is ticket alertname=SomethingWithoutSeverity
}

echo "alertmanager: no receivers configured"
cfg="$(env -i PATH="$PATH" sh -c 'AM_CONFIG_DIR="$1" sh "$2" --render-only' _ "$work/none" "$here/render-config.sh")"
"$amtool" check-config "$cfg"
check_routes

echo "alertmanager: every receiver configured"
cfg="$(env -i PATH="$PATH" \
	ALERT_WEBHOOK_URL='https://pager.example.invalid/hook?token=dummy' \
	ALERT_WARNING_WEBHOOK_URL='https://tickets.example.invalid/hook' \
	ALERT_EMAIL_TO='oncall@example.invalid' \
	ALERT_WARNING_EMAIL_TO="ops@example.invalid, o'brien@example.invalid" \
	SMTP_HOST=smtp.example.invalid SMTP_PORT=587 SMTP_USER=alerts SMTP_PASSWORD='dummy' \
	MAIL_FROM='avxxx <alerts@example.invalid>' \
	ALERT_SLACK_WEBHOOK_URL='https://hooks.example.invalid/services/dummy' \
	ALERT_TELEGRAM_BOT_TOKEN='123:dummy' ALERT_TELEGRAM_CHAT_ID='-1001234567890' \
	sh -c 'AM_CONFIG_DIR="$1" sh "$2" --render-only' _ "$work/all" "$here/render-config.sh")"
"$amtool" check-config "$cfg"
check_routes
for f in webhook warning-webhook slack telegram smtp-password; do
	[ -s "$work/all/$f.secret" ] || { echo "missing secret file $f.secret" >&2; exit 1; }
done
if grep -q 'dummy' "$cfg"; then
	echo "a secret leaked into the rendered config" >&2
	exit 1
fi
echo "alertmanager: OK"
