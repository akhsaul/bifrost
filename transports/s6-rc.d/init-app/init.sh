#!/bin/sh
mkdir -p "${APP_DIR:-/app/data}/logs" "${MINIO_DIR:-/app/data/minio-data}" 2>/dev/null || true
