package services

// 握手 401 自愈路径的退避:凭据刷新在途/持续失败时,固定 2s 重拨会高频
// 打 auth 端点并刷屏日志。改为指数退避(2s→60s 封顶),连接成功即归零。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHandshakeRetryDelay(t *testing.T) {
	assert.Equal(t, 2*time.Second, handshakeRetryDelay(1))
	assert.Equal(t, 4*time.Second, handshakeRetryDelay(2))
	assert.Equal(t, 8*time.Second, handshakeRetryDelay(3))
	assert.Equal(t, 16*time.Second, handshakeRetryDelay(4))
	assert.Equal(t, 32*time.Second, handshakeRetryDelay(5))
	assert.Equal(t, 60*time.Second, handshakeRetryDelay(6), "封顶 60s")
	assert.Equal(t, 60*time.Second, handshakeRetryDelay(50), "超大连击仍封顶")
	assert.Equal(t, 2*time.Second, handshakeRetryDelay(0), "归零后回到起步值")
}
