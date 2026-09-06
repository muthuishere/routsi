package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

type report struct {
	OK         bool       `json:"ok"`
	Operation  string     `json:"operation"`
	Provenance provenance `json:"provenance"`
	Credential credMeta   `json:"credential"`
	Discovery  *rpcMeta   `json:"discovery,omitempty"`
	Blocker    string     `json:"blocker,omitempty"`
}

func main() {
	var credentialsPath, devinBin string
	var live bool
	flag.StringVar(&credentialsPath, "credentials", defaultCredentialsPath(), "Devin credentials TOML")
	flag.StringVar(&devinBin, "devin-bin", "/opt/homebrew/bin/devin", "installed Devin binary")
	flag.BoolVar(&live, "live-discovery", false, "call the read-only model discovery RPC")
	flag.Parse()

	r := report{Operation: "inspect", Provenance: inspectBinary(devinBin)}
	if !live {
		r.OK = true
		r.Blocker = "live discovery not requested; credentials were not read"
		emit(r)
		return
	}
	creds, meta, err := loadCredentials(credentialsPath)
	r.Credential = meta
	if err != nil {
		r.Blocker = publicError(err)
		emit(r)
		os.Exit(1)
	}
	r.Operation = "model_discovery"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m, err := discoverModels(ctx, hardenedHTTPClient(15*time.Second), creds)
	if err != nil {
		r.Blocker = publicError(err)
		emit(r)
		os.Exit(1)
	}
	r.OK, r.Discovery = true, &m
	emit(r)
}

func emit(v report) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "failed to encode sanitized report")
		os.Exit(1)
	}
}
