// Command routsi is a single OpenAI-compatible endpoint that routes each
// request to the right model or agent. Subcommands:
//
//	routsi [serve]      run the server (default)
//	routsi install      install as a keep-alive background service (watchdog)
//	routsi uninstall    remove the service
//	routsi status       is the service running?
//	routsi version      print version
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/muthuishere/routsi/internal/backend"
	"github.com/muthuishere/routsi/internal/config"
	"github.com/muthuishere/routsi/internal/discovery"
	"github.com/muthuishere/routsi/internal/server"
	"github.com/muthuishere/routsi/internal/service"
)

var version = "dev"

func main() {
	log.SetFlags(log.Ltime)
	cmd := "serve"
	if len(os.Args) > 1 && os.Args[1] != "" && os.Args[1][0] != '-' {
		cmd = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...) // strip subcommand for flag parsing
	}

	switch cmd {
	case "serve":
		serve()
	case "install":
		manage("install")
	case "uninstall":
		manage("uninstall")
	case "status":
		manage("status")
	case "token":
		genTokens()
	case "certs":
		genCerts()
	case "worker":
		worker()
	case "version", "-v", "--version":
		fmt.Println("routsi", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`routsi — route each request to the right model or agent, behind one OpenAI API.

usage:
  routsi [serve] [-config path]   run the server (default)
  routsi install  [-config path]  install as a keep-alive background service
  routsi install  --skills        install the agent skill(s) into ~/.claude, ~/.codex
  routsi worker   run  --proxy URL --queue NAME --agent 'cmd'   run a pull-worker
  routsi worker   scaffold        print an editable curl worker script
  routsi uninstall                remove the service
  routsi status                   is the service running?
  routsi token [-n count]         generate API bearer tokens for auth.tokens_env
  routsi certs [-dir d] [-host h] generate CA + server + client certs for mTLS
  routsi version

config resolution: -config flag > ./models.yaml > ~/.config/routsi/models.yaml
dashboard + metrics: http://<listen>/  ·  /stats  ·  /metrics
`)
}

// resolveConfigPath applies the flag > ./models.yaml > ~/.config fallback.
func resolveConfigPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if _, err := os.Stat("models.yaml"); err == nil {
		abs, _ := filepath.Abs("models.yaml")
		return abs
	}
	if dir := config.ConfigDir(); dir != "" {
		return filepath.Join(dir, "models.yaml")
	}
	return "models.yaml"
}

func serve() {
	cfgPath := flag.String("config", "", "path to models.yaml")
	flag.Parse()
	path := resolveConfigPath(*cfgPath)

	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := discovery.Populate(context.Background(), cfg); err != nil {
		log.Printf("warning: %v", err) // per-model, non-fatal
	}
	// Custom handlers are registered here when embedding routsi as a library;
	// the stock binary ships with none.
	reg := backend.NewRegistry()

	srv, err := server.New(cfg, reg, nil)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	authNote := "auth OFF (open)"
	if n := len(cfg.Auth.AuthTokens()); n > 0 {
		authNote = fmt.Sprintf("auth ON (%d tokens)", n)
	}
	log.Printf("routsi %s on %s — %d models, default %s, %s — dashboard http://localhost%s/",
		version, cfg.Listen, len(cfg.Models), cfg.Default, authNote, cfg.Listen)

	h := srv.Handler()
	if cfg.TLS.Cert != "" {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.TLS.ClientCA != "" { // mTLS: require + verify client certs
			ca, err := os.ReadFile(cfg.TLS.ClientCA)
			if err != nil {
				log.Fatalf("tls client_ca: %v", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				log.Fatalf("tls client_ca: no certs parsed from %s", cfg.TLS.ClientCA)
			}
			tlsCfg.ClientCAs = pool
			tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
			log.Printf("mTLS on: client certs required (CA %s)", cfg.TLS.ClientCA)
		}
		hs := &http.Server{Addr: cfg.Listen, Handler: h, TLSConfig: tlsCfg}
		log.Fatal(hs.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key))
	}
	log.Fatal(http.ListenAndServe(cfg.Listen, h))
}

// genTokens prints cryptographically random bearer tokens for auth.tokens_env.
// Tokens are printed once and never stored — the operator owns them from here.
func genTokens() {
	n := flag.Int("n", 1, "how many tokens")
	flag.Parse()
	if *n < 1 || *n > 100 {
		log.Fatal("token: -n must be 1..100")
	}
	tokens := make([]string, *n)
	for i := range tokens {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("token: %v", err)
		}
		tokens[i] = "rtk_" + base64.RawURLEncoding.EncodeToString(b)
		fmt.Println(tokens[i])
	}
	fmt.Fprintf(os.Stderr, `
Enable auth: put in models.yaml
  auth:
    tokens_env: ROUTSI_TOKENS
then export (add to your shell profile or service env):
  export ROUTSI_TOKENS="%s"
Clients authenticate with it as their OpenAI api_key
(Authorization: Bearer <token>); dashboard: /?token=<token>
`, strings.Join(tokens, ","))
}

func manage(action string) {
	cfgPath := flag.String("config", "", "path to models.yaml")
	skills := flag.Bool("skills", false, "install the agent skill(s) into global skill dirs")
	flag.Parse()

	switch action {
	case "install":
		if *skills {
			installSkills()
			return
		}
		bin, err := os.Executable()
		if err != nil {
			log.Fatalf("cannot locate own binary: %v", err)
		}
		bin, _ = filepath.Abs(bin)
		summary, err := service.Install(service.Params{Binary: bin, Config: resolveConfigPath(*cfgPath)})
		if err != nil {
			log.Fatalf("install: %v", err)
		}
		fmt.Println(summary)
	case "uninstall":
		summary, err := service.Uninstall()
		if err != nil {
			log.Fatalf("uninstall: %v", err)
		}
		fmt.Println(summary)
	case "status":
		summary, err := service.Status()
		if err != nil {
			log.Fatalf("status: %v", err)
		}
		fmt.Println(summary)
	}
}
