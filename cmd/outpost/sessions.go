package main

// Local, tmux-style session commands. Everything here runs ON THE HOST and
// talks DIRECTLY to the dtach sockets + the store on disk — no daemon, no
// WebSocket (that's only for the remote phone). Sessions are shared: one made
// here shows up in the app, and one made by the app can be attached here.
//
//	outpost ls                 list sessions (outpost/dtach + tmux + zellij)
//	outpost attach <id|name>   attach locally (dtach -a / tmux / zellij attach)
//	outpost new [title]        create a session and drop into it (dtach -A)

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/godx-jp/godx-outpost/internal/auth"
	"github.com/godx-jp/godx-outpost/internal/term"
)

// sessionPaths resolves the config dir + its sessions/shellrc subdirs (same
// layout the daemon uses), honoring the global --config-dir.
func sessionPaths() (cfgDir, sessDir, shellrcDir string) {
	cfgDir = flagConfigDir
	if cfgDir == "" {
		cfgDir, _ = auth.DefaultConfigDir()
	}
	sessDir = filepath.Join(cfgDir, "sessions")
	shellrcDir = filepath.Join(cfgDir, "shellrc")
	if _, err := os.Stat(filepath.Join(shellrcDir, ".zshrc")); err != nil {
		shellrcDir = ""
	}
	return
}

// execReplace replaces the current process with bin (found on PATH), so the
// attached terminal takes over the tty — exactly like running tmux/dtach.
func execReplace(bin string, argv, env []string) error {
	path, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("%s not found on PATH: %w", bin, err)
	}
	return syscall.Exec(path, argv, env)
}

func lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "sessions"},
		Short:   "List terminal sessions on this host (shared with the app)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, sessDir, _ := sessionPaths()
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load: %w", err)
			}
			defer mgr.Close()

			sessions := term.LocalSessions(sessDir, mgr.Store())
			tmux := term.ListTmux()
			zellij := term.ListZellij()
			if len(sessions) == 0 && len(tmux) == 0 && len(zellij) == 0 {
				fmt.Println("No sessions. Start one with:  outpost new")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION\tKIND\tSTATE\tFOLDER")
			for _, s := range sessions {
				state := "running"
				if !s.Alive {
					state = "stopped"
				}
				name := s.ID
				if s.Title != "" {
					name = fmt.Sprintf("%s (%s)", s.ID, s.Title)
				}
				fmt.Fprintf(w, "%s\toutpost\t%s\t%s\n", name, state, s.Cwd)
			}
			for _, t := range tmux {
				fmt.Fprintf(w, "%s\ttmux\trunning\t\n", t.Name)
			}
			for _, z := range zellij {
				fmt.Fprintf(w, "%s\tzellij\trunning\t\n", z.Name)
			}
			_ = w.Flush()
			fmt.Println("\nAttach with:  outpost attach <session>")
			return nil
		},
	}
}

func attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "attach <id|name>",
		Aliases: []string{"a"},
		Short:   "Attach to a session locally (like tmux attach)",
		Long: `Attach this terminal to a running session. An outpost (dtach) session is
attached with dtach; a tmux/zellij session drops straight into tmux/zellij.
Detach an outpost/dtach session with Ctrl-\.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			_, sessDir, _ := sessionPaths()

			// 1) outpost (dtach) session by id.
			if sock, alive := term.SessionSocketAlive(sessDir, name); alive {
				return execReplace("dtach", []string{"dtach", "-a", sock, "-r", "winch"}, os.Environ())
			} else if _, err := os.Stat(sock); err == nil {
				return fmt.Errorf("session %q is not running — try `outpost restore`", name)
			}
			// 2) tmux session by name.
			for _, t := range term.ListTmux() {
				if t.Name == name {
					return execReplace("tmux", []string{"tmux", "attach", "-t", name}, os.Environ())
				}
			}
			// 3) zellij session by name.
			for _, z := range term.ListZellij() {
				if z.Name == name {
					return execReplace("zellij", []string{"zellij", "attach", name}, os.Environ())
				}
			}
			return fmt.Errorf("no session %q (run `outpost ls`)", name)
		},
	}
}

func newSessionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "new [title]",
		Aliases: []string{"n"},
		Short:   "Create a new terminal session and attach to it (like tmux new)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, lerr := exec.LookPath("dtach"); lerr != nil {
				return fmt.Errorf("`outpost new` needs dtach (brew install dtach / apt install dtach)")
			}
			_, sessDir, shellrcDir := sessionPaths()
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load: %w", err)
			}
			title := ""
			if len(args) == 1 {
				title = args[0]
			}
			id, sock, shell, env, err := term.NewLocalSession(sessDir, shellrcDir, os.Getenv("SHELL"), title, mgr.Store())
			mgr.Close() // flush the store before we exec away (defer won't run)
			if err != nil {
				return fmt.Errorf("new session: %w", err)
			}
			fmt.Fprintf(os.Stderr, "outpost: new session %s — detach with Ctrl-\\\n", id)
			// dtach -A: create the session (running the shell) if needed, attach.
			return execReplace("dtach", []string{"dtach", "-A", sock, "-z", "-r", "winch", shell}, env)
		},
	}
}
