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
	"github.com/muthuishere/routsi/internal/router"
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
  routsi [serve] [-config path] [-listen addr] [-port n] [-env file]   run the server (default)
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
listen resolution: -listen flag > -port flag > models.yaml 'listen:' > ':8080'
env resolution:    process env > -env/ROUTSI_ENV_FILE > ./.env > ~/.config/routsi/.env
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

// resolveListen applies the -listen/-port flag precedence: -listen wins
// outright; -port is a shorthand that becomes ":PORT" when it's digits-only
// (so "-port 9000" -> ":9000"), or is used as-is otherwise (e.g. "0.0.0.0:9000").
// Empty return means "no override" — caller keeps whatever config.Load set
// (yaml `listen:` or its own ":8080" default).
func resolveListen(listenFlag, portFlag string) string {
	if listenFlag != "" {
		return listenFlag
	}
	if portFlag == "" {
		return ""
	}
	if isDigits(portFlag) {
		return ":" + portFlag
	}
	return portFlag
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// loadEnvFiles layers env sources so all three work together. Precedence,
// highest first: the process environment (never overridden), then an explicit
// env file (-env flag or ROUTSI_ENV_FILE), then ./.env, then
// ~/.config/routsi/.env. LoadDotEnv never overrides an already-set key, so
// loading in this order makes earlier sources win. Values are never logged.
func loadEnvFiles(explicit string) {
	if explicit == "" {
		explicit = os.Getenv("ROUTSI_ENV_FILE")
	}
	var paths []string
	add := func(p string) {
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		for _, e := range paths {
			if e == p {
				return // dedupe
			}
		}
		paths = append(paths, p)
	}
	add(explicit)
	add(".env")
	if dir := config.ConfigDir(); dir != "" {
		add(filepath.Join(dir, ".env"))
	}
	for _, p := range paths {
		n, err := config.LoadDotEnv(p)
		if err != nil {
			log.Printf("warning: env file %s: %v", p, err)
			continue
		}
		if n > 0 {
			log.Printf("loaded %d var(s) from %s", n, p)
		}
	}
}

func serve() {
	cfgPath := flag.String("config", "", "path to models.yaml")
	listenFlag := flag.String("listen", "", "listen address, e.g. :8080 or 0.0.0.0:8080 (overrides models.yaml listen)")
	portFlag := flag.String("port", "", "listen port shorthand for -listen ':PORT'")
	envFlag := flag.String("env", "", "path to an env file to load (in addition to ./.env and ~/.config/routsi/.env)")
	flag.Parse()

	loadEnvFiles(*envFlag)

	path := resolveConfigPath(*cfgPath)

	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if addr := resolveListen(*listenFlag, *portFlag); addr != "" {
		cfg.Listen = addr
	}
	if err := discovery.Populate(context.Background(), cfg); err != nil {
		log.Printf("warning: %v", err) // per-model, non-fatal
	}
	// Custom handlers are registered here when embedding routsi as a library;
	// the stock binary ships with none.
	reg := backend.NewRegistry()

	var rt router.Router
	if cfg.Decider.Command != "" {
		rt = router.NewExternal(cfg.Decider.Command, cfg.Decider.Timeout, cfg.Decider.Cwd, cfg.Tiers, router.NewRules())
		log.Printf("using external decider: %s", cfg.Decider.Command)
	}

	srv, err := server.New(cfg, reg, rt)
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
