#!/bin/bash

# FUSE NFS mount script
# Set these env vars:
#   FUSE_NFS_SERVER - NFS server IP (public IP of droplet)
#   FUSE_NFS_EXPORT - NFS export path
#   FUSE_NFS_UID - UID for NFS credentials (default: 998)
#   FUSE_NFS_GID - GID for NFS credentials (default: 678)

FUSE_NFS_SERVER=${FUSE_NFS_SERVER:-}
FUSE_NFS_EXPORT=${FUSE_NFS_EXPORT:-/data/test}
FUSE_NFS_UID=${FUSE_NFS_UID:-998}
FUSE_NFS_GID=${FUSE_NFS_GID:-678}
MOUNT_POINT=${NFS_PATH:-/mnt/nfs}

if [ -z "$FUSE_NFS_SERVER" ]; then
    echo "FUSE_NFS_SERVER not set, skipping FUSE mount"
    exit 0
fi

echo "=== FUSE NFS Mount ==="
echo "Server: $FUSE_NFS_SERVER"
echo "Export: $FUSE_NFS_EXPORT"
echo "UID/GID: $FUSE_NFS_UID/$FUSE_NFS_GID"
echo "Mount: $MOUNT_POINT"

mkdir -p "$MOUNT_POINT"

# check connectivity
echo ""
echo "Testing connectivity to $FUSE_NFS_SERVER:2049..."
if nc -z -w5 "$FUSE_NFS_SERVER" 2049; then
    echo "NFS server reachable"
else
    echo "ERROR: Cannot reach NFS server"
    exit 1
fi

# mount via fuse-nfs
echo ""
echo "Mounting via fuse-nfs..."
fuse-nfs -n "nfs://$FUSE_NFS_SERVER$FUSE_NFS_EXPORT" \
    -m "$MOUNT_POINT" \
    -U "$FUSE_NFS_UID" \
    -G "$FUSE_NFS_GID" \
    -a &

sleep 3

# verify
if mount | grep -q "fuse"; then
    echo "FUSE mount successful"
    mount | grep fuse
    ls -la "$MOUNT_POINT"
else
    echo "WARNING: FUSE mount may have failed"
fi

echo ""
