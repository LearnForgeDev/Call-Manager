# AGENTS.md

Instructions for coding agents that need to run this repository locally.

## Project Purpose

This repo runs a local, production-like Jitsi Meet stack with:

- Jitsi Meet web, Prosody, Jicofo, and JVB from `docker-compose.yml`.
- Persistent Excalidraw whiteboard storage from `docker-compose.override.yml`.
- Optional Coturn TURN service from the `coturn` configuration.
- JWT authentication for moderator access.

## Local Prerequisites

- Docker Desktop must be installed and running.
- On Windows, Docker Desktop should use the WSL 2 based engine.
- On Windows, use Git Bash for `./gen-passwords.sh`; PowerShell and Command Prompt are not reliable for that script.
- Use Docker Compose v2 syntax: `docker compose ...`.

## Environment Setup

Do not overwrite an existing `.env` file. It may contain generated service passwords or local secrets.

If `.env` is missing, create it from the example and generate Jitsi internal passwords:

```bash
cp env.example .env
./gen-passwords.sh
```

For local development, `.env` must include or be updated to include these values:

```dotenv
PUBLIC_URL=https://localhost:8443

ENABLE_WHITEBOARD=1
WHITEBOARD_COLLAB_SERVER_PUBLIC_URL=http://localhost:8080
EXCALIDRAW_DB_USER=excalidraw_user
EXCALIDRAW_DB_PASS=local_dev_password_123

TURN_HOST=learnforge.com
TURN_PORT=3478
TURNS_PORT=5349
TURN_TRANSPORT=tcp
TURN_CREDENTIALS=your_super_secure_auth_secret_here

ENABLE_AUTH=1
AUTH_TYPE=jwt
JWT_APP_ID=learnforge_local_dev
JWT_APP_SECRET=local_dev_jwt_secret_999
```

Default local ports from `env.example` are:

- Jitsi HTTP: `http://localhost:8000`
- Jitsi HTTPS: `https://localhost:8443`
- Excalidraw backend: `http://localhost:8080`
- JVB UDP media: `10000/udp`

## Start The Stack

From the repository root:

```bash
docker compose up -d coturn
docker compose up -d
```

Wait roughly 60-90 seconds after startup so PostgreSQL, Prosody, Jicofo, JVB, and the web container can initialize and handshake.

Check status:

```bash
docker compose ps
```

Useful logs:

```bash
docker compose logs -f web
docker compose logs -f prosody
docker compose logs -f jicofo
docker compose logs -f jvb
docker compose logs -f excalidraw-backend
docker compose logs -f excalidraw-db
docker compose logs -f coturn
```

## Access Locally

Because JWT auth is enabled, opening `https://localhost:8443` without a token is not enough for moderator access.

Generate a local HS256 JWT using `JWT_APP_SECRET` from `.env`. With the README defaults, the secret is:

```text
local_dev_jwt_secret_999
```

Use this payload:

```json
{
  "aud": "learnforge_local_dev",
  "iss": "learnforge_local_dev",
  "sub": "meet.jitsi",
  "room": "*",
  "context": {
    "user": {
      "name": "Local Developer",
      "email": "dev@learnforge.com",
      "moderator": "true"
    },
    "features": {
      "screen-sharing": "true"
    }
  }
}
```

Join a room with:

```text
https://localhost:8443/MyTestRoom?jwt=<PASTE_TOKEN_HERE>
```

The browser may warn about the local HTTPS certificate. Accept the local development certificate warning if needed.

## Stop The Stack

Stop containers while preserving Docker volumes:

```bash
docker compose down
```

This keeps the Excalidraw PostgreSQL volume, so whiteboard data can persist across restarts.

To remove volumes, only do so when explicitly requested:

```bash
docker compose down -v
```

## Agent Safety Notes

- Do not run `docker compose down -v` unless the user explicitly asks to delete persisted local data.
- Do not regenerate `.env` passwords if `.env` already exists unless the user explicitly asks.
- If ports `8000`, `8443`, `8080`, or `10000/udp` are already in use, report the conflict and update `.env` only with user approval.
- Prefer checking `docker compose ps` and targeted service logs before changing configuration.
