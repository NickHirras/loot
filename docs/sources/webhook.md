# Generic webhook

The escape hatch. `POST /hooks/webhook` turns any JSON you can curl into a
drop, so the things that matter to your project but are not sold in an app
store — the first invoice, a green build, a customer saying yes — land in the
same feed as the sales.

```bash
curl -X POST http://localhost:8080/hooks/webhook \
  -H 'Content-Type: application/json' \
  -d '{"kind":"first_customer","rarity":"legendary","title":"First customer!","subtitle":"Someone paid us money"}'
```

```json
{"accepted":1,"ok":true}
```

## Configure

```yaml
sources:
  webhook:
    # Off by default: an open drop-minting endpoint is not something to mount
    # by accident.
    enabled: true
    # When set, callers must send `Authorization: Bearer <secret>`.
    secret: "choose-a-long-random-string"
```

Environment overrides: `LOOT_WEBHOOK_ENABLED`, `LOOT_WEBHOOK_SECRET`.

The secret is optional and you should set it anyway. Anyone who can reach the
port can otherwise write to your feed, your XP and — with `"ledger": true` —
your revenue. Both `Authorization: Bearer <secret>` and a bare
`Authorization: <secret>` are accepted, because half the tools that can POST
JSON cannot set a prefix.

```
$ loot check
✗ webhook      no secret set: anyone who can reach /hooks/webhook can inject drops
```

## Fields

Only `kind` is required.

| Field | Type | Default | What it does |
|---|---|---|---|
| `kind` | string | **required** | The event kind, free-form: `sale`, `signup`, `ci_green`. Rules match on it like any other source's kind. |
| `app` | string | `""` | What this happened to. Groups the feed and the vault. |
| `country` | string | `""` | ISO 3166-1 alpha-2, e.g. `DE`. Case-insensitive. Anything that is not two letters is quietly dropped — a stray `"Germany"` costs you a flag, not the drop. |
| `amount` | number | `0` | Money. Requires `currency`, or the request is a `400`. |
| `currency` | string | `""` | ISO 4217, e.g. `EUR`. Converted into your display currency on ingest. |
| `quantity` | number | `1` | Units. `0` is respected, not defaulted away. |
| `occurred_at` | RFC3339 | now | When it happened. |
| `id` | string | a hash | The dedupe identity, stored as `webhook:<id>`. See below. |
| `rarity` | string | — | One of `common`, `uncommon`, `rare`, `epic`, `legendary`, `cursed`. Anything else is a `400`. |
| `title` | string | `""` | The drop's headline. |
| `subtitle` | string | `""` | The line under it. |
| `ledger` | bool | `false` | `true` stores the event as settled money, so the **vault sums it into revenue**. Use it for real invoices; leave it false for estimates and signals. |
| `chest` | bool | `false` | `true` holds the drop for the day's chest instead of the live feed. |
| `payload` | object | `{}` | Anything else you want stored with the event. |

`rarity`, `title` and `subtitle` are copied to the top level of the stored
payload, because that is where the default rules look for them
(`{{.Payload.title}}`). Keys in `payload` are merged underneath, so a
`payload.title` never shadows the real one.

### Rarity

The default rules read `rarity` straight off the payload, so naming it is the
whole of the styling:

```yaml
- name: webhook-legendary
  match: {source: webhook, payload_match: {rarity: legendary}}
  then: {rarity: legendary, title: "{{.Payload.title}}", subtitle: "{{.Payload.subtitle}}", xp: 1000}
```

Omit `rarity` and the drop falls through to the kind-based and fallback rules
like any other source — which is what you want once you have written your own
rules for `kind: ci_green`.

### Dedupe and `id`

Every event in Loot is keyed. Send an `id` and the key is `webhook:<id>`, so a
retry, a re-run or a nervous double-click collapses into one drop:

```bash
# Both of these produce exactly one drop.
curl -X POST localhost:8080/hooks/webhook -d '{"kind":"sale","id":"invoice-1001"}'
curl -X POST localhost:8080/hooks/webhook -d '{"kind":"sale","id":"invoice-1001"}'
```

**Omit `id` and every post is a new drop.** The fallback key is a hash of the
body, the clock and a counter, so two identical bodies never collide. That is
deliberate: without an id there is no way to tell a retry from a second
identical sale, and silently swallowing the second sale would be the worse
mistake. If your sender retries, send an id.

## Batches

The body may also be an array, up to 500 drops. The whole batch is validated
before anything is emitted, so a typo in the fourth drop does not leave the
first three half-ingested:

```bash
curl -X POST http://localhost:8080/hooks/webhook \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer choose-a-long-random-string' \
  -d '[
    {"kind":"sale","app":"Sprocket","amount":4.99,"currency":"EUR","country":"DE","id":"inv-1001","ledger":true},
    {"kind":"sale","app":"Sprocket","amount":4.99,"currency":"EUR","country":"FR","id":"inv-1002","ledger":true},
    {"kind":"signup","app":"Sprocket","title":"Someone signed up"}
  ]'
```

```json
{"accepted":3,"ok":true}
```

Duplicates are not counted in the response — `accepted` is how many events were
handed to the pipeline, and the pipeline decides which of them are new.

## Errors

A rejected request is a `400` with the reason, and emits nothing:

```json
{"error":"drop 2: kind is required","ok":false}
{"error":"rarity \"mythic\" is not one of common, uncommon, rare, epic, legendary, cursed","ok":false}
{"error":"amount requires currency","ok":false}
{"error":"occurred_at \"yesterday\" is not an RFC3339 timestamp","ok":false}
```

A missing or wrong secret is a `401`. Anything other than POST is a `405`.

## Worked examples

**A legendary first customer.** The one you will want to have set up before it
happens:

```bash
curl -X POST http://localhost:8080/hooks/webhook \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer choose-a-long-random-string' \
  -d '{
    "kind":"first_customer",
    "app":"Sprocket",
    "rarity":"legendary",
    "title":"First customer!",
    "subtitle":"Someone paid real money for a thing you made",
    "country":"DE",
    "amount":29.00,
    "currency":"EUR",
    "id":"first-customer-ever"
  }'
```

**A ledger sale**, so the vault counts it as revenue rather than as a signal:

```bash
curl -X POST http://localhost:8080/hooks/webhook \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer choose-a-long-random-string' \
  -d '{
    "kind":"sale",
    "app":"Sprocket",
    "amount":49.00,
    "currency":"USD",
    "country":"US",
    "quantity":1,
    "occurred_at":"2026-08-18T12:00:00Z",
    "id":"stripe-ch_3PabcXYZ",
    "ledger":true,
    "rarity":"rare",
    "title":"Licence sold",
    "payload":{"plan":"team","seats":5}
  }'
```

Use the payment processor's own charge id as `id`. It is stable across
retries, which is exactly what dedupe wants.

**A green build**, kept out of the live feed and saved for the daily chest:

```bash
curl -X POST http://localhost:8080/hooks/webhook \
  -H 'Authorization: Bearer choose-a-long-random-string' \
  -d '{"kind":"ci_green","app":"Sprocket","title":"Build passed","subtitle":"main @ '"$GITHUB_SHA"'","chest":true,"id":"ci-'"$GITHUB_RUN_ID"'"}'
```

## Ideas

- **CI**: a last step in your workflow that posts `ci_green` on success and a
  `cursed` drop on failure. A red build that makes a noise is a build that gets
  fixed.
- **Stripe or Paddle**: their webhooks have their own shapes and their own
  signatures, so put a ten-line relay in front — verify their signature, pull
  out the amount and the charge id, POST the four fields Loot wants. Use the
  charge id as `id` and `"ledger": true`.
- **A newsletter or waitlist**: `kind: signup`, `rarity: uncommon`. Volume is
  the point; a rule with `min_quantity` can promote a busy day.
- **Uptime**: `rarity: cursed` when the monitor goes red, `rare` when it comes
  back. The feed becomes an ambient status board.
- **The unquantifiable**: a shell alias that posts a `legendary` drop when
  someone emails to say your thing helped them. It is the highest-value event
  your project produces and nothing else will ever measure it.
