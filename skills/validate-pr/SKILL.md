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

## What counts as a finding

**A finding needs a user who is worse off.** Name them: what they were doing, what happened,
and what they got instead. If you cannot, it is an opinion and it stays out of the report —
padding real defects with remarks about spacing is exactly how the real ones get skimmed past.

Severity is that user's cost, not the size of the fix. Data lost or silently wrong, a flow that
cannot be completed, and an error a user cannot act on are the top of the list. A label two
pixels out is not on it at all.

## Fix it, then verify the fix

Fix what you found, in this branch, now. Then run the same attack again and watch it not
happen. A fix you did not re-attack is a claim, and this procedure exists because claims about
your own code are worth nothing.

Deciding not to fix something is allowed, and it has to be said out loud: what it is, who it
costs, and why it is not being fixed in this change.

## Attach the screenshots. Always. This is the point.

The screenshots are not a defect report — they are how a human confirms the work was done, and
done correctly, without rebuilding your branch to look for themselves. **A turn that reports
done and attaches nothing has not finished**, however clean the attack was. "Nothing broke" is
the most common outcome and the one where the pictures matter most, because a claim that
everything is fine is exactly the claim a reviewer cannot check from a diff.

Save each shot to a file as you take it — `take_screenshot` writes wherever `filePath` says,
under the OS temporary directory — then attach the files to the pull request you just opened:

```sh
gh pr edit <n> --attach '/tmp/opencode/chat-desktop.png#Chat composer, model menu open at 800x600' \
                --attach '/tmp/opencode/chat-mobile.png#Same menu at 390x844'
```

What to attach, in order:

- **Every screen the change touches, in its finished state.** This is the minimum, and it is
  not conditional on finding anything.
- **Both sides of every defect** — the break, and the same view after your fix.
- **The narrow viewport** for anything with a layout, because that is where it goes wrong.

The alt text after `#` is what a reviewer reads first: say what the picture shows, not
"screenshot 1". The only thing not worth attaching is a screen your change never touched.

## What you report when you are done

Four things, in the answer for this turn:

- **What you attacked** — which screens, and which of the attacks above. An attack you did not
  run is not a pass; name it as not run.
- **What broke** — each finding with its user, and the screenshot where there is one.
- **What you fixed**, and how you verified each fix.
- **What is left**, with the reason it is left.

A pass means you tried to break it and could not. It never means you did not try.

And check the pull request before you say you are done: the attachments are either on it or
they are not, and a report describing screenshots nobody can see is worse than no report.
