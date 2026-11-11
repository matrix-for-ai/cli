package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const identitySubcommands = "create, login, token, revoke or list"

func cmdIdentity(configPath string, args []string) error {
	if len(args) == 0 {
		return usageError("identity", "missing subcommand: %s", identitySubcommands)
	}
	switch args[0] {
	case "create":
		return cmdIdentityCreate(configPath, args[1:])
	case "login":
		return cmdIdentityLogin(configPath, args[1:])
	case "token":
		return cmdIdentityToken(configPath, args[1:])
	case "revoke":
		return cmdIdentityRevoke(configPath, args[1:])
	case "list":
		return cmdIdentityList(configPath, args[1:])
	}
	return usageError("identity", "unknown subcommand %q: %s", args[0], identitySubcommands)
}

type issuedToken struct {
	MXID     string `json:"mxid"`
	APIToken string `json:"api_token"`
}

func cmdIdentityCreate(configPath string, args []string) error {
	fs := flag.NewFlagSet("identity create", flag.ExitOnError)
	base := fs.String("base", "", "")
	var output string
	fs.StringVar(&output, "output", "", "")
	fs.StringVar(&output, "o", "", "")
	displayName := fs.String("display-name", "", "")
	insecure := fs.Bool("insecure", false, "")
	parse(fs, args)
	if *base == "" {
		return usageError(fs.Name(), "missing --base")
	}
	if err := need(fs, "NAME"); err != nil {
		return err
	}
	if _, err := os.Stat(output); output != "" && err == nil {
		return fail(errTokenFile, "%s already exists: it may hold a token that can't be recovered", output)
	}
	req := map[string]string{"name": fs.Arg(0)}
	if *displayName != "" {
		req["display_name"] = *displayName
	}
	body, _ := json.Marshal(req)
	b := strings.TrimRight(*base, "/")
	if err := checkAppserviceBase(fs.Name(), b, *insecure); err != nil {
		return err
	}
	var out issuedToken
	// Sent when set: where signup is closed, only the admin creates identities.
	if err := appserviceCall(http.MethodPost, b+"/identities", os.Getenv("MATRIX_AI_ADMIN_TOKEN"), body, &out); err != nil {
		return err
	}
	if out.MXID == "" || out.APIToken == "" {
		return fail(errAppserviceAnswer, "the appservice returned no mxid or token")
	}
	write := writeNew
	if output == "" {
		output, write = tokenPath(configPath, out.MXID), writeReplace
	}
	env := tokenFileContent(b, out.APIToken, out.MXID)
	if err := write(output, env); err != nil {
		fmt.Fprint(os.Stderr, "could not write the token file; save this now, it is not shown again:\n"+env)
		return err
	}
	if err := addEntity(configPath, out.MXID, output); err != nil {
		return fail(errPartial, "token file written, but not set up here (identity login --token-file %s): %w", output, err)
	}
	return printIdentity(out.MXID, output)
}

func cmdIdentityLogin(configPath string, args []string) error {
	fs := flag.NewFlagSet("identity login", flag.ExitOnError)
	tokenFile := fs.String("token-file", "", "")
	base := fs.String("base", "", "")
	var output string
	fs.StringVar(&output, "output", "", "")
	fs.StringVar(&output, "o", "", "")
	insecure := fs.Bool("insecure", false, "")
	force := fs.Bool("force", false, "")
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	fromStdin := *base != "" || output != ""
	switch {
	case *tokenFile != "" && fromStdin:
		return usageError(fs.Name(), "--token-file goes alone; --base and --output are for a token on stdin")
	case *tokenFile == "" && !fromStdin:
		return usageError(fs.Name(), "missing --token-file, or --base with the token on stdin")
	case fromStdin && *base == "":
		return usageError(fs.Name(), "a token on stdin needs --base")
	}
	if fromStdin {
		if output != "" {
			if _, err := os.Stat(output); err == nil {
				return fail(errTokenFile, "%s already exists: log in with --token-file", output)
			}
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return err
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return usageError(fs.Name(), "no token on stdin")
		}
		b := strings.TrimRight(*base, "/")
		if err := checkAppserviceBase(fs.Name(), b, *insecure); err != nil {
			return err
		}
		mxid, err := whoAmI(b, token)
		if err != nil {
			return err
		}
		if err := checkMove(configPath, fs.Name(), mxid, b, *force); err != nil {
			return err
		}
		write := writeNew
		if output == "" {
			output, write = tokenPath(configPath, mxid), writeReplace
		}
		if err := write(output, tokenFileContent(b, token, mxid)); err != nil {
			return err
		}
		return setUp(configPath, mxid, output)
	}
	b, token, loggedIn, err := appserviceCredentials(&Entity{TokenFile: *tokenFile})
	if err != nil {
		return err
	}
	if loggedIn != "" {
		if err := checkMove(configPath, fs.Name(), loggedIn, b, *force); err != nil {
			return err
		}
	}
	if err := checkAppserviceBase(fs.Name(), b, *insecure); err != nil {
		return err
	}
	if loggedIn == "" {
		if loggedIn, err = whoAmI(b, token); err != nil {
			return err
		}
		if err := checkMove(configPath, fs.Name(), loggedIn, b, *force); err != nil {
			return err
		}
	}
	return setUp(configPath, loggedIn, *tokenFile)
}

func whoAmI(base, token string) (string, error) {
	var me struct {
		MXID string `json:"mxid"`
	}
	err := appserviceCall(http.MethodGet, base+"/identities/me", token, nil, &me)
	var refused *AppserviceError
	if errors.As(err, &refused) && refused.Code == "unauthorized" {
		return "", &AppserviceError{Status: refused.Status, Code: refused.Code, Detail: refused.Detail,
			msg: "the appservice does not take that token: " + refused.Detail + " (unauthorized)"}
	}
	if err == nil && me.MXID == "" {
		err = fail(errAppserviceAnswer, "the appservice returned no mxid")
	}
	return me.MXID, err
}

func setUp(configPath, mxid, tokenFile string) error {
	if err := addEntity(configPath, mxid, tokenFile); err != nil {
		return err
	}
	return printIdentity(mxid, tokenFile)
}

func checkMove(configPath, command, mxid, base string, force bool) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	e, ok := cfg.Entities[mxid]
	if !ok || force {
		return nil
	}
	old, _, _, err := appserviceCredentials(e)
	if err != nil || old == base {
		return nil
	}
	return usageError(command, "%s is set up here with the appservice at %s: --force to move it to %s", mxid, old, base)
}

func cmdIdentityToken(configPath string, args []string) error {
	fs := flag.NewFlagSet("identity token", flag.ExitOnError)
	base := fs.String("base", "", "")
	insecure := fs.Bool("insecure", false, "")
	parse(fs, args)
	if err := need(fs, "MXID"); err != nil {
		return err
	}
	b, token, target, path, err := identityAsker(configPath, fs.Name(), "a new token for it", fs.Arg(0), *base, *insecure)
	if err != nil {
		return err
	}
	var out issuedToken
	if err := appserviceCall(http.MethodPost, b+"/identities/"+url.QueryEscape(target)+"/token", token, nil, &out); err != nil {
		return err
	}
	if out.MXID == "" || out.APIToken == "" {
		return fail(errAppserviceAnswer, "the appservice returned no mxid or token")
	}
	if path == "" {
		path = tokenPath(configPath, out.MXID)
	}
	env := tokenFileContent(b, out.APIToken, out.MXID)
	file := env
	if old, err := os.ReadFile(path); err == nil { // other lines of the file are the entity's: they stay
		lines := strings.SplitAfter(string(old), "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "MATRIX_AI_TOKEN=") {
				lines[i] = "MATRIX_AI_TOKEN=" + out.APIToken + "\n"
			}
		}
		file = strings.Join(lines, "")
	}
	if err := writeReplace(path, file); err != nil {
		fmt.Fprint(os.Stderr, "could not write the token file; save this now, it is not shown again:\n"+env)
		return err
	}
	if err := addEntity(configPath, out.MXID, path); err != nil {
		return fail(errPartial, "token file written, but not set up here (identity login --token-file %s): %w", path, err)
	}
	return printIdentity(out.MXID, path)
}

func identityAsker(configPath, command, what, id, base string, insecure bool) (b, token, target, tokenFile string, err error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return
	}
	admin := os.Getenv("MATRIX_AI_ADMIN_TOKEN")
	b, token, target = strings.TrimRight(base, "/"), admin, id
	if e, ok := cfg.Entities[id]; ok {
		entityBase, entityToken, _, err := appserviceCredentials(e)
		if err != nil {
			return "", "", "", "", err
		}
		if b != "" && b != entityBase {
			return "", "", "", "", usageError(command, "%s is set up here with the appservice at %s: to move it, identity login --force", id, entityBase)
		}
		b = entityBase
		if admin == "" {
			token, target = entityToken, "me"
		}
		return b, token, target, e.TokenFile, nil
	}
	if b == "" || admin == "" {
		return "", "", "", "", fail(errNotHere, "%s is not set up here: %s needs --base and MATRIX_AI_ADMIN_TOKEN", id, what)
	}
	return b, token, target, "", checkAppserviceBase(command, b, insecure)
}

func cmdIdentityRevoke(configPath string, args []string) error {
	fs := flag.NewFlagSet("identity revoke", flag.ExitOnError)
	base := fs.String("base", "", "")
	insecure := fs.Bool("insecure", false, "")
	parse(fs, args)
	if err := need(fs, "MXID"); err != nil {
		return err
	}
	b, token, target, _, err := identityAsker(configPath, fs.Name(), "revoking it", fs.Arg(0), *base, *insecure)
	if err != nil {
		return err
	}
	return appserviceCall(http.MethodDelete, b+"/identities/"+url.QueryEscape(target)+"/token", token, nil, nil)
}

func tokenFileContent(base, token, mxid string) string {
	return fmt.Sprintf("MATRIX_AI_BASE=%s\nMATRIX_AI_TOKEN=%s\nMATRIX_AI_MXID=%s\n", base, token, mxid)
}

func printIdentity(mxid, tokenFile string) error {
	abs, err := filepath.Abs(tokenFile)
	if err != nil {
		return err
	}
	fmt.Printf("%s\t%s\n", mxid, abs)
	return nil
}

func cmdIdentityList(configPath string, args []string) error {
	fs := flag.NewFlagSet("identity list", flag.ExitOnError)
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(cfg.Entities))
	for id := range cfg.Entities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		mark, hook := " ", "no webhook"
		if id == os.Getenv("MATRIX_AI_MXID") {
			mark = "*"
		}
		if cfg.Entities[id].hasWebhook() {
			hook = "webhook"
		}
		fmt.Printf("%s %s\t%s\t%s\n", mark, id, hook, cfg.Entities[id].TokenFile)
	}
	return nil
}

func addEntity(configPath, mxid, tokenFile string) error {
	tf, err := filepath.Abs(tokenFile)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	e, ok := cfg.Entities[mxid]
	if !ok {
		e = &Entity{}
		cfg.Entities[mxid] = e
	} else if e.TokenFile == tf {
		return nil
	}
	e.TokenFile = tf
	return saveConfig(configPath, cfg)
}

// Without : and ., which Windows refuses in names.
func tokenPath(configPath, mxid string) string {
	name := strings.NewReplacer(":", "_", ".", "_").Replace(strings.TrimPrefix(mxid, "@"))
	return filepath.Join(filepath.Dir(configPath), "identities", name, "token.env")
}

func writeReplace(path, s string) error {
	tmp := path + ".new"
	os.Remove(tmp)
	if err := writeNew(tmp, s); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeNew(path, s string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
