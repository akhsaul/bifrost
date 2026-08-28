#!/bin/sh
set -e

if [ -z "$MINIO_ROOT_USER" ] || [ -z "$MINIO_ROOT_PASSWORD" ]; then
    echo "ERROR: MINIO_ROOT_USER and MINIO_ROOT_PASSWORD environment variables are required!"
    exit 1
fi

while ! wget -q -O /dev/null "http://127.0.0.1:${MINIO_PORT:-9000}/minio/health/ready"; do
    echo "MinIO client is waiting for MinIO API to be ready..."
    sleep 1
done

echo "MinIO is ready"

/usr/local/bin/mc alias set local "http://127.0.0.1:${MINIO_PORT:-9000}" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
/usr/local/bin/mc mb "local/${MINIO_BUCKET:-bifrost-logs}" --ignore-existing || true
