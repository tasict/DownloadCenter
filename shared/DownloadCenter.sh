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

# cron and App Center may start this with umask 0: nothing written here may
# be writable by others
umask 022

CONF="/etc/config/qpkg.conf"
QPKG_NAME="DownloadCenter"
QPKG_ROOT=$(/sbin/getcfg "$QPKG_NAME" Install_Path -f "$CONF")
# Every path below hangs off the install path: without it, do nothing
case "$QPKG_ROOT" in
	/?*) [ -d "$QPKG_ROOT" ] || { echo "$QPKG_NAME: install path $QPKG_ROOT not found"; exit 1; } ;;
	*) echo "$QPKG_NAME: install path not found"; exit 1 ;;
esac
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
	# The Apache conf holds the proxy secret: root only, also when an older
	# version left it world-writable
	[ -f "$APACHE_CONF" ] && chmod 600 "$APACHE_CONF"
	chmod 755 "${QPKG_ROOT}/DownloadCenter.sh" "$DCD" 2>/dev/null
	[ -f "${QPKG_ROOT}/bin/dc-dl" ] && chmod 755 "${QPKG_ROOT}/bin/dc-dl"
	[ -x "${QPKG_ROOT}/bin/dc-bt" ] && chmod 755 "${QPKG_ROOT}/bin/dc-bt"
	if [ -d "${QPKG_ROOT}/web" ]; then
		chown -R admin:administrators "${QPKG_ROOT}/web"
		chmod -R go-w "${QPKG_ROOT}/web"
	fi
	# Whoever can write the list of certificate authorities can make dcd, the
	# sidecars and the plugin sandbox trust any server: root's only, like web/
	if [ -d "${QPKG_ROOT}/certs" ] && [ ! -L "${QPKG_ROOT}/certs" ]; then
		chown -R admin:administrators "${QPKG_ROOT}/certs"
		chmod -R go-w "${QPKG_ROOT}/certs"
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

# The secret the admin Apache sends with every request it forwards to dcd:
# dcd trusts the client address and scheme headers only with it, so another
# local process talking to 127.0.0.1 cannot claim to be a remote browser.
# dcd creates data/proxy_auth at its first start; until then this does.
proxy_auth() {
	key=$(cat "${DATA}/proxy_auth" 2>/dev/null)
	case "$key" in
		*[!0-9a-f]*|'') key='' ;;
	esac
	if [ ${#key} -ne 32 ]; then
		key=$(head -c 32 /dev/urandom | md5sum | cut -c1-32)
		(umask 077; echo "$key" > "${DATA}/proxy_auth.tmp" && mv -f "${DATA}/proxy_auth.tmp" "${DATA}/proxy_auth")
	fi
	echo "$key"
}

write_apache_conf() {
	port=$(http_port)
	key=$(proxy_auth)
	[ ${#key} -eq 32 ] || { log "cannot create the proxy secret" 2; return 1; }
	# Written root-only into a new file, then moved over the old one (whose
	# mode may be anything)
	tmp="${APACHE_CONF}.tmp.$$"
	rm -f "$tmp"
	(umask 077; cat > "$tmp") <<EOF
# DownloadCenter QPKG - UI, REST API and event stream, served by dcd
ProxyPass /DownloadCenter/api/v1/events/stream http://127.0.0.1:${port}/DownloadCenter/api/v1/events/stream retry=0 timeout=86400 flushpackets=on
ProxyPass /DownloadCenter http://127.0.0.1:${port}/DownloadCenter retry=0 timeout=600 keepalive=On
ProxyPassReverse /DownloadCenter http://127.0.0.1:${port}/DownloadCenter
<Location "/DownloadCenter">
    SetEnv no-gzip 1
    <IfModule headers_module>
        RequestHeader set X-DC-Auth "${key}"
        RequestHeader set X-DC-Scheme "expr=%{REQUEST_SCHEME}"
        RequestHeader unset X-DC-Remote
        RequestHeader set X-DC-Remote "expr=%{REMOTE_ADDR}"
    </IfModule>
    # Without these headers nothing reaches dcd (an error here would stop the
    # whole QTS web server instead)
    <IfModule !headers_module>
        Require all denied
    </IfModule>
</Location>
EOF
	chmod 600 "$tmp" && mv -f "$tmp" "$APACHE_CONF"
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

# The running dcd: the pid file, else data/run/ready (written by dcd itself
# once it serves), which then repairs the pid file. Without the second
# look, a pid file that lost track of dcd made the watchdog start another
# one every minute.
dcd_pid() {
	for f in "$PIDFILE" "${DATA}/run/ready"; do
		pid=$(cat "$f" 2>/dev/null)
		case "$pid" in ''|*[!0-9]*) continue ;; esac
		[ -d "/proc/$pid" ] || continue
		grep -q "bin/dcd" "/proc/$pid/cmdline" 2>/dev/null || continue
		[ "$f" = "$PIDFILE" ] || (umask 077; echo "$pid" > "$PIDFILE")
		echo "$pid"
		return 0
	done
	return 1
}

dcd_start() {
	dcd_pid >/dev/null && return 0
	[ -x "$DCD" ] || { log "dcd binary missing" 2; return 1; }
	# dcd keeps the previous log as dcd.log.1 itself, once it holds its lock
	# (rotating here lost the running dcd's log when a second start failed)
	# Its log and pid file are private (dcd sets its own umask for downloads)
	umask 077
	setsid "$DCD" serve -root "$QPKG_ROOT" -port "$(http_port)" >> "${LOGDIR}/dcd.log" 2>&1 < /dev/null &
	# dcd writes its own pid once it holds its lock; $! stands in until
	# then (it is setsid's when setsid had to fork)
	dcd_pid >/dev/null || echo $! > "$PIDFILE"
	umask 022
	# Wait up to 10 s for it to serve; when it exits instead, the last line
	# of its log (the reason) is left in START_ERR
	i=0
	while [ $i -lt 10 ]; do
		sleep 1
		pid=$(dcd_pid) || { START_ERR=$(tail -n 1 "${LOGDIR}/dcd.log" 2>/dev/null | cut -c1-300); return 1; }
		[ "$(cat "${DATA}/run/ready" 2>/dev/null)" = "$pid" ] && return 0
		i=$((i + 1))
	done
	return 0
}

# What the watchdog saw when it decided to restart dcd, for whoever
# investigates later (bounded: cut to its last half past 256 KiB)
watchdog_note() {
	f="${LOGDIR}/watchdog.log"
	(
		umask 077
		[ -f "$f" ] && [ "$(wc -c < "$f")" -gt 262144 ] && tail -c 131072 "$f" > "$f.tmp" && mv -f "$f.tmp" "$f"
		{
			echo "$(date '+%Y-%m-%d %H:%M:%S') dcd not running: pid file '$(cat "$PIDFILE" 2>/dev/null)', ready '$(cat "${DATA}/run/ready" 2>/dev/null)'"
			ps 2>/dev/null | grep '[b]in/dcd'
			tail -n 5 "${LOGDIR}/dcd.log" 2>/dev/null
		} >> "$f"
	)
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

# start, stop and the watchdog never run at the same time (QTS's boot, App
# Center and cron may start them together): a lock folder, taken over when
# a script that died left it more than 2 minutes ago. The argument is how
# many seconds to wait for it (0: give up at once).
svc_lock() {
	mkdir -p "${DATA}/run"
	l="${DATA}/run/svc.lock"
	i=0
	while ! mkdir "$l" 2>/dev/null; do
		if [ -n "$(find "$l" -maxdepth 0 -mmin +2 2>/dev/null)" ]; then
			rmdir "$l" 2>/dev/null
			continue
		fi
		[ $i -ge "$1" ] && return 1
		i=$((i + 1))
		sleep 1
	done
	trap "rmdir '$l' 2>/dev/null" EXIT
	return 0
}

# --- Main ---

case "$1" in
	start)
		ENABLED=$(/sbin/getcfg "$QPKG_NAME" Enable -u -d FALSE -f "$CONF")
		if [ "$ENABLED" != "TRUE" ]; then
			echo "$QPKG_NAME is disabled."
			exit 1
		fi
		svc_lock 90 || { echo "$QPKG_NAME: another start or stop is still running."; exit 1; }
		ensure_files
		install_icons
		write_apache_conf
		/sbin/setcfg "$QPKG_NAME" Web_Port -1 -f "$CONF"
		reload_admin_web
		dcd_start || log "dcd did not start: $START_ERR" 2
		cron_add
		log "started"
		echo "$QPKG_NAME started."
		;;

	stop)
		# Stopping goes ahead even when the lock cannot be had (QTS gives a
		# stop 60 s in all)
		svc_lock 15
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
		# All well: nothing is written (the disks can sleep)
		dcd_pid >/dev/null && [ -f "$APACHE_CONF" ] && exit 0
		# A start or stop in progress: nothing to do this minute; else look
		# again under the lock
		svc_lock 0 || exit 0
		if ! dcd_pid >/dev/null; then
			# The system log gets one line an hour at most, with the number
			# of restarts since the last line (data/run/watchdog: "<time> <count>")
			set -- $(cat "${DATA}/run/watchdog" 2>/dev/null)
			last=${1:-0} count=${2:-0}
			case "$last$count" in *[!0-9]*) last=0 count=0 ;; esac
			count=$((count + 1)) now=$(date +%s)
			msg="dcd was not running; restarting"
			[ "$count" -gt 1 ] && msg="$msg ($count restarts since the last message)"
			report=0
			if [ $((now - last)) -ge 3600 ]; then
				report=1 last=$now count=0
			fi
			echo "$last $count" > "${DATA}/run/watchdog"
			watchdog_note
			dcd_start || msg="$msg; it did not start: $START_ERR"
			[ $report -eq 1 ] && log "$msg" 1
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
