---
name: validate-pr
description: "Attack a change you have just made, in a browser, before you report the work done. Use right after opening a pull request for anything a person can see or click — a screen, a form, an endpoint, a flag — to find the defects the tests did not. Covers empty and absent states, oversized and invalid input, double submits, interrupted flows, a phone-sized viewport, keyboard-only use, and console errors on a screen that renders correctly."
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

Save each shot as you take it — `take_screenshot` writes wherever `filePath` says, under the
OS temporary directory — and name it for its state and its viewport, so the table reads
without opening anything: `chat-empty-desktop.png`, `chat-menu-mobile.png`.

Then append one block to the pull request body, between markers, so re-running replaces its
own block instead of overwriting the description somebody wrote:

```markdown
<!-- validation:start -->
## Self-validation — PASS

> Attacked in the browser beside this turn, on desktop and phone.

| # | What it must do | 1440×900 | 390×844 |
|:-:|-----------------|:--------:|:-------:|
| 1 | The model menu opens on screen and scrolls inside itself | ✅ | ✅ |
| 2 | Picking a model keeps the menu open so effort can be set | ✅ | ✅ |

| Desktop (1440×900) | Phone (390×844) |
|:---:|:---:|
| ![Model menu open, catalogue scrolling](./chat-menu-desktop.png) | ![Same menu, phone width](./chat-menu-mobile.png) |

**Fixed while validating** — the catalogue painted below the fold at 800×600; it now flips above the chip.

**Left** — the sidebar still takes 224px on a phone. Older than this change, and not made worse by it.
<!-- validation:end -->
```

Post it with the files attached in the same command. `--attach` uploads each file, and where
the body already references that file it **rewrites the reference** to point at the uploaded
asset. That rewrite is what puts the screenshots in the table.

**The reference and the attached path have to be the same string.** A body saying
`./shot.png` while you attach `/tmp/opencode/shot.png` is two different strings, so nothing
matches: gh uploads the files and appends them to the bottom of the body instead, leaving four
broken images in your table and four bare ones underneath it. Run `gh` from the directory
holding the screenshots, with `--repo` so it does not need the checkout, and use the same
`./name.png` on both sides:

```sh
cd /tmp/opencode
gh pr edit <n> --repo <owner>/<repo> --body-file ./validation.md \
  --attach './chat-menu-desktop.png#Model menu open at 1440x900' \
  --attach './chat-menu-mobile.png#Same menu at 390x844'
```

Then check it landed, because this fails silently and the result looks fine from here:

```sh
gh pr view <n> --repo <owner>/<repo> --json body --jq .body | grep -c '](\./'
```

**Zero, or it did not work.** Any surviving `](./` is a reference gh did not rewrite — a broken
image on the pull request. Fix the mismatch and post again rather than leaving it.

Build the body by reading the current description, stripping any previous block between the
markers, and appending the new one. Never replace the description wholesale — the part above
those markers is the author's.

**A FAIL posts the same block**, with the verdict changed, ❌ against the criteria that failed,
and the screenshot of each failure in the table. Do not post a running commentary while you are
still fixing: one block, at the end, describing where the change actually landed.

## What you say in the turn's own answer

The pull request has the evidence; the answer is the summary a person reads first:

- **What you attacked** — which criteria, which attacks, on which viewports. Anything not run,
  named as not run.
- **What broke**, each with the user who was worse off.
- **What you fixed**, and that you re-attacked it.
- **What is left**, with the reason.

A pass means you tried to break it and could not. It never means you did not try.
