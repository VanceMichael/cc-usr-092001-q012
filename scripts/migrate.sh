#!/bin/sh
set -eu
: "${DATABASE_PATH:=./data}"
mkdir -p "$DATABASE_PATH"
printf '%s\n' "数据目录已就绪: $DATABASE_PATH（结构由服务首次写入时自动建立）"
