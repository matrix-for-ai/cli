package main

import "os"

func privateDir(dir string) error { return os.MkdirAll(dir, 0o700) }
