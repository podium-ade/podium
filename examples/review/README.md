# A review playbook that posts its own review

Podium no longer posts reviews. It used to take the review playbook's final message and turn it
into a GitHub review on its own: a **Review in progress** comment, a verdict read from the first
line, inline notes read from fenced blocks, and a new round after the App pushed a fix. None of
that could be seen or changed.

Now the playbook posts its own review, with the GitHub token its task is given, and how it does
so is written in its prompt. This directory is that old behaviour written down as a playbook, so
you can copy it as it is or change any of it.

| File | What it is |
|---|---|
| [`playbooks/review.yaml`](playbooks/review.yaml) | The playbook: `github: true`, the repository, read-only tools plus `bash` for `git` and `gh` |
| [`prompts/review.md`](prompts/review.md) | The review, step by step: the status comment, where the last review stopped, the verdict, one review per round with inline notes, and the status comment's final title |

## What the prompt does

1. **Posts a status comment** titled **Review in progress**, with the pull request's branches,
   head commit and size, or edits the one it posted in an earlier round.
2. **Finds where its last review stopped**: the App's latest review on GitHub, and its commit.
   A head that has not moved repeats the last verdict. Otherwise it reviews only what changed
   since.
3. **Reviews** and writes a verdict: **Approve**, **Nits** or **Changes needed**.
4. **Submits one review per round**, with findings as inline comments on the diff:
   - **Approve** and **Nits** approve.
   - **Changes needed** comments, or requests changes when its last review approved, so the
     earlier approval does not stand.
   - It never approves or requests changes on a pull request the App opened, or one whose
     author it cannot read.
   - When GitHub refuses an inline note, it posts the review again with the notes in the body.
5. **Retitles the status comment** to **Approved**, **Changes requested**, **Reviewed** or
   **Review did not finish**, and never writes an error or a task id on the pull request.

Its final message is one line with the link, and it stays in Podium.

## Use it

Copy the playbook and its prompt into your profile, or create the same playbook on the Playbooks
screen and paste the prompt. Then set `repos` to your repository:

```sh
cp examples/review/playbooks/review.yaml "$PODIUM_AGENT_PROFILE_DIR/playbooks/"
cp examples/review/prompts/review.md     "$PODIUM_AGENT_PROFILE_DIR/prompts/"
```

Then decide what starts a review, under **Routines** in the web UI:

- **When a pull request opens:** the **Review pull requests** template. Its events are
  **Opened** and **Ready for review**, drafts are skipped, and **The repository's review
  playbook** does the work.
- **After Podium pushes a fix:** a second routine on **Got new commits**, with the App's bot
  login (for example `podium[bot]`) under **Only these people**, so people's own pushes don't
  start a review each. The same playbook does the work, and step 2 makes it review only what
  the fix changed.
- **When someone asks:** `@podium review` or `/review` on a pull request. The template has it
  already: **Asked with a command**, with the command `/review`. Turn on **Only people in
  Podium** to leave strangers' requests unanswered.
