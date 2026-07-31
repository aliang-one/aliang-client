//go:build !prod

package debug

import (
	"log"
	"net/http"
	_ "net/http/pprof" // 自动注册路由
)

func init() {
	go func() {
		log.Println("pprof listening on http://localhost:6060/debug/pprof/")
		if err := http.ListenAndServe("localhost:6060", nil); err != nil {
			// Log instead of log.Fatal: a busy :6060 (or another listener) must
			// not kill the whole process — that would also take down the app under
			// diagnosis and defeat the profiling session.
			log.Printf("pprof server stopped: %v", err)
		}
	}()
}
