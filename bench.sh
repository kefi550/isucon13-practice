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
		local omission='...(省略)'
		# 末尾がコードブロックの場合は、閉じフェンスが消えないように残す
		if [[ "$message" == *'```' ]]; then
			omission=$'...(省略)\n```'
		fi
		message="${message:0:$((2000 - ${#omission}))}${omission}"
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

# ベンチは isu1 で動かし、向き先は isu1。名前解決は isu3 の PowerDNS (/etc/hosts の isu3) を使う
ssh ${host} 'cd /home/isucon && ./bench_linux_amd64 run --target https://pipe.u.isucon.dev --nameserver isu3 --webapp 52.198.221.103 --webapp 35.77.86.88 --webapp 13.193.224.85 --enable-ssl'
benchmark_status=$?

result_file=$(mktemp)
trap 'rm -f "$result_file"' EXIT

ssh ${host} 'cat /tmp/result.json' | tee "$result_file"

if [[ -n "${WEBHOOK_URL:-}" ]]; then
	if jq -e . "$result_file" >/dev/null 2>&1; then
    notify_discord "ベンチマーク結果 (host: ${host})
\`\`\`json
$(jq -c . "$result_file")
\`\`\`"
	else
		notify_discord "ベンチマーク終了 (exit status: $benchmark_status)
結果の JSON を取得できませんでした"
	fi
fi

exit "$benchmark_status"
