# alert-bot

Telegram alert bot for our work team. Replaces a Python cron script that re-sends the same
ArgoCD/Gravitee errors every 5 minutes. Goal: notify once when an issue appears, once when it resolves.

## How to work (the owner and Claude)
- Claude implements the project task by task. The owner reviews each task before the next one starts.
- Keep each task small and reviewable: code, tests, and a short summary of what to look at. Do not commit or push; the owner reviews the diff and commits.
- The owner is new to Go. In summaries, explain any non-obvious Go idiom in a line.
- Be concrete and pragmatic. No generic explanations.
- Never put real tokens, passwords or emails in files, logs or chat. Config comes from environment variables. Anything that prints config must go through the masking in internal/config.
- Verification: Claude's sandbox has Go but cannot download third-party modules and has no PostgreSQL server. Everything except two files is stdlib-only and is built and tested there with `go vet -tags nopgx,noldap ./... && go test -tags nopgx,noldap ./...`. The two files that import third-party modules (cmd/alertbot/driver_pgx.go, internal/directory/ldapdir) are written but not compiled there: the owner runs `go mod tidy` once, then `go build ./...`.
- The Postgres store is checked by the same contract suite as the Memory store: create a database ending in _test, set TEST_DATABASE_URL, run `go test -tags integration ./internal/store/...`.
- Commits: one commit per task (or logical step), author is the owner, never push.

## Stack (decided)
Go, single binary. PostgreSQL via pgx (driver registered only in cmd/alertbot behind build tag !nopgx, store code uses database/sql), plain SQL, own tiny migration runner (embedded NNNN_name.sql files, no goose). go-ldap for Active Directory (isolated in internal/directory/ldapdir, build tag !noldap).
Telegram: own thin Bot API client on net/http (no library). SMTP via net/smtp with STARTTLS. Layout: cmd/alertbot, internal/{config,store,poller,engine,notify,bot,auth,web}.
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
- States: unverified -> pending -> active <-> disabled. Offboarding is admin-only: disable is reversible, delete really removes the user, their tags and OTP history (audit rows stay, they hold ids only). Daily AD sync is a possible later extra.
- Roles/titles hierarchy (tree) and permissions (e.g. broadcast:all, approve:users, disable:users) live in our DB. Audit log for approvals, disables, deletes, broadcasts.

## Legacy / references
- Legacy Python poller: ArgoCD apps with health Degraded/Missing; Gravitee platform logs 4xx/5xx for the last 5 min, grouped by (path, method, status); mention maps by keyword -> hashtags.
- Earlier C# bots on the work laptop (SimOP-V2: AD + email OTP + admin approval; JobFlexify: button subscriptions + queue fan-out). Learn from them, do not copy their weight.

## Progress
- [x] 0 Go module, hello main, .gitignore, docker-compose
- [x] 1 Config from env (+ .env loader, secret masking)
- [x] 2 Store (Memory + Postgres, contract tests), migrations, roles/permissions (internal/access)
- [ ] 3 Alert engine (pure function + tests)
- [ ] 4 ArgoCD poller
- [ ] 5 Telegram sender
- [ ] 6 Gravitee poller
- [ ] 7 Users + OTP + AD lookup
- [ ] 8 Roles and permissions
- [ ] 9 Bot commands + subscription buttons
- [ ] 10 Broadcasts
- [ ] 11 Simple UI
