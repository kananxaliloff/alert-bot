# alert-bot

Telegram alert bot for our work team. Replaces a Python cron script that re-sends the same
ArgoCD/Gravitee errors every 5 minutes. Goal: notify once when an issue appears, once when it resolves.

## How to work (the owner and Claude)
- Claude implements the project task by task. The owner reviews each task before the next one starts.
- Keep each task small and reviewable: code, tests, and a short summary of what to look at. Do not commit or push; the owner reviews the diff and commits.
- The owner is new to Go. In summaries, explain any non-obvious Go idiom in a line.
- Be concrete and pragmatic. No generic explanations.
- Never put real tokens, passwords or emails in files, logs or chat. Config comes from environment variables. Anything that prints config must go through the masking in internal/config.
- Verification: Claude's sandbox has Go but cannot download third-party modules. Standard-library code is built and tested there. Code that needs third-party modules (pgx, goose, Telegram, LDAP) is written but the owner runs `go mod tidy`, `go build ./...` and `go test ./...` locally and reports errors. Prefer designs where logic lives in stdlib-only packages behind interfaces.

## Stack (decided)
Go, single binary. PostgreSQL via pgx (registered only in cmd/alertbot, store code uses database/sql), plain SQL, goose migrations. go-ldap for Active Directory.
Telegram library TBD (go-telegram/bot or telebot). SMTP with STARTTLS. Layout: cmd/alertbot, internal/{config,store,poller,engine,notify,bot,auth,web}.
Local dev: `docker compose up -d` (postgres:16-alpine). Config via env or a git-ignored .env (see .env.example).

## Core design
- Pollers only report "what is broken now" (list of alert keys). The engine diffs it against the DB and emits transitions.
- Alert key = source|kind|identity, e.g. argocd|app|<name>, gravitee|<METHOD path>|<status>.
- New key -> firing, notify once. Still present -> silent (update count/last_seen, ideally edit the Telegram message). Gone -> resolved after N clean polls (ArgoCD 1, Gravitee 2-3), notify once. No resolution = stays firing; optional reminder every X hours, never every 5 min.
- started_at/resolved_at are approximate (poll interval); store the interval.
- Tags: severity, service, team (be/fe), module (booking, trade-service, trade-guide, e-cmr, devops). Rules live in config, not code.
- Delivery: notifications outbox table + in-process rate-limited worker. 403 = user blocked bot (mark, stop). 429 = retry after retry_after.
- Subscriptions: stateless inline-button toggles (callback data like t:booking, <=64 bytes), saved on each press, menu re-rendered from DB. Also min severity.
- Commands: /start, /alerts, /subscribe, /mysubs, /broadcast (be|fe|all|booking|trade-service|trade-guide|e-cmr|devops, permission-checked).

## Users and access
- Sign-up: /start -> work email -> AD lookup (exists + enabled, indexed LDAP filter on mail) -> OTP by email -> status pending.
- OTP: 6 digits from crypto/rand, store hash only, 10 min TTL, 5 attempts, resend cooldown, rate limits per email and per chat, stored in DB, never logged.
- Admin approves pending users and assigns role + tags with buttons; then active. Admins see name/title from AD.
- States: unverified -> pending -> active -> disabled -> deleted. Offboarding is admin-only (disable, later delete = purge personal data after N days). Daily AD sync is a possible later extra.
- Roles/titles hierarchy (tree) and permissions (e.g. broadcast:all, approve:users, disable:users) live in our DB. Audit log for approvals, disables, deletes, broadcasts.

## Legacy / references
- Legacy Python poller: ArgoCD apps with health Degraded/Missing; Gravitee platform logs 4xx/5xx for the last 5 min, grouped by (path, method, status); mention maps by keyword -> hashtags.
- Earlier C# bots on the work laptop (SimOP-V2: AD + email OTP + admin approval; JobFlexify: button subscriptions + queue fan-out). Learn from them, do not copy their weight.

## Progress
- [x] 0 Go module, hello main, .gitignore, docker-compose
- [x] 1 Config from env (+ .env loader, secret masking)
- [ ] 2 Postgres store + migrations
- [ ] 3 Alert engine (pure function + tests)
- [ ] 4 ArgoCD poller
- [ ] 5 Telegram sender
- [ ] 6 Gravitee poller
- [ ] 7 Users + OTP + AD lookup
- [ ] 8 Roles and permissions
- [ ] 9 Bot commands + subscription buttons
- [ ] 10 Broadcasts
- [ ] 11 Simple UI
