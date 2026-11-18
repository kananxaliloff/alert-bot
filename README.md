# alert-bot

Telegram alert bot that notifies once when an issue appears and once when it resolves.
See CLAUDE.md for the design and the task list.

## Run locally (Windows PowerShell or any shell)

```
docker compose up -d                 # local PostgreSQL
copy .env.example .env               # then put your bot token into .env
go test ./...
go run ./cmd/alertbot
```

Configuration is read from environment variables (a `.env` file is loaded if present;
real environment variables win). See `.env.example`.
