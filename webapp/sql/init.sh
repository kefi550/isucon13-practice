#!/usr/bin/env bash

set -eux
cd $(dirname $0)

if test -f /home/isucon/env.sh; then
	. /home/isucon/env.sh
fi

ISUCON_DB_HOST=${ISUCON13_MYSQL_DIALCONFIG_ADDRESS:-127.0.0.1}
ISUCON_DB_PORT=${ISUCON13_MYSQL_DIALCONFIG_PORT:-3306}
ISUCON_DB_USER=${ISUCON13_MYSQL_DIALCONFIG_USER:-isucon}
ISUCON_DB_PASSWORD=${ISUCON13_MYSQL_DIALCONFIG_PASSWORD:-isucon}
ISUCON_DB_NAME=${ISUCON13_MYSQL_DIALCONFIG_DATABASE:-isupipe}

# MySQLを初期化（テーブルを DROP して schema から作り直すので、インデックスも schema の定義どおりになる）
mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initdb.d/10_schema.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_users.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_livestreams.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_tags.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_livestream_tags.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_reservation_slots.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_reactions.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_ngwords.sql

mysql -u"$ISUCON_DB_USER" \
		-p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" < initial_livecomments.sql

# PowerDNS を別ホストで動かしている場合(ISUCON13_PDNS_SSH_HOST)は、そのホストで init_zone.sh を実行する
if [ -n "${ISUCON13_PDNS_SSH_HOST:-}" ]; then
	ssh -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new \
		"$ISUCON13_PDNS_SSH_HOST" "bash /home/isucon/webapp/pdns/init_zone.sh"
else
	bash ../pdns/init_zone.sh
fi

# PowerDNS のレコード検索用インデックス（MySQL には CREATE INDEX IF NOT EXISTS がないため、なければ作成する）
ISUDNS_USER=${ISUCON13_POWERDNS_DB_USER:-isudns}
ISUDNS_PASSWORD=${ISUCON13_POWERDNS_DB_PASSWORD:-isudns}
if [ "$(mysql -u"$ISUDNS_USER" -p"$ISUDNS_PASSWORD" --host "$ISUCON_DB_HOST" --port "$ISUCON_DB_PORT" -N isudns \
	-e "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='isudns' AND table_name='records' AND index_name='nametype_index'")" = "0" ]; then
	mysql -u"$ISUDNS_USER" -p"$ISUDNS_PASSWORD" --host "$ISUCON_DB_HOST" --port "$ISUCON_DB_PORT" isudns \
		-e "CREATE INDEX nametype_index ON records(name, type)"
fi


