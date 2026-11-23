# A sample desktop image: Podium's desktop plus the applications this sample's work needs.
#
# podium-agent-runtime-desktop is the desktop and nothing more: a display, XFCE, Chromium, the
# stream and the terminal. A desktop playbook that does real work extends it the same way any
# playbook extends the base runtime (docs/agent.md#extending-the-runtime-image): FROM the image,
# install as root, smoke-test what was installed, hand back to `agent`.
#
# This one adds an office desk — a spreadsheet, a text editor and an image viewer — so the person
# watching can see the agent build a sheet, and open it themselves, in the same session.
#
#   make agent-runtime agent-runtime-desktop
#   docker build -t podium-desktop-sample:dev examples/desktop
#
# Built locally it is visible only to a node on this machine, and like the image it is built FROM
# it is never published by this repository: it carries voxl, which is GPL-2+ as built (see
# agent/runtime/desktop/THIRD_PARTY.md). A worker elsewhere, or a cloud sandbox, needs it pushed
# somewhere it can pull from — on Modal, examples/modal/publish_image.py.
ARG DESKTOP_IMAGE=podium-agent-runtime-desktop:dev
FROM ${DESKTOP_IMAGE}

USER root

# - libreoffice-calc, libreoffice-gtk3: the spreadsheet, drawn with the same GTK theme as the
#   rest of the desktop (without -gtk3 it falls back to its own X11 widgets).
# - mousepad, ristretto: XFCE's own text editor and image viewer, so a CSV or a chart the agent
#   writes opens in something native when the person double-clicks it.
# - wmctrl: closes a window the way its own close button does. xdotool can only destroy one,
#   which LibreOffice treats as a crash and answers with a recovery dialog on the next start;
#   and xdotool cannot find Calc's window by its title at all.
# - fonts-crosextra-carlito, fonts-crosextra-caladea: metric-compatible Calibri and Cambria, so
#   a sheet someone sends in keeps its column widths.
#
# About 110 MB to download and 350 MB on disk with --no-install-recommends. Check what a package
# drags in before adding it: `apt-get install -s --no-install-recommends <pkg>` inside this image.
#
# The smoke tests run with a throwaway HOME. The base sets HOME=/home/agent for every user, so a
# GUI program started here as root writes its first-run ~/.config and ~/.cache there, owned by
# root — and then, at run time, XFCE cannot save its settings and Chromium cannot create its
# profile, and the desktop never comes up. The last line makes that a build failure instead.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        libreoffice-calc libreoffice-gtk3 \
        mousepad ristretto wmctrl \
        fonts-crosextra-carlito fonts-crosextra-caladea; \
    rm -rf /var/lib/apt/lists/*; \
    smoke="$(mktemp -d)"; \
    HOME="$smoke" soffice --version; \
    HOME="$smoke" mousepad --version >/dev/null; \
    command -v ristretto; \
    wmctrl --version; \
    rm -rf "$smoke"; \
    test -z "$(find /home/agent ! -user agent -print -quit)"

# Calc's first start shows a tip-of-the-day dialog and a "what's new" bar. A person can dismiss
# them; an agent driving the screen should not have to. These are LibreOffice's own settings, in
# its system-wide defaults, so every user the turn runs as starts without them.
COPY --chmod=0644 podium-sample.xcd /usr/lib/libreoffice/share/registry/podium-sample.xcd
RUN set -eux; \
    version="$(HOME=/tmp soffice --version | sed -n "s/^LibreOffice \([0-9]*\.[0-9]*\).*/\1/p")"; \
    test -n "$version"; \
    sed -i "s|<value>@LO_VERSION@</value>|<value>$version</value>|" /usr/lib/libreoffice/share/registry/podium-sample.xcd; \
    grep -q "<value>$version</value>" /usr/lib/libreoffice/share/registry/podium-sample.xcd; \
    rm -rf /tmp/.config /tmp/.cache

USER agent
# No ENTRYPOINT, no CMD: the desktop image's are the runtime's, and a desktop playbook's command
# is podium-desktop in front of it. Overriding either is working around Podium, not extending it.
