#!/bin/sh
# Скорость, с которой тензоры идут через платформу: счётчики eth0 до и после.
a=$(grep eth0 /proc/net/dev | awk '{print $2}')
b0=$(grep eth0 /proc/net/dev | awk '{print $10}')
sleep 20
b=$(grep eth0 /proc/net/dev | awk '{print $2}')
b1=$(grep eth0 /proc/net/dev | awk '{print $10}')
echo "rx за 20 с: $(( (b-a)/1024 )) KiB, $(( (b-a)/1024/20 )) KiB/s"
echo "tx за 20 с: $(( (b1-b0)/1024 )) KiB, $(( (b1-b0)/1024/20 )) KiB/s"
