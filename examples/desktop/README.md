# A sample desktop playbook

A desktop playbook gives a turn a Linux desktop that a person watches live beside the chat and
can take over: Chromium, a terminal, and whatever applications the image carries
([docs/agent.md#desktop-true](../../docs/agent.md#desktop-true)). Podium ships the desktop
itself, `podium-agent-runtime-desktop`. This directory is one worked example of putting it to
work: **a spreadsheet desk**, where the agent researches in the browser, builds the table in
LibreOffice Calc on the same screen, and attaches the finished `.xlsx` to its reply.

| File | What it is |
|---|---|
| [`Dockerfile`](Dockerfile) | The desktop image plus LibreOffice Calc, a text editor, an image viewer and `wmctrl`: the pattern for extending the desktop with your own applications |
| [`podium-sample.xcd`](podium-sample.xcd) | LibreOffice defaults with no first-run dialogs, so Calc opens straight to the sheet |
| [`playbooks/spreadsheet-desk.yaml`](playbooks/spreadsheet-desk.yaml) | The playbook: `desktop: true`, the image, resources and a timeout sized for a working session |
| [`prompts/spreadsheet-desk.md`](prompts/spreadsheet-desk.md) | How the agent works the desktop: the exact commands that open, refresh and export a sheet without a dialog in the way |

## Try it

On the machine the node runs on:

```sh
make agent-runtime agent-runtime-desktop                # Podium's runtime, then its desktop
docker build -t podium-desktop-sample:dev examples/desktop
```

Then copy the playbook and its prompt into your profile (or create the same playbook on the
Playbooks screen, pasting the prompt):

```sh
cp examples/desktop/playbooks/spreadsheet-desk.yaml "$PODIUM_AGENT_PROFILE_DIR/playbooks/"
cp examples/desktop/prompts/spreadsheet-desk.md     "$PODIUM_AGENT_PROFILE_DIR/prompts/"
```

podium-server needs `PODIUM_DESKTOP_URL` set, or the chat says desktops are off
([networking.md#desktops](../../docs/networking.md#desktops)). Restart the conductor so it reads
the new playbook, then ask in a chat:

> Compare the price of a 1 TB NVMe SSD at three Canadian retailers, as a spreadsheet.

The assistant delegates to `spreadsheet-desk`, the chat opens the Desktop tab, and you watch
Chromium search, Calc fill in, and the reply arrive with `ssd-prices.xlsx` and `ssd-prices.csv`
attached. Click into the sheet and change a cell: the next message finds your edit rather than
overwriting it.

## Making your own

Copy the shape, not the applications:

- **The image is `FROM podium-agent-runtime-desktop`**, installs as root, smoke-tests what it
  installed, and ends with `USER agent` and no `ENTRYPOINT`. Run any GUI program's smoke test
  with a throwaway `HOME`: the base points every user's `HOME` at `/home/agent`, and a root-owned
  `~/.config` left there stops the desktop from starting. The Dockerfile's last check fails the
  build on that.
- **Take every dialog out of the agent's way** before it meets one, in the application's own
  system-wide defaults, as `podium-sample.xcd` does for LibreOffice. A first-run dialog costs a
  person one click and an agent several screenshots.
- **Give the prompt the exact commands**, tried on the desktop, for opening, refreshing and
  saving the application's files. The runtime already tells the model it is on a desktop, that
  a person is watching, and that `DISPLAY=:1` and `xdotool` reach the screen. What it does not
  know is your application's quirks: here, that Calc needs `--infilter` to open a CSV with no
  import dialog, that it will not reload a file it already has open, and that `wmctrl -c`, not
  `xdotool`, closes it the way its close button does.
- **The image is yours to distribute.** It is built on the desktop image, which carries voxl,
  so it is GPL-2+ as built ([THIRD_PARTY.md](../../agent/runtime/desktop/THIRD_PARTY.md)):
  build it where your nodes run, or push it to a registry they pull from, and keep the source
  beside it if you publish it.

Mind what a desktop costs. The turn holds its slot, 4 CPUs and 8 GB while the person reads, and
the browser shares a box with the workspace: point a desktop playbook at work a person will
watch, not at arbitrary sites.
