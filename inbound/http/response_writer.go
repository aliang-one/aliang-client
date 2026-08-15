package http

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
)

type CustomResponseWriter struct {
	conn    net.Conn
	writer  *bufio.Writer
	headers http.Header
	status  int
	written bool
	body    bytes.Buffer // 缓冲 body
}

func NewCustomResponseWriter(conn net.Conn) *CustomResponseWriter {
	return &CustomResponseWriter{
		conn:    conn,
		writer:  bufio.NewWriter(conn),
		headers: make(http.Header),
		status:  0, // 不预设状态
		written: false,
	}
}

func (w *CustomResponseWriter) Header() http.Header {
	return w.headers
}

func (w *CustomResponseWriter) WriteHeader(statusCode int) {
	if w.written {
		return
	}
	w.status = statusCode
	w.written = true

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("HTTP/1.1 %d %s\r\n", statusCode, http.StatusText(statusCode)))
	for k, vs := range w.headers {
		for _, v := range vs {
			buf.WriteString(k)
			buf.WriteString(": ")
			buf.WriteString(v)
			buf.WriteString("\r\n")
		}
	}
	if _, ok := w.headers["Content-Length"]; !ok {
		buf.WriteString(fmt.Sprintf("Content-Length: %d\r\n", w.body.Len()))
	}
	buf.WriteString("\r\n")

	w.writer.Write(buf.Bytes())
	if w.body.Len() > 0 {
		w.writer.Write(w.body.Bytes())
	}
	w.writer.Flush()
}

func (w *CustomResponseWriter) Write(data []byte) (int, error) {
	if w.written {
		return w.writer.Write(data) // 已写入头部，直接写数据
	}
	// 未写入头部，缓冲数据，等待显式状态码
	return w.body.Write(data)
}

func (w *CustomResponseWriter) Flush() {
	if !w.written && w.status != 0 {
		w.WriteHeader(w.status) // 使用已设置的状态码
	} else if !w.written {
		w.WriteHeader(http.StatusOK) // 仅在未设置状态时默认 200
	}
	w.writer.Flush()
}
