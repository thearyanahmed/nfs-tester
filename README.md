# nfs-tester

Simple Go REST API for testing NFS operations.

## User

- **UID**: 998
- **GID**: 678
- **User**: nfstest

## Build

```bash
docker build -t nfs-tester .
```

## Run locally

```bash
docker run -p 8080:8080 -v /path/to/nfs:/mnt/nfs nfs-tester
```

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Service info |
| GET | `/health` | Health check |
| GET | `/api/v1/info` | System and mount info |
| GET | `/api/v1/matrix` | Run full NFS test matrix |
| GET | `/api/v1/exec?cmd=<cmd>&cwd=<path>` | Execute shell command |

## Test Matrix

The `/api/v1/matrix` endpoint runs these tests:

1. create_file
2. read_file
3. append_file
4. overwrite_file
5. mkdir
6. create_in_subdir
7. chmod
8. rename
9. copy
10. delete_file
11. rmdir
12. large_file_1mb
13. concurrent_writes

## NFS Export Config

For this app to work with NFS, configure the export with matching UID:

```
/data/export *(rw,sync,all_squash,anonuid=998,anongid=678,no_subtree_check,insecure)
```
