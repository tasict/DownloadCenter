#!/bin/sh
#
# DownloadCenter.sh - QPKG service script.
#
# start:    publish the UI on the QTS admin Apache, start the control daemon
#           (dcd, which starts and supervises the download engines) and
#           install the watchdog.
# stop:     stop dcd (it saves the engine sessions first), remove the UI and
#           the watchdog.
# watchdog: run from cron every minute; restarts dcd when it is gone.
#

CONF="/etc/config/qpkg.conf"
QPKG_NAME="DownloadCenter"
QPKG_ROOT=$(/sbin/getcfg "$QPKG_NAME" Install_Path -f "$CONF")
DATA="${QPKG_ROOT}/data"
DCD="${QPKG_ROOT}/bin/dcd"
PIDFILE="${DATA}/run/dcd.pid"
LOGDIR="${DATA}/logs"
APACHE_CONF="/etc/default_config/apache/extra/apache-downloadcenter.conf"
CRONTAB="/etc/config/crontab"
RSS_IMG_DIR="/home/httpd/RSS/images"

# --- Helpers (defined before use) ---

log() {
	/sbin/log_tool -t"${2:-0}" -uSystem -p127.0.0.1 -mlocalhost -a "[$QPKG_NAME] $1" >/dev/null 2>&1
}

ensure_files() {
	mkdir -p "$DATA" "${DATA}/run" "$LOGDIR"
	chmod 700 "$DATA"
	chmod 755 "${QPKG_ROOT}/DownloadCenter.sh" "$DCD" 2>/dev/null
	[ -f "${QPKG_ROOT}/bin/dc-dl" ] && chmod 755 "${QPKG_ROOT}/bin/dc-dl"
	[ -x "${QPKG_ROOT}/bin/dc-bt" ] && chmod 755 "${QPKG_ROOT}/bin/dc-bt"
	if [ -d "${QPKG_ROOT}/web" ]; then
		chown -R admin:administrators "${QPKG_ROOT}/web"
		chmod -R go-w "${QPKG_ROOT}/web"
	fi
}

install_icons() {
	[ -d "$RSS_IMG_DIR" ] || return 0
	for suffix in "" "_80" "_gray"; do
		src="${QPKG_ROOT}/.qpkg_icon${suffix}.gif"
		if [ -f "$src" ]; then
			cp -f "$src" "${RSS_IMG_DIR}/${QPKG_NAME}${suffix}.gif"
			cp -f "$src" "${RSS_IMG_DIR}/${QPKG_NAME}${suffix}.png"
		fi
	done
}

# The port dcd listens on (127.0.0.1 only) is chosen once and kept in data/.
http_port() {
	port=$(cat "${DATA}/http_port" 2>/dev/null)
	case "$port" in
		''|*[!0-9]*) port=18780; echo "$port" > "${DATA}/http_port" ;;
	esac
	echo "$port"
}

write_apache_conf() {
	port=$(http_port)
	cat > "$APACHE_CONF" <<EOF
# DownloadCenter QPKG - UI, REST API and event stream, served by dcd
ProxyPass /DownloadCenter/api/v1/events/stream http://127.0.0.1:${port}/DownloadCenter/api/v1/events/stream retry=0 timeout=86400 flushpackets=on
ProxyPass /DownloadCenter http://127.0.0.1:${port}/DownloadCenter retry=0 timeout=600 keepalive=On
ProxyPassReverse /DownloadCenter http://127.0.0.1:${port}/DownloadCenter
<Location "/DownloadCenter/">
    SetEnv no-gzip 1
    <IfModule mod_headers.c>
        RequestHeader set X-DC-Scheme "expr=%{REQUEST_SCHEME}"
        RequestHeader unset X-DC-Remote
        RequestHeader set X-DC-Remote "expr=%{REMOTE_ADDR}"
    </IfModule>
</Location>
EOF
}

# The UI lives in the QTS admin servers (8080/8081): reload only those.
reload_admin_web() {
	[ -f /etc/apache-sys-proxy.conf ] && /usr/local/apache/bin/apache_proxy -k graceful -f /etc/apache-sys-proxy.conf >/dev/null 2>&1
	[ -f /etc/apache-sys-proxy-ssl.conf ] && /usr/local/apache/bin/apache_proxys -k graceful -f /etc/apache-sys-proxy-ssl.conf >/dev/null 2>&1
}

cron_add() {
	grep -qF "${QPKG_ROOT}/DownloadCenter.sh watchdog" "$CRONTAB" 2>/dev/null && return 0
	echo "* * * * * /bin/sh ${QPKG_ROOT}/DownloadCenter.sh watchdog >/dev/null 2>&1" >> "$CRONTAB"
	crontab "$CRONTAB"
	/etc/init.d/crond.sh restart >/dev/null 2>&1
}

cron_remove() {
	grep -qF "DownloadCenter.sh watchdog" "$CRONTAB" 2>/dev/null || return 0
	sed -i "\#DownloadCenter.sh watchdog#d" "$CRONTAB"
	crontab "$CRONTAB"
	/etc/init.d/crond.sh restart >/dev/null 2>&1
}

dcd_pid() {
	pid=$(cat "$PIDFILE" 2>/dev/null)
	[ -n "$pid" ] || return 1
	[ -d "/proc/$pid" ] || return 1
	grep -q "bin/dcd" "/proc/$pid/cmdline" 2>/dev/null || return 1
	echo "$pid"
}

dcd_start() {
	dcd_pid >/dev/null && return 0
	[ -x "$DCD" ] || { log "dcd binary missing" 2; return 1; }
	# Keep the previous log for one restart
	[ -f "${LOGDIR}/dcd.log" ] && mv -f "${LOGDIR}/dcd.log" "${LOGDIR}/dcd.log.1"
	setsid "$DCD" serve -root "$QPKG_ROOT" -port "$(http_port)" >> "${LOGDIR}/dcd.log" 2>&1 < /dev/null &
	echo $! > "$PIDFILE"
	i=0
	while [ $i -lt 10 ]; do
		[ -f "${DATA}/run/ready" ] && return 0
		sleep 1
		i=$((i + 1))
	done
	return 0
}

dcd_stop() {
	pid=$(dcd_pid) || { rm -f "$PIDFILE"; return 0; }
	kill -TERM "$pid" 2>/dev/null
	i=0
	while [ $i -lt 45 ]; do
		[ -d "/proc/$pid" ] || break
		sleep 1
		i=$((i + 1))
	done
	[ -d "/proc/$pid" ] && kill -KILL "$pid" 2>/dev/null
	rm -f "$PIDFILE" "${DATA}/run/ready"
}

# --- Main ---

case "$1" in
	start)
		ENABLED=$(/sbin/getcfg "$QPKG_NAME" Enable -u -d FALSE -f "$CONF")
		if [ "$ENABLED" != "TRUE" ]; then
			echo "$QPKG_NAME is disabled."
			exit 1
		fi
		ensure_files
		install_icons
		write_apache_conf
		/sbin/setcfg "$QPKG_NAME" Web_Port -1 -f "$CONF"
		reload_admin_web
		dcd_start
		cron_add
		log "started"
		echo "$QPKG_NAME started."
		;;

	stop)
		cron_remove
		dcd_stop
		rm -f "$APACHE_CONF"
		reload_admin_web
		log "stopped"
		echo "$QPKG_NAME stopped."
		;;

	restart)
		$0 stop
		sleep 1
		$0 start
		;;

	watchdog)
		ENABLED=$(/sbin/getcfg "$QPKG_NAME" Enable -u -d FALSE -f "$CONF")
		[ "$ENABLED" = "TRUE" ] || exit 0
		if ! dcd_pid >/dev/null; then
			log "dcd was not running; restarting" 1
			dcd_start
		fi
		[ -f "$APACHE_CONF" ] || { write_apache_conf; reload_admin_web; }
		;;

	status)
		if pid=$(dcd_pid); then
			echo "dcd running (pid $pid)"
		else
			echo "dcd not running"
			exit 1
		fi
		;;

	*)
		echo "Usage: $0 {start|stop|restart|status|watchdog}"
		exit 1
		;;
esac

exit 0
