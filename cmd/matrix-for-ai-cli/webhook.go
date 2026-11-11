package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"
)

const webhookSubcommands = "create, delete or list"

func cmdWebhook(configPath string, args []string) error {
	if len(args) == 0 {
		return usageError("webhook", "missing subcommand: %s", webhookSubcommands)
	}
	switch args[0] {
	case "create":
		return cmdWebhookCreate(configPath, args[1:])
	case "delete":
		return cmdWebhookDelete(configPath, args[1:])
	case "list":
		return cmdWebhookList(configPath, args[1:])
	}
	return usageError("webhook", "unknown subcommand %q: %s", args[0], webhookSubcommands)
}

func cmdWebhookCreate(configPath string, args []string) error {
	fs := flag.NewFlagSet("webhook create", flag.ExitOnError)
	tokenFile := fs.String("token-file", "", "")
	as := asFlag(fs)
	webhookURL := fs.String("url", "", "")
	parseUntil(fs, args, 1) // from CMD on, the flags are the handler's
	handler := fs.Args()
	if len(handler) == 0 {
		return usageError(fs.Name(), "missing CMD")
	}
	if *tokenFile == "" && *as == "" {
		return usageError(fs.Name(), "missing --as or --token-file")
	}
	var err error
	if handler[0], err = filepath.Abs(handler[0]); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	tf := *tokenFile
	if tf == "" {
		e, ok := cfg.Entities[*as]
		if !ok {
			return notHere(cfg, *as)
		}
		tf = e.TokenFile
	}
	if tf, err = filepath.Abs(tf); err != nil {
		return err
	}
	if *webhookURL != "" {
		cfg.URL = *webhookURL
	}
	if cfg.URL == "" {
		return usageError(fs.Name(), "missing --url: it is needed the first time")
	}
	e := &Entity{TokenFile: tf, OnNotice: handler}
	base, token, mxid, err := appserviceCredentials(e)
	if err != nil {
		return err
	}
	if mxid == "" {
		if mxid, err = whoAmI(base, token); err != nil {
			return err
		}
	}
	if e.Secret, err = putWebhook(base, token, cfg.URL); err != nil {
		return err
	}
	cfg.Entities[mxid] = e
	if err := saveConfig(configPath, cfg); err != nil {
		return fail(errPartial, "the appservice has the webhook but the secret could not be saved (run webhook create again): %w", err)
	}
	wake(configPath)
	fmt.Printf("%s webhook → %s\n", mxid, cfg.URL)
	return nil
}

func cmdWebhookDelete(configPath string, args []string) error {
	fs := flag.NewFlagSet("webhook delete", flag.ExitOnError)
	parse(fs, args)
	if err := need(fs, "MXID"); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	e, ok := cfg.Entities[fs.Arg(0)]
	if !ok {
		return notHere(cfg, fs.Arg(0))
	}
	base, token, _, err := appserviceCredentials(e)
	if err != nil {
		return err
	}
	appserviceErr := deleteWebhook(base, token)
	e.Secret, e.OnNotice = "", nil
	if err := saveConfig(configPath, cfg); err != nil {
		return err
	}
	if appserviceErr != nil {
		return fail(errPartial, "removed here, but the appservice may still send notices (it is ignored here): %w", appserviceErr)
	}
	return nil
}

func cmdWebhookList(configPath string, args []string) error {
	fs := flag.NewFlagSet("webhook list", flag.ExitOnError)
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	fmt.Printf("url: %s\n", cfg.URL)
	names := make([]string, 0, len(cfg.Entities))
	for n := range cfg.Entities {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := cfg.Entities[n]
		if !e.hasWebhook() {
			fmt.Printf("%s\ttoken: %s\tno webhook\n", n, e.TokenFile)
			continue
		}
		fmt.Printf("%s\ttoken: %s\ton_notice: %v\n", n, e.TokenFile, e.OnNotice)
	}
	return nil
}
