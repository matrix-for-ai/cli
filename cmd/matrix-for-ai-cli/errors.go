package main

import (
	"errors"
	"fmt"
	"strings"
)

// 3 means only that nobody listens: handlers branch on it.
var (
	errUsage                 = errors.New("usage")
	errNotHere               = errors.New("not here")
	errTokenFile             = errors.New("token file")
	errConfig                = errors.New("config")
	errAppservice            = errors.New("appservice")
	errAppserviceUnreachable = errors.New("appservice unreachable")
	errAppserviceAnswer      = errors.New("appservice answer")
	errPartial               = errors.New("partial")
	errNotTaken              = errors.New("not taken")
	errUnsupported           = errors.New("unsupported")
	errSystem                = errors.New("system")
	errNoListener            = errors.New("no live session listening")
)

var errorKinds = map[string]error{
	"usage": errUsage, "not_here": errNotHere, "token_file": errTokenFile, "config": errConfig,
	"appservice": errAppservice, "appservice_unreachable": errAppserviceUnreachable, "appservice_answer": errAppserviceAnswer, "partial": errPartial,
	"not_taken": errNotTaken, "unsupported": errUnsupported, "system": errSystem, "no_listener": errNoListener,
}

func exitCode(err error) int {
	switch {
	case errors.Is(err, errUsage):
		return 2
	case errors.Is(err, errNoListener):
		return 3
	}
	return 1
}

type kindError struct {
	kind error
	msg  string
	err  error
}

func (e *kindError) Error() string        { return e.msg }
func (e *kindError) Is(target error) bool { return target == e.kind }
func (e *kindError) Unwrap() error        { return e.err }

func fail(kind error, format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	return &kindError{kind, err.Error(), errors.Unwrap(err)}
}

func usageError(command, format string, a ...any) error {
	problem := fmt.Sprintf(format, a...)
	if command == "" {
		return &kindError{kind: errUsage, msg: problem + "\nsee: matrix-for-ai-cli --help"}
	}
	first, _, _ := strings.Cut(command, " ")
	return &kindError{kind: errUsage, msg: fmt.Sprintf("%s: %s\nsee: matrix-for-ai-cli %s --help", command, problem, first)}
}

type AppserviceError struct {
	Status int
	Code   string
	Detail string
	msg    string
}

func (e *AppserviceError) Error() string        { return e.msg }
func (e *AppserviceError) Is(target error) bool { return target == errAppservice }
