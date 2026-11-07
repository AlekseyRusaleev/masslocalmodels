#!/usr/bin/env bash
for url in https://mirrors.aliyun.com/pytorch-wheels/cu128/ https://mirror.sjtu.edu.cn/pytorch-wheels/cu128/ https://download.pytorch.org/whl/cu128/ https://pypi.tuna.tsinghua.edu.cn/simple/; do
  code=$(timeout 20 curl -s -o /dev/null -w '%{http_code} %{time_total}s' "$url")
  echo "$url -> $code"
done