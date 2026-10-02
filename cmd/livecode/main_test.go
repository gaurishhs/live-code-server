package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestReorderArgsAllowsFlagsAfterDirectory(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	port := fs.Int("port", 8787, "")
	verbose := fs.Bool("verbose", false, "")
	args := reorderArgs(fs, []string{".", "--port", "9000", "--verbose"})
	if !reflect.DeepEqual(args, []string{"--port", "9000", "--verbose", "."}) {
		t.Fatalf("args = %#v", args)
	}
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	if *port != 9000 || !*verbose || fs.Arg(0) != "." {
		t.Fatalf("port=%d verbose=%t dir=%q", *port, *verbose, fs.Arg(0))
	}
}
