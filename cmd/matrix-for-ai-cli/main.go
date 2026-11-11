package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
)

const version = "1.0.0"

func main() {
	log.SetFlags(0)
	if err := cli(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(exitCode(err))
	}
}

func cli(argv []string) error {
	global := flag.NewFlagSet("matrix-for-ai-cli", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	configPath := global.String("config", defaultConfigPath(), "")
	state := global.String("state", defaultStateDir(), "")
	as := global.String("as", "", "")
	if err := global.Parse(argv); errors.Is(err, flag.ErrHelp) {
		fmt.Print(usage)
		return nil
	} else if err != nil {
		return usageError("", "%s", flagProblem(err))
	}
	args := global.Args()
	if *as != "" {
		os.Setenv("MATRIX_AI_MXID", *as) // the default of each command's --as
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if args[0] == "help" && len(args) > 1 {
		args = []string{args[1], "--help"}
	}
	if len(args) > 1 && (args[1] == "-h" || args[1] == "--help" || args[1] == "-help") {
		if h, ok := helps[args[0]]; ok {
			fmt.Print(h)
			return nil
		}
	}
	var err error
	switch args[0] {
	case "serve":
		err = cmdServe(*configPath, *state, args[1:])
	case "unread":
		err = cmdUnread(*configPath, *state, args[1:])
	case "read":
		err = cmdRead(*configPath, *state, args[1:])
	case "send":
		err = cmdSend(*configPath, args[1:])
	case "event":
		err = cmdEvent(*configPath, args[1:])
	case "channel":
		err = cmdChannel(*configPath, args[1:])
	case "profile":
		err = cmdProfile(*configPath, args[1:])
	case "voices":
		err = cmdVoices(*configPath, args[1:])
	case "listen":
		err = cmdListen(args[1:])
	case "deliver":
		err = cmdDeliver(args[1:])
	case "identity":
		err = cmdIdentity(*configPath, args[1:])
	case "mute":
		err = cmdMute(*configPath, *state, args[1:])
	case "unmute":
		err = cmdUnmute(*configPath, *state, args[1:])
	case "status":
		err = cmdStatus(*configPath, *state, args[1:])
	case "service":
		err = cmdService(*configPath, args[1:])
	case "webhook":
		err = cmdWebhook(*configPath, args[1:])
	case "version":
		if len(args) > 1 {
			return usageError("version", "unexpected %q", args[1])
		}
		fmt.Printf("matrix-for-ai-cli %s (appservice API v%d)\n", version, appserviceAPIVersion)
	case "help":
		fmt.Print(usage)
	default:
		err = usageError("", "unknown command %q", args[0])
	}
	return err
}
