package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	simulator "github.com/kevynsax/cpamc-simulator"
)

// version is stamped at release: -ldflags "-X main.version=0.1.2". Dev builds stay "dev".
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8317"
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid listen address")
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:" + port + "/ready")
		if err != nil {
			return fmt.Errorf("simulator not ready")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("simulator not ready")
		}
		return nil
	}
	handler, err := simulator.New(simulator.Config{ClientKey: os.Getenv("CLIENT_API_KEY"), ManagementKey: os.Getenv("MANAGEMENT_API_KEY"), Scenario: os.Getenv("SIMULATOR_SCENARIO")})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("cpamc simulator %s listening on %s; no upstream calls, no real AI", version, addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
