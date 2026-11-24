---
name: validate-pr
description: "Attack a change you have just made, in a browser, before you report the work done. Use right after opening a pull request for anything a person can see or click — a screen, a form, an endpoint, a flag — to find the defects the tests did not. Covers empty and absent states, oversized and invalid input, double submits, interrupted flows, a phone-sized viewport, keyboard-only use, and console errors on a screen that renders correctly. Ends with one human-paced walkthrough video of the feature, and the verdict posted on the pull request."
---

# Attack your own pull request

You wrote the change, and that is the problem this procedure exists for. You know what it is
meant to do, so that is what you will type, in that order, once — and it will work. Nothing
you can conclude from that is worth reporting.

**This is a self-check, not a review.** The turn running this is the turn that wrote the code,
so a defect found here is a defect to fix now, in this branch, and then verify again. There is
no list to hand to somebody else. The human's review happens afterwards, on the pull request,
and what should be waiting for them is a change that already survived an attack — not a set of
things you noticed and left.

Stop when you run out of attacks, not when you stop finding things.

## The environment, so you do not spend turns rediscovering it

- **The browser is a sidecar container**, driven through the harness's `browser*` tools. None
  of it is in your container: it has its own filesystem, its own profile and its own network
  namespace, and it is thrown away with the task.
- **It starts with no tab.** Call `new_page` first, or every other tool has no page to act on.
  **Page ids begin at 1.**
- **`take_snapshot` is how you find what to click.** It is the accessibility tree — names,
  roles, and the uids the click and fill tools take — and it costs a fraction of a screenshot.
  Snapshot to work; screenshot when a human needs to see something.
- **A server you start must bind `0.0.0.0`.** Loopback inside your container is reachable by
  nothing else, and the browser is somewhere else entirely. Your own container answers to
  `task` on the task network, so the browser opens `http://task:<port>` — never `localhost`.
- **`list_console_messages` and `list_network_requests` after every screen.** A screen that
  renders correctly and throws in the console is a failing screen, and no screenshot shows it.
  A 500 behind a spinner is the same. This is the cheapest finding available and the one most
  often missed.

## Get it running first

Build and start what you changed, the way the repository says to, and confirm the page loads
before you attack it. A pass that never got the application up has found nothing — say so
plainly rather than reporting unattempted attacks as passes.

## Write down what the change must do, before you attack it

Read your own diff and turn it into a short list of criteria — the things a person would check
to decide the work was done. Three or four is usual; one is fine. They become the rows of the
table you post at the end, and each one gets walked on **both** viewports:

- **Desktop, 1440×900** — `resize_page`.
- **Phone, 390×844** — `resize_page`.

A criterion you did not walk is not a pass. Say it was not run, and why.

## The attacks, in the order that finds bugs

Work down the list. Each one is a question about a user who ends up worse off, not a box.

1. **Empty and absent.** No rows, no results, no permission, no such id. A list you only ever
   saw with data in it is a list whose empty state may be a blank rectangle, a spinner that
   never stops, or a crash on `rows[0]`. Delete the last row and look at what is left.
2. **Oversized input.** A 300-character name, a 50 kB paste, a value at and one past every
   limit the code declares. Watch for layout a long string tears open, a limit enforced only
   in the browser, and truncation that loses the data rather than displaying it short.
3. **Invalid input and double submits.** The wrong type, a negative number, a malformed date,
   an empty required field. Then press the button twice, fast: two rows, two emails, a double
   charge, or a button that stays disabled for ever after the first click.
4. **Interrupted flows.** This is where the real bugs are. Reload halfway through. Press back
   after submitting. Open the same thing in two tabs and change it in both. Navigate away with
   a request in flight. Anything holding state that is not in the URL or the database will show
   you where it kept it.
5. **390×844.** `resize_page` to a phone. Look for what is now off-screen, unreachable or
   under a fixed bar: a submit button below the fold with nothing to scroll, a table that
   pushes the page sideways, a dialog taller than the viewport with no way out of it.
6. **Keyboard only.** Tab through it. Can you reach every control, see where the focus is, and
   finish the flow without the mouse? Does Escape close what it opened, and does focus return
   to where it came from?

## The verdict is FAIL if anything is wrong

One rule, and it is not a judgement call: **any console error, uncaught exception, or 4xx/5xx
tied to the change is a FAIL**, whatever the screen looked like. So is a criterion that does
not hold on either viewport. There is no "passed with notes".

A FAIL is not the end of the turn — you are the one who fixes it. But it does mean you are not
finished, and it means you do not post a verdict yet.

## What counts as a finding

**A finding needs a user who is worse off.** Name them: what they were doing, what happened,
and what they got instead. If you cannot, it is an opinion and it stays out of the report —
padding real defects with remarks about spacing is exactly how the real ones get skimmed past.

Severity is that user's cost, not the size of the fix. Data lost or silently wrong, a flow that
cannot be completed, and an error a user cannot act on are the top of the list. A label two
pixels out is not on it at all.

## Fix it, re-attack it, and know when to stop

Fix what you found, in this branch, now. Then run the same attack again and watch it not
happen. A fix you did not re-attack is a claim, and this procedure exists because claims about
your own code are worth nothing.

Deciding not to fix something is allowed, and it has to be said out loud: what it is, who it
costs, and why it is not being fixed in this change.

**Three passes is the ceiling** — the first, and at most two more after fixes. If it still
fails on the third, stop there: post the verdict as a FAIL with what is outstanding and say the
cap was reached. A turn that loops on its own defects until it times out reports nothing at
all, which is worse than reporting a defect.

## Post the verdict on the pull request

This is the deliverable. The screenshots are how a human confirms the work was done and done
correctly, without checking out your branch to look — so **a turn that reports done and posts
no verdict has not finished**, however clean the attack was. "Nothing broke" is the common
outcome and the one where the pictures matter most: it is exactly the claim a reviewer cannot
check from a diff.

Save each shot as you take it into **one directory of its own** — `take_screenshot` writes
wherever `filePath` says, so give it `/tmp/validation/<name>.png` — and name it for its state
and its viewport, so the table reads without opening anything: `chat-empty-desktop.png`,
`chat-menu-mobile.png`.

### Record one walkthrough of the feature, last

The screenshots prove each criterion. The video is something else: **one walkthrough of the
feature as a person would use it**, start to finish, which the reviewer watches before reading
the diff. Not one clip per criterion, and not the attack. A reviewer should be able to watch it
without knowing what changed and come away knowing what the feature is and that it works.

Record it once, after the last pass is clean. Never record while you are still fixing.

**Do not use `screencast_start` for this.** A model driving the browser tools fires its clicks
back to back. With no cursor on screen, nothing can be followed, and the recording stops before
the result it was waiting for arrives. What came out was four-second clips of a page twitching.
Record with `walkthrough.cjs`, which sits beside this `SKILL.md`. You write what to do, and it
decides how fast:
- a caption says what is about to happen
- a cursor glides to the target, pauses, and clicks, with a ripple
- text is typed a key at a time
- what a step is looking for is framed and held on screen long enough to read
- a wait for a model's answer stays in the video at its real length

Write the flow to `/tmp/validation/walkthrough.cjs`:

```js
module.exports = {
  title: "A second person in a chat means @Podium is needed",   // the opening title card
  url: "http://task:5173/",                                     // where it starts
  async run(w) {
    const p = w.page;                                           // a Playwright page
    await w.say("A chat shared with the team, one person in it so far");
    await w.click(p.getByRole("link", { name: "Launch plan" }));
    await w.type(p.getByPlaceholder(/ask anything/i), "Can you draft the agenda?");
    await w.press("Enter");
    await w.see(p.getByText(/here's a draft/i), { timeout: 120_000 });  // waits as long as it takes
    await w.say("Now a second person speaks");
    // ...
  },
};
```

| Call | What the viewer sees |
|---|---|
| `w.say(text)` | a caption, held long enough to read. One before each thing that happens |
| `w.click(target)` | the cursor travels to it, pauses, clicks |
| `w.type(target, text)` | a click into the field, then typing at a person's speed |
| `w.press("Enter")` | a key or chord after a beat (`"Escape"`, `"Control+K"`) |
| `w.hover(target)` | the cursor rests on it: a tooltip, a hover menu |
| `w.scroll(dy)` | a smooth scroll of whatever is under the cursor |
| `w.see(target, { timeout, hold })` | waits for it, points at it, frames it, holds it. **End every step that changes something with one** |
| `w.goto(url)` / `w.hold(ms)` | a page loaded and settled / a pause |

A `target` is a Playwright locator: `getByRole`, `getByText`, `getByPlaceholder`, `getByLabel`.
**Leave `.first()` off.** The runner acts on the first match a person could see. A responsive
page keeps its other layout in the DOM, and `.first()` will happily pick a hidden copy of the
thing you meant.

Run it from the repository's checkout:

```sh
node <this skill's directory>/walkthrough.cjs /tmp/validation/walkthrough.cjs \
  --out /tmp/validation/walkthrough.mp4                    # 1440×900; --viewport 390x844 for a phone
```

- **The browser.** It opens a new tab in the browser your tools drive, with the same cookies and
  storage, and finds that browser on its own: the `chrome` sidecar, or the desktop's own Chromium.
- **What comes out.** The `.mp4`, and a `.png` of its last frame beside it. It prints the
  length and size. Aim for **one to two minutes**:
  - the feature's main path
  - the one or two moments where the change shows
  - nothing you already proved in the screenshots

  Over 10 MB, `publish.sh` refuses it; cut the flow down.
- **When a step fails, the video ends on it**, with the step and the error as the last caption,
  and the exit is non-zero. Read the error, fix the locator or the product, and run it again.
  A walkthrough that never reached the end is not evidence of the end.
- **A phone walkthrough** is worth recording only when the change is about the phone layout.
  Otherwise the phone screenshots in the table are enough.

It needs `ffmpeg` and `playwright-core`, which the `-dev` runtime image carries. Without them it
says so and exits. In that case post screenshots alone and say in the block that no walkthrough
was recorded. Do not try to install an encoder.

**GitHub does not play a video from outside GitHub inside a description**: it shows a link. So
the walkthrough goes at the top of the block as a still linked to the video,
`[![alt](./walkthrough.png)](./walkthrough.mp4)`. The runner writes that still for you: its
last frame, beside the video under the same name.

Then write `/tmp/validation/block.md`: one block, between markers, so re-running replaces its
own block instead of overwriting the description somebody wrote. Reference every file as
`./<name>`, exactly as it is named in that directory:

```markdown
<!-- validation:start -->
## Self-validation — PASS

[![Walkthrough: pick a model, set its effort, send — 1 min 20 s](./walkthrough.png)](./walkthrough.mp4)

**▶ Watch the walkthrough (1:20)** — the model menu, start to finish, as a person uses it.

> Attacked in the browser beside this turn, on desktop and phone.

| # | What it must do | 1440×900 | 390×844 |
|:-:|-----------------|:--------:|:-------:|
| 1 | The model menu opens on screen and scrolls inside itself | ✅ | ✅ |
| 2 | Picking a model keeps the menu open so effort can be set | ✅ | ✅ |

| Desktop (1440×900) | Phone (390×844) |
|:---:|:---:|
| ![1 — menu open, catalogue scrolling](./chat-menu-desktop.png) | ![1 — same menu, phone width](./chat-menu-mobile.png) |
| ![2 — model picked, effort still open](./chat-effort-desktop.png) | ![2 — same, phone width](./chat-effort-mobile.png) |

**Fixed while validating** — the catalogue painted below the fold at 800×600; it now flips above the chip.

**Left** — the sidebar still takes 224px on a phone. Older than this change, and not made worse by it.
<!-- validation:end -->
```

**The verdict goes in the description, and only through `publish.sh`.** Not `gh pr comment` — a
comment scrolls away under the review and cannot be replaced by the next run — and not
screenshots left in `.podium/artifacts`, which nobody on GitHub can see.

Post it with `publish.sh`, which sits beside this `SKILL.md`. Run it with `bash` from the
repository's checkout, so it knows which repository the pull request is in:

```sh
cd /workspace/<repo>
bash <this skill's directory>/publish.sh <pr-number> /tmp/validation
```

It uploads every file the block references and **rewrites each `./name` to the uploaded
file's URL** — that rewrite is what puts the screenshots in the table and the walkthrough behind
them. Where they go is the
playbook's choice, not yours: with the `QA_R2_*` variables set they go to the evidence bucket
and the block links them from its public domain; without them, `gh pr edit --attach` puts them
in GitHub's attachments. Either way it then splices the block into the description —
replacing a previous block, keeping everything the author wrote around it — and checks the
result: exactly one block and no surviving `./` reference.

It refuses anything but `.png` and `.mp4`, a file over 10 MB (a walkthrough that size is too long —
record it again, shorter), and more than 100 MB or 40 files
in one run. **A non-zero exit means the pull request does not show your evidence.** Read what it
printed, fix that, and run it again; never hand-edit the description around it.

**A FAIL posts the same block**, with the verdict changed, ❌ against the criteria that failed,
and the screenshot of each failure in the table. Do not post a running commentary while you are
still fixing: one block, at the end, describing where the change actually landed.

## Mark it ready for review — on a PASS only

The pull request was opened as a draft so nobody reviewed a change that had not been attacked
yet. Once `publish.sh` has posted a **PASS** and exited 0, that is no longer true, so take it out
of draft:

```sh
gh pr ready <pr-number>
gh pr view <pr-number> --json isDraft -q .isDraft     # must print false
```

**A FAIL stays a draft.** The verdict on it says what is outstanding, and a draft is how the
pull request says it is not ready for anyone's time. So does a run that never got its verdict
posted: no evidence on the pull request, no `gh pr ready`.

## What you say in the turn's own answer

The pull request has the evidence; the answer is the summary a person reads first:

- **What you attacked** — which criteria, which attacks, on which viewports. Anything not run,
  named as not run.
- **What broke**, each with the user who was worse off.
- **What you fixed**, and that you re-attacked it.
- **What is left**, with the reason.

A pass means you tried to break it and could not. It never means you did not try.
