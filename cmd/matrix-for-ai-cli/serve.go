package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func cmdServe(configPath, state string, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	idle := fs.Duration("idle", 15*time.Minute, "")
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}

	ln, err := activatedListener()
	if err != nil {
		return err
	}
	activated := ln != nil
	if !activated {
		cfg, err := loadConfig(configPath)
		if err != nil {
			return err
		}
		if ln, err = net.Listen("tcp", listenAddr(cfg)); err != nil {
			return err
		}
	}
	s := newServer(func() (*Config, error) { return loadConfig(configPath) }, state)
	logOtherAPIVersion := otherAPIVersion
	otherAPIVersion = func(major string) {
		logOtherAPIVersion(major)
		s.alert("matrix-for-ai-cli", fmt.Sprintf("The appservice speaks API v%s and this client v%d: update whichever is behind.", major, appserviceAPIVersion))
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	log.Printf("listening on %s", ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		// Late, so the notice that started us is recorded before the catch-up.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		s.sweep()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.sweep()
				if activated && *idle > 0 && s.idle() >= *idle && !s.retrying() {
					log.Printf("no notices for %s and no retries due, exiting (systemd keeps the socket)", *idle)
					stop()
					return
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func listenAddr(cfg *Config) string {
	if cfg.Listen != "" {
		return cfg.Listen
	}
	return defaultListen
}
