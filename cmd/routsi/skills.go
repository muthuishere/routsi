package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

// Embedded agent skill(s), shipped in the binary so `routsi install --skills`
// can write them into the user's global skill dirs.
//
//go:embed all:skills
var skillFS embed.FS

// globalSkillDirs are the well-known per-tool skill roots we install into if
// they exist (or their parent does).
func globalSkillDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".codex", "skills"),
	}
}

// installSkills copies embedded skills into each global skill root whose parent
// exists, and returns where it wrote.
func installSkills() {
	fs.WalkDir(skillFS, "skills", func(p string, d fs.DirEntry, err error) error { return err }) //nolint
	var wrote []string
	for _, root := range globalSkillDirs() {
		if _, err := os.Stat(filepath.Dir(root)); err != nil {
			continue // the tool (e.g. ~/.claude) isn't present; skip
		}
		if err := copyEmbeddedSkills(root); err != nil {
			log.Printf("skills: %s: %v", root, err)
			continue
		}
		wrote = append(wrote, root)
	}
	if len(wrote) == 0 {
		fmt.Println("no global skill dirs found (~/.claude or ~/.codex). Nothing installed.")
		return
	}
	for _, w := range wrote {
		fmt.Println("installed skills into", filepath.Join(w, "routsi-worker"))
	}
	fmt.Println("\nThe routsi-worker skill is now available to your agents. Trigger it with:")
	fmt.Println(`  "run a routsi worker" / "join a routsi proxy as a worker"`)
}

func copyEmbeddedSkills(root string) error {
	return fs.WalkDir(skillFS, "skills", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("skills", p)
		if rel == "." {
			return nil
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := skillFS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// worker dispatches `routsi worker <run|scaffold|register|poll|answer>`.
//
// By the time this runs, main() has already stripped the top-level "worker"
// arg (os.Args[1] used to be "worker"; after the strip it's the sub-verb, e.g.
// "run"/"register"), so the sub-verb lives at os.Args[1], not os.Args[2].
func worker() {
	sub := "run"
	if len(os.Args) > 1 && os.Args[1] != "" && os.Args[1][0] != '-' {
		sub = os.Args[1]
	}
	switch sub {
	case "run":
		runWorker()
	case "scaffold":
		scaffoldWorker()
	case "register":
		workerRegisterCmd()
	case "poll":
		workerPollCmd()
	case "answer":
		workerAnswerCmd()
	default:
		fmt.Fprintf(os.Stderr, "usage: routsi worker <run|scaffold|register|poll|answer>\n")
		os.Exit(2)
	}
}

