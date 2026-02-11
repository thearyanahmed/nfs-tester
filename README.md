# nfs-tester

Go REST API for testing NFS operations across multiple app instances. Demonstrates shared sessions, image gallery, and a full NFS test suite — all backed by a single NFS mount.

## User

- **UID**: 1000
- **GID**: 1000
- **User**: apps

## Architecture

- **app** (3 instances, port 8080) — main service with sessions, image gallery, shell, test suite
- **session-watcher** (1 instance, port 8081) — read-only session viewer at `/watcher/`

## Build

```bash
docker build -t nfs-tester .
docker build -f Dockerfile.session-watcher -t session-watcher .
```

## Run locally

```bash
docker run -p 8080:8080 -v /path/to/nfs:/mnt/nfs nfs-tester
```

## Endpoints

### Main app

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Main page (login, sessions, gallery, shell, test suite) |
| GET | `/health` | Health check |
| GET | `/api/v1/info` | System and mount info |
| GET | `/api/v1/matrix` | Run NFS test matrix |
| GET | `/api/v1/test-suite` | Full NFS test suite (isolated + shared) |
| GET | `/api/v1/exec?cmd=<cmd>&cwd=<path>` | Execute shell command |
| POST | `/api/v1/login` | Login (JSON: `{"username":"alice","password":"secret12"}`) |
| GET | `/api/v1/me` | Current session info |
| POST | `/api/v1/logout` | Logout |
| GET | `/api/v1/sessions` | List all sessions |
| POST | `/api/v1/images/upload` | Upload image (multipart form) |
| GET | `/api/v1/images` | List images |
| POST | `/api/v1/images/delete/<name>` | Delete image |

### Session watcher

| Method | Path | Description |
|--------|------|-------------|
| GET | `/watcher/` | Live session viewer (auto-refreshes every 5s) |
| GET | `/watcher/health` | Health check |
| GET | `/watcher/api/v1/digest` | Session file digests (JSON) |

## Demo users

All users share the password `secret12`:

alice, bob, zach, soulan, anish, bikram

## Deploy

```bash
# deploy (force build)
./deploy.sh

# deploy + mount NFS
./redeploy.sh
```
