package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func appserviceFor(configPath, as string) (base, token string, err error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return "", "", err
	}
	_, base, token, err = entityFor(cfg, as)
	return base, token, err
}

func callAndPrint(configPath, as, method, path string, body any) error {
	base, token, err := appserviceFor(configPath, as)
	if err != nil {
		return err
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	data, err := appserviceRaw(method, base+path, token, raw)
	if err != nil {
		return err
	}
	return printAppserviceJSON(data)
}

func printAppserviceJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return fail(errAppserviceAnswer, "the appservice's answer is not JSON: %v", err)
	}
	out.WriteByte('\n')
	_, err := os.Stdout.Write(out.Bytes())
	return err
}

func channelPath(r string) string { return "/channels/" + url.QueryEscape(r) }

const channelSubcommands = "list, create, join, leave, members, invite or kick"

func cmdChannel(configPath string, args []string) error {
	if len(args) == 0 {
		return usageError("channel", "missing subcommand: %s", channelSubcommands)
	}
	fs := flag.NewFlagSet("channel "+args[0], flag.ExitOnError)
	as := asFlag(fs)
	switch args[0] {
	case "list":
		parse(fs, args[1:])
		if err := need(fs); err != nil {
			return err
		}
		base, token, err := appserviceFor(configPath, *as)
		if err != nil {
			return err
		}
		channels, err := appservicePages(base+"/channels", token, "channels")
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"channels": nonNil(channels)})
	case "create":
		name := fs.String("name", "", "")
		group := fs.Bool("group", false, "")
		plain := fs.Bool("plain", false, "")
		public := fs.Bool("public", false, "")
		parse(fs, args[1:])
		if fs.NArg() == 0 && !*public {
			return usageError(fs.Name(), "missing USER")
		}
		body := map[string]any{"invite": append([]string{}, fs.Args()...)}
		if *name != "" {
			body["name"] = *name
		}
		if *group || *public || fs.NArg() > 1 || *name != "" {
			body["direct"] = false
		}
		if *public {
			body["public"] = true
		}
		if *plain {
			body["encrypted"] = false
		}
		return callAndPrint(configPath, *as, http.MethodPost, "/channels", body)
	case "join", "leave", "members":
		parse(fs, args[1:])
		if err := need(fs, "ROOM"); err != nil {
			return err
		}
		if args[0] == "join" {
			return callAndPrint(configPath, *as, http.MethodPut, channelPath(fs.Arg(0)), nil)
		}
		if args[0] == "leave" {
			return callAndPrint(configPath, *as, http.MethodDelete, channelPath(fs.Arg(0)), nil)
		}
		base, token, err := appserviceFor(configPath, *as)
		if err != nil {
			return err
		}
		members, err := appservicePages(base+channelPath(fs.Arg(0))+"/members", token, "members")
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"members": nonNil(members)})
	case "invite", "kick":
		parse(fs, args[1:])
		if err := need(fs, "ROOM", "USER"); err != nil {
			return err
		}
		if args[0] == "invite" {
			return callAndPrint(configPath, *as, http.MethodPost, channelPath(fs.Arg(0))+"/members",
				map[string]string{"user": fs.Arg(1)})
		}
		return callAndPrint(configPath, *as, http.MethodDelete,
			channelPath(fs.Arg(0))+"/members/"+url.PathEscape(fs.Arg(1)), nil)
	}
	return usageError("channel", "unknown subcommand %q: %s", args[0], channelSubcommands)
}

func cmdEvent(configPath string, args []string) error {
	fs := flag.NewFlagSet("event", flag.ExitOnError)
	as := asFlag(fs)
	extra := fs.Bool("extra", false, "")
	parse(fs, args)
	if err := need(fs, "ROOM", "EVENT"); err != nil {
		return err
	}
	path := channelPath(fs.Arg(0)) + "/events/" + url.PathEscape(fs.Arg(1))
	if *extra {
		path += "?include=media,poll"
	}
	return callAndPrint(configPath, *as, http.MethodGet, path, nil)
}

func cmdProfile(configPath string, args []string) error {
	switch {
	case len(args) == 0:
		return usageError("profile", "missing subcommand: set")
	case args[0] != "set":
		return usageError("profile", "unknown subcommand %q: set", args[0])
	}
	fs := flag.NewFlagSet("profile set", flag.ExitOnError)
	as := asFlag(fs)
	name := fs.String("name", "", "")
	avatar := fs.String("avatar", "", "")
	voice := fs.String("voice", "", "")
	transcribe := fs.String("transcribe-audio", "", "")
	parse(fs, args[1:])
	if err := need(fs); err != nil {
		return err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	patch := map[string]any{}
	if given["name"] {
		patch["matrix"] = map[string]any{"display_name": nullIfEmpty(*name)}
	}
	if given["voice"] {
		v, err := parseVoice(*voice)
		if err != nil {
			return err
		}
		patch["tts"] = map[string]any{"voice": v}
	}
	if given["transcribe-audio"] {
		switch *transcribe {
		case "true":
			patch["transcribe_audio"] = true
		case "false":
			patch["transcribe_audio"] = false
		case "":
			patch["transcribe_audio"] = nil
		default:
			return usageError(fs.Name(), "--transcribe-audio: %q is not true or false", *transcribe)
		}
	}
	var image []byte
	var mimetype string
	if *avatar != "" {
		var err error
		if image, mimetype, err = readUpload(fs.Name(), "avatar", *avatar); err != nil {
			return err
		}
	}
	if len(patch) == 0 && !given["avatar"] {
		return usageError(fs.Name(), "nothing to set: --name, --avatar, --voice or --transcribe-audio")
	}
	base, token, err := appserviceFor(configPath, *as)
	if err != nil {
		return err
	}
	me := base + "/identities/me"
	switch {
	case given["avatar"] && *avatar == "":
		err = appserviceCall(http.MethodDelete, me+"/avatar", token, nil, nil)
	case *avatar != "":
		_, err = appserviceSend(http.MethodPut, me+"/avatar", token, mimetype, image)
	}
	if err != nil {
		return err
	}
	var profile []byte
	if len(patch) == 0 {
		profile, err = appserviceRaw(http.MethodGet, me, token, nil)
	} else {
		body, _ := json.Marshal(patch)
		profile, err = appserviceSend(http.MethodPatch, me, token, "application/merge-patch+json", body)
	}
	if err != nil && given["avatar"] {
		return fail(errPartial, "the avatar is set, but: %w", err)
	}
	if err != nil {
		return err
	}
	return printAppserviceJSON(profile)
}

func readUpload(command, flagName, path string) (data []byte, mimetype string, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, "", usageError(command, "--%s: %v", flagName, err)
	}
	mimetype = mime.TypeByExtension(filepath.Ext(path))
	if mimetype == "" {
		mimetype = http.DetectContentType(data)
	}
	return data, mimetype, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseVoice(s string) (any, error) {
	if s == "" {
		return nil, nil
	}
	if !strings.Contains(s, "=") {
		return s, nil
	}
	blend := map[string]float64{}
	for _, part := range strings.Split(s, ",") {
		k, v, _ := strings.Cut(part, "=")
		w, err := strconv.ParseFloat(v, 64)
		if err != nil || k == "" || w <= 0 {
			return nil, usageError("profile set", "--voice: %q is not NAME=WEIGHT", part)
		}
		blend[k] = w
	}
	return blend, nil
}

func cmdVoices(configPath string, args []string) error {
	fs := flag.NewFlagSet("voices", flag.ExitOnError)
	as := asFlag(fs)
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	base, token, err := appserviceFor(configPath, *as)
	if err != nil {
		return err
	}
	var caps struct {
		TTS struct {
			Info struct {
				Voices []string `json:"voices"`
			} `json:"info"`
		} `json:"tts"`
	}
	if err := appserviceCall(http.MethodGet, base+"/capabilities", token, nil, &caps); err != nil {
		return err
	}
	return printJSON(map[string]any{"voices": nonNilStrings(caps.TTS.Info.Voices)})
}

func nonNilStrings(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
