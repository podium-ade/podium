# Third-party software in the desktop image

`podium-agent-runtime-desktop` is the agent runtime (`podium-agent-runtime`) plus a desktop the
turn and a person share. This lists what the desktop adds, under which licence, and where its
source is. The runtime's own components are listed after it.

Podium's own code is Apache-2.0 and is not changed by anything here. voxl is a separate program:
the image starts it as a process, and Podium reaches it over HTTP and a WebSocket. No Podium
binary links voxl or x264.

"GPL-2+" means the GNU General Public License, version 2 or (at your option) any later version.
Each Debian package's full copyright and licence text is in `/usr/share/doc/<package>/copyright`
in this image, and its source is in the Debian archive for Debian 13 (trixie).

## Added by the desktop

| Component | Role | Licence | Source |
|---|---|---|---|
| voxl (`voxl-server` 0.3.3) | streams the display to a browser | MIT. **The built `/usr/local/bin/voxl` is distributed under GPL-2+**, because it links x264 | `/usr/share/src/voxl/voxl-server-0.3.3.crate` in this image (the exact source it was built from); https://github.com/tembo/voxl; build steps: the `voxl` stage of `agent/runtime/Dockerfile.desktop` |
| x264 (`libx264-164`) | H.264 encoder, linked by voxl | GPL-2+ (some files ISC, LGPL-2.1+, public domain) | Debian |
| libyuv (`libyuv0`) | colour conversion, linked by voxl | BSD-3-Clause | Debian |
| Opus (`libopus0`) | audio codec, linked by voxl | BSD-2-Clause, BSD-3-Clause | Debian |
| PulseAudio client (`libpulse0`) | audio capture, linked by voxl | LGPL-2.1+ | Debian |
| ttyd 1.7.7 | serves the terminal to a browser | MIT. The release binary is statically linked with libwebsockets 4.3.3 (MIT), libuv 1.44.2 (MIT), json-c 0.17 (MIT), zlib 1.3.1 (Zlib) and mbed TLS 2.28.5 (Apache-2.0 or GPL-2+, used under Apache-2.0); its web client bundles xterm.js (MIT) | https://github.com/tsl0922/ttyd (tag 1.7.7; `scripts/cross-build.sh` is how the release is built) |
| tmux (`tmux`) | the terminal's shared session | ISC | Debian |
| Xvfb (`xvfb`) | the virtual X display | MIT/X11 | Debian |
| XFCE (`xfce4`, `xfce4-terminal`: session, xfwm4, panel, xfdesktop, Thunar) | the desktop | GPL-2+; panel libraries LGPL-2.1+ | Debian |
| Greybird (`greybird-gtk-theme`) | theme | GPL-2+ or CC-BY-SA-3.0+ | Debian |
| elementary Xfce icons (`elementary-xfce-icon-theme`) | icons | GPL-3+ (parts GPL-2+, LGPL-2+, CC-BY-SA-2.0) | Debian |
| librsvg (`librsvg2-common`) | SVG loader for the wallpaper | LGPL-2+ | Debian |
| Chromium (`chromium`) | the browser the agent drives and the person sees | BSD-3-Clause, with Apache-2.0 and MPL/GPL/LGPL tri-licensed parts | Debian |
| D-Bus X11 helpers (`dbus-x11`) | the desktop's session bus | GPL-2+ or AFL-2.1; libraries LGPL-2.1+ | Debian |
| xdotool | keyboard and mouse automation | BSD-3-Clause | Debian |

## Already in the agent runtime

| Component | Licence |
|---|---|
| Podium agent runtime | Apache-2.0 |
| Node.js | MIT |
| opencode (`opencode-ai`) | MIT |
| GitHub CLI (`gh`) | MIT |
| git | GPL-2.0 |
| bash | GPL-3+ |
| coreutils and the rest of the Debian base | GPL-3+, GPL-2+, LGPL and permissive licences, per package |
| ripgrep | MIT or Unlicense |
| jq | MIT |
