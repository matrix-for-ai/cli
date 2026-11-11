package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The help and the code, both ways: every flag a command defines is in its help, every flag its help
// names exists, every environment variable the client reads is in some help, and every one a help
// names is read or set by the client.

var globalFlags = []string{"config", "state", "as"}

// readForSystemd: systemd's socket activation protocol, not for people.
var readForSystemd = map[string]bool{"LISTEN_PID": true, "LISTEN_FDS": true}

// codeFlags: the flags each command defines, by its help's key ("" is the general one), from the source.
func codeFlags(t *testing.T) (flags map[string]map[string]bool, env map[string]bool, literals map[string]bool) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	flags, env, literals = map[string]map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range pkgs["main"].Files {
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					literals[s] = true
				}
			}
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			cmd := ""
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := callName(call)
				switch {
				case name == "flag.NewFlagSet":
					cmd, found = flagSetKey(call.Args[0]), true
				case name == "os.Getenv":
					if s, ok := stringArg(call, 0); ok {
						env[s] = true
					}
				}
				return true
			})
			if !found {
				return true
			}
			if flags[cmd] == nil {
				flags[cmd] = map[string]bool{}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := callName(call)
				if name == "asFlag" {
					flags[cmd]["as"] = true
				}
				method, _ := strings.CutPrefix(name, "fs.")
				if method == name {
					method, _ = strings.CutPrefix(name, "global.")
				}
				if method == name {
					return true
				}
				arg := 0
				if strings.HasSuffix(method, "Var") {
					arg = 1
				}
				switch strings.TrimSuffix(method, "Var") {
				case "String", "Bool", "Duration", "Int", "Int64", "Uint", "Float64":
					if s, ok := stringArg(call, arg); ok {
						flags[cmd][s] = true
					}
				}
				return true
			})
			return true
		})
	}
	return flags, env, literals
}

func callName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
	}
	return ""
}

func stringArg(call *ast.CallExpr, i int) (string, bool) {
	if len(call.Args) <= i {
		return "", false
	}
	lit, ok := call.Args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// flagSetKey is the help a flag set belongs to: its name's first word ("channel "+args[0] → channel).
func flagSetKey(e ast.Expr) string {
	if b, ok := e.(*ast.BinaryExpr); ok {
		e = b.X
	}
	lit, ok := e.(*ast.BasicLit)
	if !ok {
		return "?"
	}
	s, _ := strconv.Unquote(lit.Value)
	s = strings.TrimSpace(s)
	if s == "matrix-for-ai-cli" {
		return ""
	}
	first, _, _ := strings.Cut(s, " ")
	return first
}

func helpText(key string) string {
	if key == "" {
		return usage
	}
	return helps[key]
}

var helpFlag = regexp.MustCompile(`(?:^|[\s\[(|])--?([a-z][a-z0-9-]*)`)
var helpEnv = regexp.MustCompile(`\b((?:MATRIX|NOTICE|XDG|LISTEN)_[A-Z0-9_]+)\b`)

func TestHelpMatchesCode(t *testing.T) {
	flags, env, literals := codeFlags(t)
	var found []string
	note := func(s string) { found = append(found, s) }

	keys := []string{""}
	for k := range helps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h := helpText(k)
		named := map[string]bool{}
		for _, m := range helpFlag.FindAllStringSubmatch(h, -1) {
			named[m[1]] = true
		}
		for f := range flags[k] {
			if !named[f] && !(k == "" && slices.Contains(globalFlags, f)) {
				note("flag " + k + " --" + f + ": in the code, not in its help")
			}
		}
		if k == "" || k == "handlers" || k == "unmute" {
			continue // the general help names every command's flags; handlers, other programs'; unmute is mute's
		}
		for f := range named {
			if flags[k][f] || slices.Contains(globalFlags, f) || f == "help" || f == "h" || crossRef(h, f, flags) {
				continue
			}
			note("flag " + k + " --" + f + ": in its help, not in the code")
		}
	}
	// The general help names each command's flags too: none it names may be unknown to all.
	all := map[string]bool{}
	for _, fs := range flags {
		for f := range fs {
			all[f] = true
		}
	}
	for _, m := range helpFlag.FindAllStringSubmatch(usage, -1) {
		if !all[m[1]] && m[1] != "help" {
			note("flag --" + m[1] + ": in the general help, in no command")
		}
	}

	helpAll := usage
	for _, h := range helps {
		helpAll += h
	}
	for v := range env {
		if !strings.Contains(helpAll, v) && !readForSystemd[v] {
			note("env " + v + ": read, in no help")
		}
	}
	for _, m := range helpEnv.FindAllStringSubmatch(helpAll, -1) {
		v := m[1]
		if !env[v] && !literals[v] && !literals[v+"="] && !hasLiteralPrefix(literals, v+"=") {
			note("env " + v + ": in a help, neither read nor set")
		}
	}

	sort.Strings(found)
	reportAll(t, slices.Compact(found))
}

func reportAll(t *testing.T, found []string) {
	for _, f := range found {
		t.Errorf("%s", f)
	}
}

func hasLiteralPrefix(literals map[string]bool, p string) bool {
	for l := range literals {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// crossRef: the flag is named as another command's, on a line that names that command first
// ("profile set --voice").
func crossRef(h, f string, flags map[string]map[string]bool) bool {
	for _, line := range strings.Split(h, "\n") {
		i := strings.Index(line, "--"+f)
		if i < 0 {
			continue
		}
		for c, fs := range flags {
			if c != "" && fs[f] && strings.Contains(line[:i], c+" ") {
				return true
			}
		}
	}
	return false
}

// TestCommandsMatchHelp: the commands cli runs, the general help and the helps, both ways.
func TestCommandsMatchHelp(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cmds := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "cli" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if cc, ok := n.(*ast.CaseClause); ok {
				for _, e := range cc.List {
					if lit, ok := e.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(lit.Value)
						if !strings.HasPrefix(s, "-") {
							cmds[s] = true
						}
					}
				}
			}
			return true
		})
		return false
	})
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z]+)(?: / ([a-z]+))?`).FindAllStringSubmatch(usage, -1) {
		listed[m[1]] = true
		if m[2] != "" {
			listed[m[2]] = true
		}
	}
	var found []string
	for c := range cmds {
		if !listed[c] {
			found = append(found, "command "+c+": runs, not in the general help")
		}
		if _, ok := helps[c]; !ok && c != "help" {
			found = append(found, "command "+c+": runs, no help of its own")
		}
	}
	for c := range listed {
		if !cmds[c] {
			found = append(found, "command "+c+": in the general help, does not run")
		}
	}
	for c := range helps {
		if !cmds[c] && !listed[c] && c != "handlers" { // a topic: help handlers
			found = append(found, "help "+c+": no such command")
		}
	}
	sort.Strings(found)
	reportAll(t, found)
}

func TestHelpLinesFitInATerminal(t *testing.T) {
	texts := map[string]string{"": usage}
	for k, h := range helps {
		texts[k] = h
	}
	for k, h := range texts {
		for _, line := range strings.Split(h, "\n") {
			if n := len([]rune(line)); n > 100 {
				t.Errorf("help %q: a line of %d columns: %s", k, n, line)
			}
		}
	}
}
