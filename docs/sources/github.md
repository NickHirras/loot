# GitHub

Turns a repository's public life into drops: stars, forks, issues, pull
requests and releases. It is the source that makes an open-source project feel
like it is being played rather than maintained.

It works two ways, and they are designed to be used together:

| | Latency | Setup | Works when Loot is offline |
|---|---|---|---|
| **Polling** | up to 10 minutes | a repo name | yes — it catches up on the next poll |
| **Webhook** | instant | a webhook on GitHub | no — GitHub retries briefly, then gives up |

Both paths mint the same dedupe keys (`github:star:<repo>:<login>` and
friends), so running them together gives you real-time drops without doubles:
whichever path sees an event first wins and the other one collapses in the
pipeline. Start with polling, add the webhook when you want the star to land
while the person is still on the page.

## Configure

```yaml
sources:
  github:
    # "owner/name", one per repository you want to watch.
    repos:
      - nickhirras/loot
      - yourname/yourapp
    # Optional. Public repos work without one, at a lower rate limit.
    token: ""
    # Optional, but set it if you mount the webhook.
    webhook_secret: ""
    # How far back the first poll looks. Defaults to 30.
    backfill_days: 30
```

Environment overrides: `LOOT_GITHUB_REPOS` (comma separated),
`LOOT_GITHUB_TOKEN`, `LOOT_GITHUB_WEBHOOK_SECRET`,
`LOOT_GITHUB_BACKFILL_DAYS`.

### Token scopes

| What you watch | Token | Scopes |
|---|---|---|
| Public repositories | optional | none — a token with **no scopes at all** still lifts you from 60 to 5,000 requests an hour |
| Private repositories | required | classic: `repo`. Fine-grained: **Contents: read-only**, **Issues: read-only**, **Pull requests: read-only**, **Metadata: read-only** |

Create one at **Settings → Developer settings → Personal access tokens**. A
scope-less classic token is the right answer for public repos: it costs
nothing if it leaks and it buys you eighty times the rate limit.

### Backfill

The first poll of each repository is the only one that looks backwards, and it
only reaches `backfill_days` (default 30). Everything older is used to set the
cursors, not to fire drops — installing Loot on a five-year-old project should
not replay five years of stars into your feed.

Star **milestones** work the same way: the first poll records where you already
stand, so a repo that arrives with 3,000 stars announces 5,000 when it gets
there, not 10, 50, 100, 250, 500, 1,000 and 2,500 on its first evening.

Override the window once at startup with `loot serve --since 2026-01-01`.

## What lands in the feed

| Kind | When | Dedupe key | Payload |
|---|---|---|---|
| `star` | someone stars the repo | `github:star:<repo>:<login>` | `user` |
| `stars_milestone` | the total crosses 10, 50, 100, 250, 500, 1k, 2.5k, 5k, 10k | `github:stars_milestone:<repo>:<n>` | `stars` |
| `fork` | someone forks it | `github:fork:<repo>:<login>` | `user`, `url` |
| `issue_opened` | a new issue ("a quest appears") | `github:issue_opened:<repo>:<n>` | `number`, `title`, `user`, `url` |
| `issue_closed` | an issue is closed | `github:issue_closed:<repo>:<n>` | same |
| `pr_opened` | a pull request is opened | `github:pr_opened:<repo>:<n>` | same |
| `pr_merged` | a pull request is **merged** | `github:pr_merged:<repo>:<n>` | same |
| `release` | a release is published | `github:release:<repo>:<tag>` | `tag`, `name`, `url`, `prerelease` |

`App` is the `owner/name` of the repository, so the feed and the vault group by
project.

Notes on the edges:

- A pull request is never reported as an issue, even though GitHub's issues API
  returns both. Loot tells them apart by the `pull_request` field.
- A pull request that is closed **without** being merged produces nothing. It
  is not a win, and the feed is for wins and warnings.
- Draft releases are skipped; prereleases are kept and flagged
  `"prerelease": true` in the payload, so you can write a rule that treats a
  beta more quietly than a `.0`.

The default rules title these — "A quest appears: #7", "Issue slain: #5",
"Release shipped: v1.2.0". Change them in your own rules file; see
`internal/rules/default.yaml` for the format.

## Rate limits

Unauthenticated polling gets 60 requests an hour and each repository costs
about five per poll, so one repo fits comfortably and three do not. Add a
scope-less token and the ceiling becomes 5,000.

When GitHub reports `X-RateLimit-Remaining: 0`, Loot stops calling until
`X-RateLimit-Reset` passes and says so once at info level:

```
level=INFO msg="github: rate limit exhausted, skipping poll" until=2026-08-18T13:00:00Z
```

It is not an error and it does not lose anything: the cursors are untouched, so
the next successful poll picks up exactly where it left off.

The stargazers endpoint has no `since` parameter and returns oldest first, so
Loot reads the repo's total star count, jumps straight to the **last** page and
walks backwards only as far as the cursor. A repository with 40,000 stars costs
the same two requests per poll as one with 40.

## Webhook (real time)

Set a secret first — the endpoint mints drops, so leave it open only on a
private network:

```yaml
sources:
  github:
    webhook_secret: "choose-a-long-random-string"
```

Then, in the repository, go to **Settings → Webhooks → Add webhook**:

- **Payload URL**: `https://your-loot-host/hooks/github`
- **Content type**: `application/json`
- **Secret**: the same string
- **SSL verification**: enabled
- **Which events**: *Let me select individual events*, then tick
  **Stars**, **Forks**, **Issues**, **Pull requests** and **Releases**.
  (*Watches* also works; GitHub's legacy `watch` event is starring, and Loot
  maps it to the same `star` drop.)

GitHub sends a `ping` on save. Loot answers `200 pong`, and the delivery shows
a green tick in the webhook's **Recent Deliveries** tab.

Everything else answers with JSON:

```json
{"emitted":1,"event":"star","ok":true}
```

`"emitted":0` means the delivery was understood and deliberately ignored — a
label added to an issue, a release edited, a PR closed unmerged.

An organisation-wide webhook (**Organisation settings → Webhooks**) works too,
and reports every repo at once; Loot attributes each event to the
`repository.full_name` in the payload, so only repos in your `repos:` list are
polled but any repo may push.

### Signature verification

With `webhook_secret` set, every delivery must carry a valid
`X-Hub-Signature-256`; anything else is a `401` and nothing is emitted. Without
a secret, deliveries are accepted and Loot warns once at startup of the first
delivery:

```
level=WARN msg="github webhook has no secret set; anyone who can reach /hooks/github can inject drops"
```

To test the endpoint by hand, sign the body the way GitHub does:

```bash
BODY='{"action":"created","starred_at":"2026-08-18T12:00:00Z","repository":{"full_name":"you/yourapp"},"sender":{"login":"octocat"}}'
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "choose-a-long-random-string" | awk '{print $2}')"

curl -X POST http://localhost:8080/hooks/github \
  -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: star' \
  -H "X-Hub-Signature-256: $SIG" \
  -d "$BODY"
```

Send it twice. The dedupe key is `github:star:you/yourapp:octocat`, so the
second delivery returns `200` and creates nothing — the same reason a webhook
and a poll of the same star never produce two drops.

## Checking it

```
$ loot check
✓ github       2 repo(s)
```

`loot check` reads each repository's metadata, which exercises the token, the
network and the repo names in one call apiece:

| Message | Means |
|---|---|
| `repo not found or token lacks access` | typo in `owner/name`, or a private repo and a token without `repo` |
| `bad token (401)` | the token is wrong, revoked or expired |
| `rate limit exhausted, resets at …` | wait, or add a token |
| `"nickhirras" is not an owner/name repository` | a repo entry is missing its owner |
