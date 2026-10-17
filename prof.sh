#!/bin/bash
# プロファイリング用スクリプト
#   ./prof.sh reset            ベンチ前に nginx アクセスログとクエリ統計(performance_schema)をリセットする
#   ./prof.sh pprof [秒数]     ベンチ中に CPU プロファイルを取得する（デフォルト 60 秒）
#   ./prof.sh analyze          ベンチ後に alp とクエリ統計を集計し、prof/<日時>/ に保存する

set -eu

host=${HOST:-isu2}
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
out_dir="${script_dir}/prof"

# alp でまとめる URI パターン（パスパラメータを含むエンドポイント）
alp_matching='^/api/user/[^/]+/icon$,^/api/user/[^/]+/theme$,^/api/user/[^/]+/livestream$,^/api/user/[^/]+/statistics$,^/api/user/[^/]+$,^/api/livestream/[0-9]+/livecomment/[0-9]+/report$,^/api/livestream/[0-9]+/livecomment$,^/api/livestream/[0-9]+/reaction$,^/api/livestream/[0-9]+/report$,^/api/livestream/[0-9]+/ngwords$,^/api/livestream/[0-9]+/moderate$,^/api/livestream/[0-9]+/enter$,^/api/livestream/[0-9]+/exit$,^/api/livestream/[0-9]+/statistics$,^/api/livestream/[0-9]+$'

# performance_schema のクエリ種別ごとの統計を合計時間順に出す（pt-query-digest の代わり）
digest_sql='SELECT SCHEMA_NAME AS db, ROUND(SUM_TIMER_WAIT/1e12, 3) AS total_s, COUNT_STAR AS cnt, ROUND(AVG_TIMER_WAIT/1e9, 3) AS avg_ms, ROUND(SUM_ROWS_EXAMINED/COUNT_STAR) AS rows_exam, ROUND(SUM_ROWS_SENT/COUNT_STAR) AS rows_sent, LEFT(DIGEST_TEXT, 300) AS query FROM performance_schema.events_statements_summary_by_digest WHERE SCHEMA_NAME IS NOT NULL ORDER BY SUM_TIMER_WAIT DESC LIMIT 30'

# PowerDNS などサーバー側プリペアドステートメントを使うクエリの統計（接続ごとの行を SQL 文単位にまとめる）
prepared_sql='SELECT ROUND(SUM(SUM_TIMER_EXECUTE)/1e12, 3) AS total_s, SUM(COUNT_EXECUTE) AS cnt, ROUND(SUM(SUM_TIMER_EXECUTE)/SUM(COUNT_EXECUTE)/1e9, 3) AS avg_ms, ROUND(SUM(SUM_ROWS_EXAMINED)/SUM(COUNT_EXECUTE)) AS rows_exam, LEFT(SQL_TEXT, 300) AS query FROM performance_schema.prepared_statements_instances WHERE COUNT_EXECUTE > 0 GROUP BY SQL_TEXT ORDER BY SUM(SUM_TIMER_EXECUTE) DESC LIMIT 20'

case "${1:-}" in
reset)
	ssh "$host" 'sudo truncate -s 0 /var/log/nginx/access.log && sudo mysql -e "TRUNCATE TABLE performance_schema.events_statements_summary_by_digest; TRUNCATE TABLE performance_schema.prepared_statements_instances"'
	echo "[$host] アクセスログとクエリ統計をリセットしました"
	;;
pprof)
	seconds=${2:-60}
	mkdir -p "$out_dir"
	file="${out_dir}/cpu-$(date +%Y%m%d-%H%M%S).pb.gz"
	echo "[$host] CPU プロファイルを ${seconds} 秒間取得します..."
	ssh "$host" "curl -sS 'http://localhost:6060/debug/pprof/profile?seconds=${seconds}'" >"$file"
	echo "保存しました: $file"
	echo "確認: go tool pprof -http=:8888 $file"
	;;
analyze)
	dir="${out_dir}/$(date +%Y%m%d-%H%M%S)"
	mkdir -p "$dir"
	ssh "$host" "sudo alp ltsv --file /var/log/nginx/access.log --sort sum --reverse -m '${alp_matching}'" >"${dir}/alp.txt"
	ssh "$host" "sudo mysql --table -e '${digest_sql}'" >"${dir}/query.txt"
	ssh "$host" "sudo mysql --table -e '${prepared_sql}'" >"${dir}/query_prepared.txt"
	echo "保存しました: $dir"
	head -30 "${dir}/alp.txt"
	head -20 "${dir}/query.txt"
	head -15 "${dir}/query_prepared.txt"
	;;
*)
	echo "usage: $0 {reset|pprof [秒数]|analyze}" >&2
	exit 1
	;;
esac
