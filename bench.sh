#!/bin/bash

ssh isu1 'cd /home/isucon && ./bench_linux_amd64 run --target https://pipe.u.isucon.dev --nameserver 127.0.0.1 --webapp 52.198.221.103 --enable-ssl'
ssh isu1 'cat /tmp/result.json'
