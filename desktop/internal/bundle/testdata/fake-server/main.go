// Fake niuniu-server for bundle tests. Flags mirror the real one.
// Compiled at test time via go build.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func main() {
	var embedded bool
	var addr string
	var crashEarly bool
	var ignoreSIGTERM bool
	flag.BoolVar(&embedded, "embedded", false, "")
	flag.StringVar(&addr, "addr", "127.0.0.1:0", "")
	flag.BoolVar(&crashEarly, "crash-early", false, "")
	flag.BoolVar(&ignoreSIGTERM, "ignore-sigterm", false, "")
	flag.Parse()

	if crashEarly {
		os.Exit(3)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if embedded {
		fmt.Fprintf(os.Stdout, `{"event":"ready","addr":"127.0.0.1:%d"}`+"\n", port)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"port":` + strconv.Itoa(port) + `}`))
	})
	srv := &http.Server{Handler: mux}

	go func() { _ = srv.Serve(ln) }()

	stop := make(chan os.Signal, 1)
	if !ignoreSIGTERM {
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	}
	parentGone := make(chan struct{})
	if embedded {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			close(parentGone)
		}()
	}
	select {
	case <-stop:
	case <-parentGone:
	}
	_ = srv.Close()
}
