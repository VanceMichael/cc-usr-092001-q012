#!/bin/sh
set -eu
mkdir -p "${DATABASE_PATH:-./data}"
printf '%s
' '当前默认使用进程内并发安全存储，无需预置表结构；data/ 预留给持久化后端的本地文件。'
