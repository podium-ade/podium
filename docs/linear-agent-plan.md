# Plan: Linear as an agent app

Status: built, by the branches `feature/linear-1-connection` (phase 1), `feature/linear-2-webhooks`
(phase 2), `feature/linear-3-activities` (phase 3), `feature/linear-4-identity` (phase 4),
`feature/linear-5-mirror` (phase 5) and `feature/linear-6-routing` (phase 6), each stacked on the
one before, then delegation routing, [issue conversations](#issue-conversations) and [tickets in a chat](#tickets-in-a-chat). Written 2026-11-12. Nothing has been tried against a real Linear workspace with a
running conductor yet; see [What is left](#what-is-left).

This moves Linear from a polled bot seat to a Linear agent app with webhooks, live
activities and per-person identity, in six phases that each ship alone.

## Where Linear is today

The code is in `internal/agent/linear/`.

- **Login:** a personal API key on a bot user's seat (`PODIUM_AGENT_LINEAR_API_KEY`). It can
  only be set in the environment, not under Access → Connections.
- **How work arrives:** the source polls for issues assigned to the bot every 30 seconds. One
  conductor polls at a time (`source:linear` lease).
- **What runs:** the single playbook marked `linear: true`, as a task.
- **What it says back:** a "working…" comment that is edited as the turn progresses and
  rewritten at the end. It also moves the issue's state and uploads attachments.
- **Who it runs as:** nobody. Linear has no accounts in `conductor/identity.go`, and
  `docs/security.md` says anyone who can assign the bot a ticket can run code on a worker.

The comment at the top of `client.go` explains the polling: webhooks need an OAuth
`actor=app` application and an inbound listener, and Podium had no inbound listener when it
was written. The GitHub App has since added one (`POST /webhooks/github`, HMAC-verified, on
a second listener), so that reason no longer holds.

## Decisions

- **Auth: client-credentials grant.** Linear OAuth apps support
  `grant_type=client_credentials`, which gives an app-actor token lasting 30 days with no
  refresh token. The operator pastes a client id and secret, with no redirect flow, and the
  conductor re-requests on a 401. The fallback is the authorization-code flow with
  `actor=app` (24-hour access tokens, rotating 30-day refresh tokens).
- **One conversation is one issue** (since `feature/linear-9-issue-sessions`; it was one agent
  session, `linear:session:<agentSessionId>`, before). The source key is `linear:<issueId>`, the
  key the poller already used, and every agent session on the issue is a turn of it; the turn's ref
  still names its agent session, which is where its replies go. See [Issue
  conversations](#issue-conversations).
- **Polling stays, untouched.** The environment key alone runs the poller as it does today.
  A saved connection wins and turns the poller off, so a ticket is never started twice.
- **An unmatched Linear user is refused.** They get an `error` activity and nothing runs,
  the same rule as Slack.
- **Each app names its own webhook address, and the two may be the same.** The Linear
  connection stores a listen address of its own. When it equals the GitHub App's, one
  listener serves `/webhooks/github` and `/webhooks/linear`.
- **Minting a token does not revoke the last one.** A token from an earlier
  client-credentials grant kept working after a second grant, so conductors that race to
  mint do no harm. They still share one token through the vault.

## What Linear's docs say

Checked on 2026-11-12 against `linear.app/developers` (agents, agent-interaction,
agent-best-practices, agent-signals, oauth-actor-authorization, oauth-2-0-authentication,
webhooks).

- **OAuth:** authorize at `https://linear.app/oauth/authorize`, token at
  `https://api.linear.app/oauth/token`, revoke at `https://api.linear.app/oauth/revoke`.
  `actor=app` installs as the app and needs a workspace admin. Agent scopes are
  `app:assignable` and `app:mentionable`, requested with `read` and `write`.
- **App user:** `{ viewer { id } }` with the app token. The id is unique per workspace.
- **Webhook headers:** `Linear-Delivery`, `Linear-Event`, `Linear-Signature`,
  `Linear-Timestamp`. The signature is a hex HMAC-SHA256 of the raw body, keyed with the
  webhook's signing secret. The body carries `webhookTimestamp` in milliseconds; reject
  anything more than 60 seconds off.
- **Webhook deadlines:** answer 200 within 5 seconds. Failures are retried three times, after
  1 minute, 1 hour and 6 hours, and Linear may then disable the webhook.
- **`AgentSessionEvent`:** `created` (a delegation or an @mention) and `prompted` (a
  follow-up, with the text in `agentActivity.body`). The payload has `agentSession` (with
  `issue` and `comment`), `previousComments`, `guidance` and `promptContext`.
- **Session deadlines:** the first activity or `externalUrls` update must arrive within 10
  seconds of `created`, or the session is marked unresponsive. A session goes stale after 30
  minutes without an activity and recovers on the next one.
- **Session states:** `pending`, `active`, `error`, `awaitingInput`, `complete`, `stale`,
  derived by Linear from the last activity.
- **Activities:** `agentActivityCreate` with `agentSessionId` and `content`. Content types
  are `thought` (`body`), `action` (`action`, `parameter`, optional `result`), `elicitation`
  (`body`), `response` (`body`) and `error` (`body`). A `thought` or `action` may be
  ephemeral, replaced by the next activity.
- **Signals:** `stop` arrives on a user's `prompt` and the agent must halt and close with a
  `response` or `error`. `auth` and `select` go out on an `elicitation`.
- **Session update:** `agentSessionUpdate` takes `externalUrls` (`{label, url}` entries),
  `addedExternalUrls`, `removedExternalUrls` and `plan` (`{content, status}` entries).
- **Delegation:** assigning an issue to the app sets `Issue.delegate`, and the human stays
  the assignee. Linear recommends moving the issue to the team's lowest-position `started`
  state unless it is already started, completed or cancelled.
- **Revocation:** an `OAuthApp` webhook with action `revoked` when the app is de-authorized;
  a `PermissionChange` webhook with action `teamAccessChanged` when team access changes.

## Phase 0: spike against a real workspace (done 2026-11-12)

A throwaway listener behind a tunnel, an OAuth app in a real workspace, and one test issue
that was delegated, replied to, stopped and @mentioned. Sanitized payloads are in
`internal/agent/linear/testdata/`.

| Question | Answer |
|---|---|
| Does client credentials give a usable app user? | Yes. A 30-day token with all four scopes, and the app user is delegable and mentionable in the app's own workspace with no install step. |
| What is in the `AgentSessionEvent` payload? | `organizationId`, `appUserId`, `oauthClientId`, `agentSession` (with `creator`, `issue`, `comment`, `status`, `externalUrls`, `sourceMetadata`), `agentActivity` (the prompt, with `user`, `signal`, `content.body`), `previousComments`, `guidance`, `promptContext`. |
| Is the person's email available? | Yes, twice: `agentSession.creator.email` and `agentActivity.user.email` are in the payload, and `users { email }` works with the app token. No extra query is needed. |
| How does a stop arrive? | As a `prompted` event with `agentActivity.signal: "stop"` and `agentSession.status: "stopping"`. |
| What is the ephemeral key? | `ephemeral: Boolean` on `AgentActivityCreateInput`. |
| What are the rate limits? | 5,000 requests and 2,000,000 complexity points per hour. |
| Can the app set the delegate? | It does not need to. Linear sets `delegate` to the app on delegation and leaves the assignee alone. `IssueUpdateInput.delegateId` exists for the @mention case. |

What else the spike showed:

- **Delivery is fast.** Webhooks arrived 150 to 600 ms after the event.
- **The speaker is `agentActivity.user`, not `agentSession.creator`.** A follow-up can come
  from somebody other than the person who started the session, so identity is resolved per
  prompt.
- **Delegation and mention are told apart by `agentSession.sourceMetadata`.** It is null on a
  delegation, whose prompt body is "Work on [EME-5](…)", and `{"type": "comment", …}` on an
  @mention, whose prompt body is the comment.
- **The same trigger arrives twice.** A delegation also sends `AppUserNotification` with
  action `issueAssignedToYou`, and a mention sends `issueCommentMention`. The source acts on
  `AgentSessionEvent` only, and "Inbox notifications" need not be ticked on the app.
- **Linear writes the session into the issue's comments.** Each prompt, `elicitation` and
  `response` becomes a comment under a thread Linear opens ("This thread is for an agent
  session with …", authored by a bot actor with a null `user`). `thought` and `action` do
  not. `FetchTranscript` can keep reading comments, and has to skip that opening comment.
- **Linear does not move the issue.** It stayed in Backlog, so moving it to `started` is the
  source's job, as planned.
- **A stopped turn must really be cancelled.** The spike's pending reply was posted after its
  "Stopped" response, and Linear accepted it.
- **Not captured:** the `OAuthApp` `revoked` and `PermissionChange` payloads.

## Phase 1: the connection

- **Storage:** `connection.linear` in `internal/agent/connections/connections.go`, holding
  the client id, the webhook listen address, and the app user and workspace the check found.
  The client secret, the webhook signing secret and the cached token go in the vault under
  `spec.ConnectionSecretPrefix`.
- **API:** `SetLinearConnection` and `ClearLinearConnection` in
  `proto/podium/agent/v1/agent.proto`. The `LinearConnection` placeholder message becomes
  real.
- **Live reload:** `internal/agent/live.go` gains a third source beside Slack and GitHub.
  `Check` fetches a token and runs `viewer` before anything is saved.
- **UI:** a Linear card in `web/src/components/agent/ConnectionsPanel.tsx` showing the
  webhook URL to paste into Linear and the scopes to request.
- **Client:** `client.go` takes a token source. OAuth tokens go out as `Bearer`; the personal
  key stays bare.

## Phase 2: webhooks in

- **Handler:** a new `internal/agent/linear/webhook.go` modelled on
  `internal/agent/ghreview/webhook.go`. It verifies `Linear-Signature`, rejects a
  `webhookTimestamp` more than 60 seconds off, dedupes on `Linear-Delivery`, and caps the
  body at 1 MB.
- **Fast return:** it answers 200 within the 5-second limit and does the work after.
- **Instant acknowledgement:** on `created`, the source itself posts a `thought` before the
  event reaches the conductor. Playbook selection and session setup can take longer than the
  10 seconds Linear allows.
- **Events:**
  - `created` becomes an `InboundEvent` with `Mentioned: true`. For a delegation the text is
    the issue, worded as the poller words an assignment; for a mention it is the comment, less
    the mention. `promptContext` is not used: the transcript carries the issue.
  - `prompted` becomes a follow-up with `agentActivity.body` as the text.
  - `OAuthApp revoked` stops the source and marks the connection broken in the UI.
- **Ref:** it grows a fourth part, the agent session id. Three-part refs still parse, so
  polled sessions keep working. Phase 3 adds a fifth, `delegated`, to a session opened by
  delegation, because only that kind moves the issue and the ref is what survives a restart.
- **Leadership:** the webhook source runs on every conductor with no leader lock. `ExternalID`
  is the prompt's activity id, which a retry keeps. The conductor dedupes on it only for a
  mirrored source, and Linear sessions are not mirrored until phase 5, so the source itself
  remembers delivery and activity ids for seven hours, past Linear's last retry.
- **Listener:** a small server in `internal/agent/webhooks` owns the webhook listeners by
  address, and the GitHub and Linear sources mount their paths on it.
- **On start:** moving the issue to `started` moved to phase 3. Setting the delegate was
  dropped: Linear sets it on delegation.

## Phase 3: activities out

A second implementation of `conductor.Source` for session-mode refs.

| Conductor call | Linear |
|---|---|
| `Post` progress | ephemeral `thought` |
| `PostActivity` (tool calls, reasoning) | ephemeral `action`; Linear becomes an `ActivityPoster` |
| `Post` question | `elicitation`; the reply arrives as `prompted` and is injected into the parked container |
| `Post` final | `response` |
| `Post` failure | `error` |
| `Attach` | done in phase 2: uploaded as today and linked in a response of its own, since an activity cannot be edited; over 50 MB falls back to the task link |
| `React` | working moves a delegated issue to the team's lowest-position `started` state and starts the keep-alive; Linear derives the session state from the last activity |
| `LinkPullRequests` | each pull request the answer named, added to the session's links |

- **Links:** the Podium task page is added with `addedExternalUrls` the first time a turn
  says anything once its task exists (the placeholder comes before the task), and each pull
  request the answer names is added when the turn ends.
- **Stop:** a `prompted` event carrying `signal: stop` cancels the turn, then posts a closing
  `response`. The source is handed a `Stop` function, which is the conductor's
  `CancelSessionTurn`: it cancels the running turn's task and drops the turns queued behind
  it, because Linear forbids acting in a session after a stop. The source also refuses
  anything the stopped turn says as it unwinds.
- **Stale sessions:** a keep-alive `thought` after 20 minutes without an activity, for as long
  as the turn is working.

## Phase 4: identity

- **Provider:** add `SpeakerLinear` in `conductor/source.go` and the store's identity
  providers (migration 0045 widens the provider check), with `SpeakerID` as
  `<organizationId>/<userId>`.
- **Resolution:** a `resolveLinear` in `conductor/identity.go` beside `resolveSlack`. The
  email of the prompt's author (`agentActivity.user`, not `agentSession.creator`) is a
  candidate login, offered only when `user(id:)` says they are `active` and neither a `guest`
  nor an `app`, and kept only if that login has signed in and passes `domainAllowed`. Both
  halves are cached like Slack's.
- **Admission:** `admit` refuses an unresolved speaker in session mode, told by an `error`
  activity, or a lasting `thought` when somebody else's turn is open in the session. The app
  names its provider on every prompt, so a Linear event with no provider is the poller's. Poll
  mode stays unauthenticated and the docs say so. A Stop is not gated.
- **Effect:** turns run with the asker's secrets, MCP servers and GitHub account, and the
  company's model key: a Linear issue is a shared conversation, like a Slack thread.
- **Docs:** rewrite the Linear paragraphs in `docs/security.md` and `SECURITY.md`. The
  "anyone who can assign runs code" warning then applies only to poll mode.

## Phase 5: chat mirror

This comes before routing because answering a mention with the assistant is a conversation, and
a conversation in the conductor is built on a chat row.

- **Mirror:** Linear sessions get a chat row, like Slack threads and pull requests, so they
  appear in the web UI with a link back and `startchat` works from them. The chat row brings
  the conductor's own dedupe on `ExternalID` and the delivery outbox to Linear.

## Phase 6: routing

- **The source says what happened, the conductor routes.** The app no longer names a playbook
  (`InboundEvent.Playbook` stays the poller's, which keeps its behaviour byte for byte). It sets
  `InboundEvent.Delegated` on every prompt of a session opened by delegation, read off
  `agentSession.sourceMetadata` as the ref's fifth part is.
- **Delegation**, where there is a host runtime, is a conversation as a mention is (see
  *Delegation routing*, below). Without one it runs a playbook as a task: a `/playbook` prefix in a person's words picks
  one for that turn — a delegation's opening prompt is Linear's own, so in practice a follow-up;
  otherwise the session's playbook runs, which is the `linear: true` playbook (`Routing.Default`,
  after the prefix). Unlike a Slack thread, a Linear session does not refuse a second playbook: a
  `/name` runs for that turn alone, and is never injected into a task that is already up.
- **@mention** with no delegation is a conversation when the host runtime is on: the assistant
  answers on the host with the issue and its thread as context, and may delegate to any playbook
  it would be offered in Slack — `linear: true` plays no part in that menu. Its follow-ups stay
  with the assistant. A mention that starts with a `/playbook` on that menu runs that playbook
  directly as the conversation's task (`runDirect`), as a pull request's review is delegated
  without asking the assistant. Without the host runtime a mention runs the `linear: true`
  playbook as a task, as before, and is not refused.
- **What the session shows of a conversation.** The assistant's turn has no task: no task
  footer, no task link. Each task it delegates is linked as `Podium task: <playbook> (<id>)` and
  drawn on its own — its progress a thought even after the assistant answered, its answer a
  response naming its task, its question an elicitation — and the session is kept alive while any
  of them works.
- **Stop on a conversation** stops the assistant's turn and every task the session delegated that
  is still running, announcing each task id to every conductor's source before cancelling it,
  drops what is queued, and closes the session with one "Stopped.".
- **`linear_teams:` and `linear_labels:` on a playbook, mirroring `slack_channels`:** built, and
  delegation now goes to the assistant unless they claim the issue. See *Delegation routing*
  below.
- **Docs:** `docs/agent.md`, `docs/networking.md`, `docs/operations.md`, `docs/security.md`, the
  README and `deploy/.env.example`.

## Delegation routing

Built after phase 6, on `feature/linear-8-delegation-routing`.

- **Rules first.** A playbook may set `linear_teams` (team keys) and `linear_labels` (label names,
  without case). It claims a delegated issue when it sets either, the team is in `linear_teams`
  or that list is empty, and a label is in `linear_labels` or that list is empty. Exactly one
  claiming playbook, out of those the assistant would be offered (`menuFor`), runs directly as
  the session's task (`claimedPlaybook`, then `runDirect`), as a `/name` on a mention does.
- **Otherwise the assistant decides.** A delegation is a conversation like a mention
  (`conversational` no longer looks at `Delegated`): the assistant is told the issue was delegated
  to it, delegates from its menu or asks in the session. Several claiming playbooks fall through
  to it too. Follow-ups are the assistant's, as a mention's are.
- **What the rules see.** The app sets `InboundEvent.DelegatedIssue` on a delegation's opening
  prompt only: the team key from the payload, and the labels from one `PodiumIssueRouting` query
  with a five-second limit. A failed query marks the issue unknown, which no rule claims.
- **`linear: true`** is the poller's, unchanged. A saved app no longer needs one; it is still
  what a delegation runs on a conductor with no host runtime, which our installs do not have.
- **Editor.** The playbook page sets both lists under Routing, with teams and label suggestions
  from `ListLinearCatalog` (the app's `teams` and `issueLabels`), and a line saying when it runs.

## Issue conversations

Built on `feature/linear-9-issue-sessions`, after delegation routing. What the user asked for:
every ticket has one conversation however it was started, a routine on Linear events leaves no
empty chat, and hundreds of them are listed somewhere other than the chat rail, to those who
touched the ticket and to admins.

- **Keys.** `MirrorKey` and `InboundEvent.SourceKey` are `linear:<issueId>`. The ref keeps the
  agent session as its fourth part, so `Post`, `Edit`, `PostActivity`, `React`, `Attach` and
  `LinkPullRequests` still reach the session that prompted the turn, and the outbox, a turn's
  `trigger_ref` and a delegation's `trigger_ref` keep that mapping across a restart. A project is
  `linear:project:<id>`, for routines.
- **Threads.** A conversation may hold threads (`conductor.ThreadKeyer`); the app's thread is the
  agent session (`linear:session:<id>`), and a routine's run is a thread of its own. A prompt is
  put into a running turn, a parked task or a warm task only from the same person *and* the same
  thread (`sameAsker`), never across conductors when it has a thread, and never into or from a
  routine's run.
- **Stop.** `AppOptions.Stop` takes the issue key and the session's thread key, and the conductor's
  `CancelThreadTurn` stops only that thread's running turn, the delegations whose `trigger_ref`
  is that thread's, and its queued prompts (`ListQueuedTurns`, `DeleteQueuedTurn`). The stop is
  announced by the thread key, which is what `StoppedElsewhere` already read.
- **Routines.** A routine in per-ticket mode runs an issue's or project's events as a turn of that
  conversation: the scheduler opens it when needed (`EnsureLinearConversation`: a mirrored chat of
  origin `linear` and its session), and the web chat starts the run there as the routine's owner,
  or nobody, with the routine's assistant (`chat.RoutineRun.Conversation`). The brief's routine
  block, `routine_finish` and the idle hold find the routine by the run, not the chat. The
  instruction is preceded by `routines.ConversationPreamble`, which tells the run that earlier
  turns and their tasks are in the conversation and its brief and not to redo or duplicate them.
  No runtime change. A turn of a routine never falls back to the chat owner's identity
  (`turnLogin`, `delegate`). `routine_subject_chats` and the 50-chats-a-day guard are gone.
- **No empty chats.** `routines.chat_id` is nullable (0048). A routine on Linear events gets no
  chat when it is saved; one in routine mode gets it on its first run (`EnsureRoutineChat`), and so
  does a per-ticket one the first time an event about a cycle or a label needs it. Moving one to a
  schedule opens its chat. The Routines page opens a routine with no chat on a page of its own
  (`/agent/routines/:id`) with its card and its runs, each linking to where it ran
  (`RoutineRun.chat_id`). A paused routine with no chat gets no inbox notice.
- **Who reads one.** `linear_people` records who touched an issue as the deliveries arrive — an
  agent session's creator and prompt speaker, an `Issue` change's creator, assignee and actor, a
  `Comment`'s author and actor, a routine's owner — keyed by Linear account (`<org>/<user>`), or by
  login for a routine's owner. `chat_role` and `chat_visible_to` resolve accounts to logins through
  `identities` when somebody reads (`linear_touched`), so a link made later counts, and grant
  `viewer`. Admins and owners read every one through the API (`Infra`). Writing is still a
  participant's.
- **The Linear view.** `ListLinearConversations`, filtered (mine, team, state) and keyset-paged on
  the server, with what the deliveries said about each issue kept in `linear_issues` (identifier,
  title, link, team, Linear state) and whether it waits on a person (`asked_at`, set on a question,
  cleared by the next thing said from Linear). Issue conversations are left out of `ListChats` and
  `ListSessions`; the web UI's Work → Linear lists them. (The chat rail lists the ones whose
  people the viewer is one of since *Ticket chats*, below.)
- **Existing rows.** Nothing is migrated. Chats keyed `linear:session:<id>` stay readable by their
  participants and listed under Sessions; the next prompt on their issue opens the issue's
  conversation. A per-ticket routine's old chats stay ordinary chats of the routine
  (`chats.routine_id`); its next event about the ticket runs in the ticket's conversation. Merging
  them was not worth it for rows that only ever existed on development stacks.

## Ticket chats

Built on `feature/linear-10-ticket-chats`, after issue conversations. What the user asked for: a
ticket's conversation in the chat rail of the people on it, readable by them without leaking the
work of turns somebody else ran, and written from Podium as well as from Linear.

- **Who ran a turn, and who sees its work.** A turn runs as whoever prompted it. Every row the bot
  wrote in a Linear conversation carries `ran_as` (login and name) or `ran_as_routine`, read from
  `turns.started_by` and the routine run, through `ListChatTurnRunners`: a row with a task id is
  the turn that ran the task (its own, or one it delegated), and the assistant's own rows the turn
  that was running when they were said. No column was added; a conversation runs one turn at a
  time. A reader who is neither the runner nor an admin or owner (`Infra`) gets the prompt and the
  answer, and every tool call, thought and progress line as a `redacted` row with no text; the
  answer's task id and attachments are withheld too, and so are a live progress line and a status
  frame's task id while the turn is somebody else's, and a turn's task in `ListTurns`
  (`api/runner.go`). A row no turn can be placed on is nobody's: only admins see it. The UI counts
  the redacted rows into "4 steps, ran as Alice" and says "ran as …" on every answer. **Only Linear
  conversations:** a shared web chat's or a Slack thread's people chose to share it and have always
  seen the whole trail.
- **The rail.** `ListChats` lets back in, from the Linear conversations it leaves out, an issue's
  conversation whose people the caller is one of: they touched it through a linked Linear account
  (`linear_people` through `identities`) or spoke in it. Not an admin who did not, not a routine's
  owner as such, not the dev token, not a project's conversation and not a pre-0048 session's. It
  carries `linear_issue` (identifier, title, state, `asked_at`, and `waiting_on_me`: asked by the
  caller's turn, or by nobody's), and is ordered by last activity with the rest. The row reads
  `EME-6 · title` with a dot for the state and *Waiting on you*. `SetChatRailHidden` hides one
  (`chat_rail_hidden`, 0049) until something new is said in it; it offers no Rename, Leave or
  Delete.
- **Who writes it.** Touching the issue now grants `editor` (`chat_role`, 0049): the rail is the
  issue's people, and a reply from Podium is what an @mention in Linear would be, as the same
  person. An admin or owner who did not touch it reads and does not write.
- **Replies reach Linear** (`conductor/relay.go`). A person's web message in an issue's
  conversation — addressed to Podium or not — is owed to the issue through the outbox as an
  `OutRelay`: a comment from the app with `createAsUser` and `displayIconUrl` (their Podium name and
  picture). The answer of a turn a person started there, its failure or question, and the answers
  of the tasks it delegated are owed as the app's comment, through the same session-less path the
  poller's tickets use. Not into an agent session: a web turn has none, and posting into one a
  Linear person opened would reopen a finished session and put the answer away from the message.
  A routine's run is not relayed. Both show "not delivered" in the web chat and are retried.
- **No loop.** The app drops an agent session prompt whose speaker is its own user or that came
  from a comment it posted as somebody (remembered for `dedupeTTL`), and treats a `Comment` data
  change by its user or about such a comment as its own (no routine). A transcript reads a comment
  posted as somebody (`botActor.userDisplayName`) as theirs, not the app's answer.

## Tickets in a chat

Built on `feature/linear-11-ticket-chips`, after ticket chats. What the user asked for: a
Linear issue named in any chat is a chip with a card, the assistant is given the tickets a person
names, and an issue's conversation shows the issue beside it. See [agent.md, Tickets in a
chat](agent.md#tickets-in-a-chat).

- **Matching.** `linear.FindIssueRefs` (Go) and `findIssueRefs` (web) find an upper-case
  identifier standing on its own, or a `linear.app/<workspace>/issue/<ID>` link. The web finds
  candidates; the conductor keeps those of a team the workspace has (`AppSource.TeamKeys`, the
  catalog, cached five minutes, and the teams in `linear_issues`).
- **Who sees an issue** is decided in `internal/agent/tickets` (`MayRead`): an admin or owner,
  somebody who touched it (`linear_touched`), or somebody whose linked Linear account
  (`identities`, provider `linear`, of the app's own workspace) Linear says is on its team
  (`PodiumUserTeams`, cached two minutes, an inactive account on none). Anything else is
  `ErrNotFound`, the same as an issue that does not exist.
- **Reading.** `GetLinearIssues(refs, detail)`: a chip (`detail` false) is the `linear_issues` row
  when it has a title, team and state, else Linear; a card (`detail` true) is Linear now
  (`PodiumTicket`, cached a minute per issue in `AppSource`), else the row. The answer carries the
  issue's conversation's chat id only when the caller may read it ("Continue in ticket").
- **The assistant's brief.** `Brief.LinearTickets` (`linear_tickets`, the runtime's
  `linearTicketSchema`), filled by `conductor.TicketBriefer` from a person's own words
  (`RecordText`, else `Text`) on a host turn, not a routine's run or an automatic event: up to three
  tickets the speaker may see, without the conversation's own issue, trimmed to 12 KiB. The runtime
  renders them as "Linear tickets this message names", fenced, data not instructions.
- **Who reads the answer** (`feature/linear-13-ticket-context-scope`). A ticket the speaker may
  see is briefed only if everybody who will read the answer could see it too
  (`tickets.audienceOf`, from the conversation's source key): `chat:<id>` checks `MayRead` for the
  chat's owner, named grants (`chat_access`) and participants (`chat_participants`), each one's
  Linear teams asked once per brief, and is nobody to vouch for — brief nothing — when the chat is
  `members` or `link` access, has a participant with no login, or more than `MaxBriefReaders` (20)
  readers besides the speaker; `linear:<issueId>` briefs only tickets of that issue's `team_key`
  (nothing when the webhooks never said it); every other key (Slack, GitHub, `linear:project:`,
  `linear:session:`) briefs nothing. `TicketBriefer` returns the count it withheld, which goes in
  the brief as `linear_tickets_withheld`; the runtime says "Linear tickets not shown" with the
  count and no identifiers, and tells the assistant not to guess or look them up to quote. Admins
  are not among the readers checked (they may see everything); the conductor still does not know
  roles, so one who is a named reader is checked by the member rule.
- **The panel.** `GetLinearIssueConversation(chat_id)`, to a reader of the chat: the issue (Linear
  now, else the row), the session's delegations and the routine runs whose `chat_id` is the chat
  (twenty each). The web merges Linear's GitHub attachments with the chat's own pull requests.
  It refetches when the stream's message count changes, on focus, and every thirty seconds.
- **No migration.** The queries are read-only (`queries/linear_tickets.sql`).

## Questions in the inbox

Built on `feature/linear-12-ticket-inbox`, after tickets in a chat. What the user asked for: a question
the agent asks in a ticket's conversation reaches the person it is asked of where they already
look, answered without opening the chat; reading and writing Linear from a chat through the
person's own Linear MCP server, not tools of Podium's; and the trust that follows written down.

- **The item.** `noteLinearQuestion` (`conductor/linear_conversation.go`), which already set
  `asked_at`, now also puts the question in the inbox of whoever it is asked of: a `question` row in
  `notifications` (0051) with the text in `detail`, for the person the turn that ran the asking task
  ran as (`ChatTurnRunners` and the turn index #300 built), else the turn running on this
  conductor. That is who prompted it, from Linear or the web, or a routine's owner for its run; a
  system routine's run asks nobody. `asked_of` is set the same way, which fixes a question from a
  task an earlier turn delegated being put on the latest turn's person. `ListNotifications` joins
  `linear_issues` for the ticket's identifier and title.
- **No duplicates.** A partial unique index on `(chat_id, login) where kind = 'question' and read_at
  is null`: one open item per person and conversation, and a second question before the first is
  answered replaces its text (`NotifyQuestion`, an upsert). A question is posted once by `post`, so
  an outbox retry adds nothing.
- **Closed by an answer from anywhere.** `clearLinearQuestion` — every event put to Podium in a
  Linear conversation, whichever source, which is what cleared `asked_at` — also closes the
  conversation's open questions (`ResolveQuestions`). An agent session prompt in Linear, a message
  in the chat and an answer from the inbox are all such events. Opening the chat, and *mark all
  read*, leave a question open (`MarkNotificationsRead` skips `question` unless named by id, and the
  web does not mark it seen on open); **Dismiss** names it.
- **Answering from the inbox is the chat.** The bell's row has a box that calls `SendChatMessage`
  on the conversation, nothing more, so it is relayed to the issue as the person's comment (#300's
  path) and goes where a reply typed in the chat would. Two things make that reach the question:
  - In the chat, while the issue waits on the sender (`asked_of`), their message is addressed
    without naming Podium, and is not held behind the turn waiting for it
    (`chat.answersQuestion`, through an optional `QuestionStore`).
  - In the conductor, a task awaiting a question takes the asked person's message from the web in a
    Linear conversation even when its turn began on another surface — a Linear agent session or
    their routine's run (`conductor.answersQuestion`). Before, a web reply to a question asked in an
    agent session was queued behind the task waiting for it. It is the same person's words to a task
    running as them; its answer still goes where its turn answers.
- **Linear MCP hint.** No custom Linear tools. `LinearMcpHint` shows one line in the Linear view and
  above an issue conversation's composer to a signed-in person with no enabled personal server named
  `linear` (the name the assistant asks for; a global fallback acts as whoever stored its key, so it
  does not count). It links to `/agent/mcp?add=linear`, which opens the MCP screen's add dialog on
  the Linear preset, and is dismissed per browser (`localStorage`).
- **Docs.** security.md: a question's inbox item, and that the person's Linear MCP server acts as
  them with nothing between the model and its tools while turns read ticket text other members
  wrote. agent.md: the inbox item and Linear-from-chat under *The Linear view*, and two example
  routines (a weekday digest, bulk triage) under *Routines*.

## Testing

- **Unit:** extend `stub_test.go` to serve the OAuth token endpoint and the agent mutations.
  Table tests cover signature, replay window and dedupe against the Phase 0 fixtures.
- **Integration:** the `fakesource` pattern covers the full path: a `created` event producing
  a thought within the deadline, a task, activities and a response. Separate cases cover an
  elicitation round trip, a stop, a refused speaker and a revoked app.
- **Live:** one pass per phase on the dev stack, against a real workspace through a tunnel.

## Order and size

Phases 1 to 3 are the core and only make sense together for users, though each merges alone
behind the unsaved connection. Phase 4 should land before this is documented as ready, since
it closes the security gap. Phase 6 builds on phase 5: routing a mention to the assistant
needs the session's chat row.

| Phase | Rough size |
|---|---|
| 0: spike | half a day |
| 1: connection | one day |
| 2: webhooks in | one day |
| 3: activities out | one day |
| 4: identity | half a day |
| 5: chat mirror | one day |
| 6: routing and docs | one day |

## What is left

Across the whole stack, what is still open:

- **Nothing has run against a real Linear workspace with a running conductor.** The spike
  (phase 0) captured payloads with a throwaway listener; every phase since was built against
  those fixtures and a GraphQL stub. One live pass per phase on the dev stack, through a tunnel,
  is still owed.
- **The stack's integration tests and `make e2e` have not been run.** They compile. The ones that
  matter here are in `internal/agent/conductor` (`gate_`, `linear_mirror_`, `cancel_session_`,
  `linear_routing_integration_test.go`) and need Postgres.
- **The `OAuthApp` `revoked` and `PermissionChange` payloads were never captured** from a real
  workspace. Revocation is handled from Linear's documentation; a team-access change is only
  logged.
- **A message owed from before a Stop can be posted after "Stopped." following a restart.** What a
  Stop silences is remembered in memory. An answer, question or failure that had already failed to
  deliver stays owed in the outbox, and a conductor that retries it after a restart does not know
  it was stopped. A cancellation itself is recognised everywhere by its cancel reason.
- **Outbox delivery is at least once.** A conductor that dies after Linear accepted an activity and
  before it recorded that can post it again.
- **A task the assistant was starting at the moment of a Stop** — recorded, not yet created — is
  not one the control plane can cancel, and runs; nothing it says is posted while the session
  stays stopped.
- **The source tells a delegated task from a turn's own in memory.** After a restart, a resumed
  delegated task is drawn as the session's turn (linked as `Podium task`, its answer taken as the
  turn's) until the next prompt; an answer the outbox retries from an earlier assistant turn,
  after the next has started, is taken as the new turn's.
- **A follow-up put into a task that has already answered** is said as responses rather than
  thoughts until that task answers again: nothing tells the source the task is working again. This
  was so for the linear: true playbook's warm tasks before phase 6, and holds for delegated ones.
- **Across conductors, a follow-up that names no playbook** can be injected into another
  conductor's warm task of a playbook a previous turn named for itself: the lease records the warm
  task's person and surface, not its playbook.
- **Conductors on one database must agree on the host runtime.** What answers a mention is
  decided by the conductor the webhook reaches.
- **Delegation routing by `linear_teams` and `linear_labels`** has unit tests; its integration
  tests (`TestALinearDelegation…` in `linear_routing_integration_test.go`) have not run against
  Postgres, and the `PodiumIssueRouting` and `PodiumCatalog` queries were written from Linear's
  schema, not tried against a workspace.
- **Routines on Linear events are built, against invented payloads.** Linear's documentation gives
  the data change envelope but no exact `Issue` or `Comment` body, so the team is read leniently
  (`teamId`, `team.key`, `team.id`, and a comment's `issue.teamId` or `issue.team`), and a team
  named only by id is looked up. No data change was captured from a real workspace, and the
  routine's integration tests have not run against Postgres. See [agent.md, On a Linear
  event](agent.md#on-a-linear-event).
- **Issue conversations** (`feature/linear-9-issue-sessions`) have unit tests for the keys, the
  threads, the scheduler and the store's interface; the store, conductor and API integration
  tests that cover them (`routine_subjects_integration_test.go`, `TestStoppingOneThread…`,
  `TestARoutineRunsInTheIssuesConversationAsItsOwner`) compile and have not run against
  Postgres, and nothing has been tried with two agent sessions on one real issue. Creator and
  assignee are read from data change payloads Linear does not document, and from an agent
  session's issue only if Linear ever sends them there.
- **Ticket chats** (`feature/linear-10-ticket-chats`) have unit tests for the visibility rule, the
  turn index, the rail's issue fields, the relay and the loop guards; the store test for the rail
  query, the hide and the new `chat_role` (`ticket_chats_integration_test.go`) compiles and has not
  run against Postgres. `createAsUser`/`displayIconUrl` and how Linear fills `user`, `botActor`
  and the webhooks for such a comment were written from Linear's documentation, not tried: if
  Linear makes a relayed comment that starts with a mention into an agent session with a person
  as its speaker, only the remembered comment id stops it, and that memory is one conductor's and
  `dedupeTTL` long. Attribution of the assistant's own rows by time assumes one turn at a time per
  conversation and conductors whose clocks agree.
- **Tickets in a chat** (`feature/linear-11-ticket-chips`) have unit tests for the matcher, the
  rule, the brief's framing and trimming, the Linear queries against the stub, and the web
  components; `PodiumTicket` and `PodiumUserTeams` were written from Linear's schema and not tried
  against a workspace (an `issue(id:)` lookup by identifier, `attachments.sourceType` and
  `user.teams` are what they rely on), and the store's queries have an integration test that has not
  run against Postgres. The brief's new `linear_tickets` field needs the runtime rebuilt with it:
  an older runtime refuses a brief that carries it (strict schema), so a turn naming a ticket fails
  until the runtime image and the chat host's renderer are updated. Team membership is Podium's
  narrower reading of who sees what (a public team's issues are not shown to non-members).
  Admins are briefed with tickets by the member rule, since the conductor does not know roles.
- **Who reads the answer** (`feature/linear-13-ticket-context-scope`) has unit tests for each
  surface (a private chat, a shared chat by grant and by participant, an open chat, a stranger in
  it, the reader cap, an issue's conversation by team, Slack and the others) and for the runtime's
  new block. The brief's `linear_tickets_withheld` field needs the runtime rebuilt with it, as
  `linear_tickets` did. The readers are read when the turn starts: somebody added to a chat after
  reads what it said before. A ticket in a public Linear team is still withheld from a Linear
  issue's conversation in another team, and a shared chat's readers are judged by Podium's
  narrower team rule, so more is left out than Linear would hide. Each reader costs a
  `linear_touched` query per ticket and one `IdentitiesOf` per brief; their teams come from the
  two-minute cache. Not tried on the dev stack.
- **Questions in the inbox** (`feature/linear-12-ticket-inbox`) have unit tests for the chat's and
  the conductor's answer rules and the web inbox, hint and deep link; the store test (create,
  dedupe, mark-all, resolve, dismiss: `question_inbox_integration_test.go`) and the conductor tests
  (a question from a Linear agent session in the inbox, closed by the answer in Linear and by one
  from the web) compile and have not run against Postgres. An answer from the web goes into a task
  waiting in a Linear agent session only on the conductor that runs it; another conductor queues it
  as a turn. Whether Linear shows the agent session's elicitation as answered when the answer
  arrives as a relayed comment rather than a prompt in the session was not tried. A question whose
  task ended without an answer keeps its item until somebody speaks or it is dismissed.
- **`make lint` was not run** on any of the stack's branches: golangci-lint cannot type-check on
  the machine they were built on. CI is the first place it runs.
