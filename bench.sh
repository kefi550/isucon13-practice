#!/bin/bash

host=isu1
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

if [[ -f "$script_dir/.env" ]]; then
	# shellcheck source=/dev/null
	source "$script_dir/.env"
fi

notify_discord() {
	local message=$1
	local payload

	# Discord のメッセージ上限は 2000 文字なので超える場合は切り詰める
	if (( ${#message} > 2000 )); then
		message="${message:0:1986}...(省略)"
	fi

	payload=$(jq -cn --arg content "$message" '{content: $content}') || return
	curl -fsS \
		-H 'Content-Type: application/json' \
		-d "$payload" \
		"$WEBHOOK_URL" >/dev/null ||
		echo 'Discord webhook への通知に失敗しました' >&2
}

if [[ -n "${WEBHOOK_URL:-}" ]]; then
	notify_discord "ベンチマークスタート (host: ${host})"
fi

ssh ${host} 'cd /home/isucon && ./bench_linux_amd64 run --target https://pipe.u.isucon.dev --nameserver 127.0.0.1 --webapp 52.192.44.134 --webapp 52.198.221.103 --webapp 35.77.86.88 --enable-ssl'
benchmark_status=$?

result_file=$(mktemp)
trap 'rm -f "$result_file"' EXIT

ssh ${host} 'cat /tmp/result.json' | tee "$result_file"

if [[ -n "${WEBHOOK_URL:-}" ]]; then
	if jq -e . "$result_file" >/dev/null 2>&1; then
		notify_discord "ベンチマーク結果
\`\`\`json
$(jq -c . "$result_file")
\`\`\`"
	else
		notify_discord "ベンチマーク終了 (exit status: $benchmark_status)
結果の JSON を取得できませんでした"
	fi
fi

exit "$benchmark_status"
