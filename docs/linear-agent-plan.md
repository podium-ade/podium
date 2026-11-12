# Plan: Linear as an agent app

Status: proposed, not started. Written 2026-11-12.

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
- **One conversation is one agent session.** The source key is
  `linear:session:<agentSessionId>`. Earlier sessions on the same issue reach the transcript
  through the issue's comments.
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
  polled sessions keep working.
- **Leadership:** the webhook source runs on every conductor with no leader lock. `ExternalID`
  is the prompt's activity id, which a retry keeps. The conductor dedupes on it only for a
  mirrored source, and Linear sessions are not mirrored until phase 6, so the source itself
  remembers delivery and activity ids for seven hours, past Linear's last retry.
- **Listener:** a small server in `internal/agent/webhooks` owns the webhook listeners by
  address, and the GitHub and Linear sources mount their paths on it.
- **On start:** moving the issue to `started` and setting the delegate moved to phase 3.

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
| `React` | nothing; Linear derives the session state from the last activity |

- **Links:** the Podium task page goes into the session's `externalUrls` when the task is
  created, and a pull request URL is added when one opens.
- **Stop:** a `prompted` event carrying `signal: stop` cancels the turn, then posts a closing
  `response`. How a source reaches turn cancellation still has to be worked out; the web
  chat's cancel path is the starting point.
- **Stale sessions:** send a keep-alive `thought` every 20 minutes for long tasks.

## Phase 4: identity

- **Provider:** add `SpeakerLinear` in `conductor/source.go` and the store's identity
  providers, with `SpeakerID` as `<organizationId>/<userId>`.
- **Resolution:** a `resolveLinear` in `conductor/identity.go` beside `resolveSlack`. The
  creator's email is a candidate login, kept only if that login has signed in and passes
  `domainAllowed`. It is cached like Slack's.
- **Admission:** `admit` refuses an unresolved speaker in session mode. Poll mode stays
  unauthenticated and the docs say so.
- **Effect:** turns run with the asker's secrets, MCP servers and GitHub account.
- **Docs:** rewrite the Linear paragraphs in `docs/security.md` and `SECURITY.md`. The
  "anyone who can assign runs code" warning then applies only to poll mode.

## Phase 5: routing

- **Delegation** runs a playbook as a task. A `/playbook` prefix in the prompt picks one;
  otherwise the `linear: true` playbook runs.
- **@mention** with no delegation goes to the assistant on the host when the host runtime is
  on, and it may delegate from there. Without the host runtime it runs the `linear: true`
  playbook.
- **Later, if wanted:** a `linear_teams:` list on a playbook, mirroring `slack_channels`.

## Phase 6: chat mirror and docs

- **Mirror:** Linear sessions get a chat row, like Slack threads and pull requests, so they
  appear in the web UI with a link back and `startchat` works from them. This needs a
  migration.
- **Docs:** `docs/agent.md`, `docs/networking.md` (the "Linear is still polling" line),
  `docs/operations.md`, and the README environment table.

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
it closes the security gap. Phases 5 and 6 are independent of each other.

| Phase | Rough size |
|---|---|
| 0: spike | half a day |
| 1: connection | one day |
| 2: webhooks in | one day |
| 3: activities out | one day |
| 4: identity | half a day |
| 5: routing | one day |
| 6: chat mirror and docs | one day |
