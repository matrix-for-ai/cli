package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

func parse(fs *flag.FlagSet, args []string) { parseUntil(fs, args, 0) }

func parseUntil(fs *flag.FlagSet, args []string, n int) {
	fs.Init(fs.Name(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			parseFailed(fs, err, n > 1 && len(pos) == n-1)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		if used := len(args) - len(rest); used > 0 && args[used-1] == "--" {
			pos = append(pos, rest...)
			break
		}
		pos, args = append(pos, rest[0]), rest[1:]
		if n > 0 && len(pos) == n {
			pos = append(pos, args...)
			break
		}
	}
	fs.Parse(append([]string{"--"}, pos...))
}

func parseFailed(fs *flag.FlagSet, err error, atText bool) {
	cmd, _, _ := strings.Cut(fs.Name(), " ")
	if errors.Is(err, flag.ErrHelp) {
		fmt.Print(helps[cmd])
		os.Exit(0)
	}
	problem := flagProblem(err)
	if atText {
		problem += " (text that starts with - goes after --, or on stdin: -)"
	}
	fmt.Fprintln(os.Stderr, usageError(fs.Name(), "%s", problem))
	os.Exit(2)
}

var invalidFlagValue = regexp.MustCompile(`^invalid value "(.*)" for flag -(\S+): (.*)$`)

func flagProblem(err error) string {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
		return "unknown flag --" + name
	}
	if name, ok := strings.CutPrefix(msg, "flag needs an argument: -"); ok {
		return "--" + name + " needs a value"
	}
	if m := invalidFlagValue.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("--%s: %q is not valid (%s)", m[2], m[1], m[3])
	}
	return msg
}

func need(fs *flag.FlagSet, names ...string) error {
	switch n := fs.NArg(); {
	case n < len(names):
		return usageError(fs.Name(), "missing %s", strings.Join(names[n:], " "))
	case n > len(names):
		return usageError(fs.Name(), "unexpected %q", fs.Arg(len(names)))
	}
	return nil
}

func notHere(cfg *Config, mxid string) error {
	msg := mxid + " is not set up here (identity create, or webhook create)"
	best, bestD := "", 3 // a typo or two
	var all []string
	for id := range cfg.Entities {
		all = append(all, id)
		if d := editDistance(mxid, id); d < bestD {
			best, bestD = id, d
		}
	}
	switch {
	case best != "":
		msg += "; did you mean " + best + "?"
	case len(all) > 0:
		msg += "; identity list shows the ones that are"
	}
	return &kindError{errNotHere, msg, nil}
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
