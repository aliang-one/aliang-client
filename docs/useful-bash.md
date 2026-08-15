检测mtls的延迟
```
# 单次测试（适用于 mTLS 服务）
curl -v -k --cert processor/cert/client/client.crt --key processor/cert/client/client.key \
  -w "TCP 建连: %{time_connect}s\nTLS 握手: %{time_appconnect}s\n首字节: %{time_starttransfer}s\n总耗时: %{time_total}s\n" \
  -o /dev/null -s \
  https://15.nat0.cn:16749
```