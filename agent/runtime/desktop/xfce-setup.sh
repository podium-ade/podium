#!/bin/sh
# Settles XFCE's defaults into the desktop a person sees in desktop mode: a dark theme, a top
# bar with the applications menu, the window list, the tray and a clock, the bottom launcher
# bar always shown, a wallpaper and no desktop icons. It runs once, after XFCE is up, and
# leaves XFCE to manage itself from there — including following the screen when voxl resizes
# it, which XFCE does on its own.
#
# Every value is XFCE's own setting (xfconf); nothing here draws a panel by hand.
set -eu

: "${DISPLAY:?DISPLAY must be set}"

# XFCE starts its settings daemon, panel and desktop asynchronously. Wait for all three.
i=0
until pgrep -u "$(id -u)" -x xfsettingsd >/dev/null &&
	pgrep -u "$(id -u)" -x xfce4-panel >/dev/null &&
	pgrep -u "$(id -u)" -x xfdesktop >/dev/null; do
	i=$((i + 1))
	if [ "$i" -gt 150 ]; then
		echo "xfce-setup: XFCE did not start within 30s" >&2
		exit 1
	fi
	sleep 0.2
done

# xfconf lives on the session's bus, which only the session's processes know the address of.
bus="$(tr '\0' '\n' <"/proc/$(pgrep -u "$(id -u)" -x xfsettingsd | head -1)/environ" |
	sed -n 's/^DBUS_SESSION_BUS_ADDRESS=//p')"
if [ -z "$bus" ]; then
	echo "xfce-setup: no session bus address on xfsettingsd" >&2
	exit 1
fi
export DBUS_SESSION_BUS_ADDRESS="$bus"

# set writes a property, creating it when XFCE has not yet.
set_prop() {
	xfconf-query "$@" 2>/dev/null || xfconf-query --create "$@"
}

set_prop -c xsettings -p /Net/ThemeName -t string -s Greybird-dark
set_prop -c xsettings -p /Net/IconThemeName -t string -s elementary-Xfce-dark
set_prop -c xfwm4 -p /general/theme -t string -s Greybird-dark
set_prop -c xfce4-panel -p /panels/dark-mode -t bool -s true

# Top bar, XFCE's default panel-1 minus its workspace pager and session menu: applications
# menu (1), window list (2), separator (3), tray (6), separator (7), clock (8).
xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids -rR
xfconf-query -c xfce4-panel -p /panels/panel-1/plugin-ids --create -a \
	-t int -s 1 -t int -s 2 -t int -s 3 -t int -s 6 -t int -s 7 -t int -s 8
set_prop -c xfce4-panel -p /plugins/plugin-8/digital-layout -t int -s 3
set_prop -c xfce4-panel -p /plugins/plugin-8/digital-time-format -t string -s "%a %b %-d  %H:%M"

# Bottom bar, XFCE's default launchers: always shown (it auto-hides by default), dock-sized.
set_prop -c xfce4-panel -p /panels/panel-2/autohide-behavior -t uint -s 0
set_prop -c xfce4-panel -p /panels/panel-2/size -t uint -s 48
set_prop -c xfce4-panel -p /panels/panel-2/icon-size -t uint -s 32

# Wallpaper, for each name the virtual display's one output may go by, and no icons.
for monitor in monitorscreen monitor0 monitorDUMMY0; do
	backdrop="/backdrop/screen0/$monitor/workspace0"
	set_prop -c xfce4-desktop -p "$backdrop/image-style" -t int -s 5
	set_prop -c xfce4-desktop -p "$backdrop/last-image" -t string \
		-s /usr/share/backgrounds/xfce/xfce-cp-dark.svg
done
set_prop -c xfce4-desktop -p /desktop-icons/style -t int -s 0

# The panel reads its plugin list only when it starts, and xfdesktop, started before the
# display's output had settled, sizes itself 10x10 and paints nothing. Restart both, detached
# so they outlive this script.
xfce4-panel --quit || true
xfdesktop --quit || true
sleep 1
setsid xfce4-panel >/dev/null 2>&1 </dev/null &
setsid xfdesktop >/dev/null 2>&1 </dev/null &

# Wait for the restarted panel, so the desktop is whole when the wrapper says it is ready.
i=0
until pgrep -u "$(id -u)" -x xfce4-panel >/dev/null && pgrep -u "$(id -u)" -x xfdesktop >/dev/null; do
	i=$((i + 1))
	if [ "$i" -gt 50 ]; then
		echo "xfce-setup: the panel or desktop did not come back after the restart" >&2
		exit 1
	fi
	sleep 0.2
done
