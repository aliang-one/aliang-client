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
			log.Fatal(err)
		}
	}()
}
