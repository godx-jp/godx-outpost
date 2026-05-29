// Command hostd is the CLI that runs on the host machine and serves
// remote terminal/file/monitor/custom-API access to the mobile app.
//
// Commands:
//
//	hostd start   --bind <addr> --port <port>
//	              Loads the device identity, builds the WebSocket server, prints
//	              a pairing QR code, then blocks until SIGINT.
//
//	hostd pair    Loads the device identity and prints a fresh pairing QR code
//	              without starting a server.
//
//	hostd status  Prints the device ID and the path to the config directory.
//
//	hostd revoke  Revokes all outstanding tokens; the next client connection must
//	              re-pair via QR.
//
// See docs/PLAN.md for the full architecture and milestones (M1–M5).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/famgia/remote-host/internal/auth"
	"github.com/famgia/remote-host/internal/channel"
	"github.com/famgia/remote-host/internal/customapi"
	fs "github.com/famgia/remote-host/internal/fs"
	"github.com/famgia/remote-host/internal/launcher"
	"github.com/famgia/remote-host/internal/qr"
	"github.com/famgia/remote-host/internal/server"
	"github.com/famgia/remote-host/internal/sys"
	"github.com/famgia/remote-host/internal/term"
)

// version is the hostd build version. Overridable at build time with
//
//	-ldflags "-X main.version=v1.2.3"
var version = "0.1.0-dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		// cobra already prints the error; just exit non-zero.
		os.Exit(1)
	}
}

// rootCmd builds the top-level cobra command with all sub-commands attached.
func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hostd",
		Short: "hostd – remote-access host daemon",
		Long: `hostd runs on the machine you want to control remotely.

Start the daemon, scan the QR code with the mobile app, and you get a
terminal, file browser, system monitor, and custom API over WebSocket.`,
		// No default action; require an explicit sub-command.
		SilenceUsage: true,
		Version:      version, // enables `hostd --version`
	}

	root.AddCommand(startCmd(), pairCmd(), statusCmd(), revokeCmd(), versionCmd())
	return root
}

// versionCmd prints the hostd version (also available as `hostd --version`).
func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the hostd version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("hostd %s\n", version)
			return nil
		},
	}
}

// ---- helpers -----------------------------------------------------------------

// pairingBlock prints a fresh pairing QR code (and raw payload) for mgr, valid
// for ttl. Shared by both the "start" and "pair" sub-commands.
func pairingBlock(mgr *auth.Manager, wsURL string, ttl time.Duration) {
	code := mgr.StartPairing(ttl)
	payload := qr.PairingPayload(wsURL, mgr.DeviceID(), code)

	fmt.Println()
	if err := qr.Render(payload); err != nil {
		fmt.Fprintf(os.Stderr, "hostd: render QR: %v\n", err)
	}
	fmt.Printf("\nPairing payload : %s\n", payload)
	fmt.Printf("Device ID       : %s\n", mgr.DeviceID())
	fmt.Printf("Pairing code    : %s  (valid %s)\n\n", code, ttl)
}

// makeHandlers returns a FRESH slice of channel.Handler values. It is called
// once per accepted WebSocket connection so every connection starts with
// clean, per-connection state (open PTY sessions, metric subscriptions, …).
func makeHandlers() []channel.Handler {
	return []channel.Handler{
		term.New(launcher.NewDirect()),
		fs.New(),
		sys.New(),
		customapi.New(),
	}
}

// ---- start -------------------------------------------------------------------

func startCmd() *cobra.Command {
	var bind string
	var port string
	var pairTTL time.Duration

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the hostd WebSocket server",
		Long: `Start the WebSocket server, print a pairing QR code, then serve until SIGINT.

The mobile app scans the QR to pair and receives a long-lived token.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.Load()
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}

			addr := bind + ":" + port
			wsURL := "ws://" + addr

			// Build the WebSocket server. makeHandlers is called fresh per
			// connection so channel state (PTYs, tickers, …) is isolated.
			srv := server.New(mgr, makeHandlers)

			// Print the pairing QR before blocking.
			pairingBlock(mgr, wsURL, pairTTL)

			// Run until SIGINT / SIGTERM.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Fprintln(os.Stderr, "\nhostd: shutting down…")
				cancel()
			}()

			if err := srv.ListenAndServe(ctx, addr); err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "bind address")
	cmd.Flags().StringVar(&port, "port", "8722", "listen port")
	cmd.Flags().DurationVar(&pairTTL, "pair-ttl", 2*time.Minute, "how long the pairing code stays valid (e.g. 30m for slow/manual pairing)")
	return cmd
}

// ---- pair --------------------------------------------------------------------

func pairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Print a fresh pairing QR code (without starting the server)",
		Long: `Generate and display a new short-lived pairing code as a QR code.

Use this when you want to pair a new device without restarting the daemon,
or to inspect the pairing payload for debugging.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.Load()
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			// Use a placeholder URL; the user is expected to edit the relay/tunnel
			// URL in their config before sharing with a remote device.
			wsURL := "ws://127.0.0.1:8722"
			pairingBlock(mgr, wsURL, 2*time.Minute)
			return nil
		},
	}
}

// ---- status ------------------------------------------------------------------

// hostdConfigDir resolves the hostd config directory path without loading the
// full auth manager, mirroring the logic in internal/auth (which does not
// export a ConfigDir accessor). Mirrors auth.configDir() logic exactly.
func hostdConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("cannot locate config dir: %v", herr)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hostd"), nil
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print device ID and config directory path",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.Load()
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			dir, err := hostdConfigDir()
			if err != nil {
				return err
			}
			fmt.Printf("hostd version  : %s\n", version)
			fmt.Printf("Device ID      : %s\n", mgr.DeviceID())
			fmt.Printf("Config dir     : %s\n", dir)
			fmt.Printf("Token gen      : %d", mgr.Generation())
			if mgr.Generation() == 0 {
				fmt.Print("  (no revocations; tokens from any pairing are valid)")
			} else {
				fmt.Print("  (revoked; clients before this gen must re-pair)")
			}
			fmt.Println()
			return nil
		},
	}
}

// ---- revoke ------------------------------------------------------------------

func revokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke",
		Short: "Revoke all outstanding tokens",
		Long: `Revoke all previously issued tokens.

After revocation the next client connection must re-pair by scanning a new
QR code. Use this when a paired device is lost or compromised.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.Load()
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			if err := mgr.Revoke(); err != nil {
				return fmt.Errorf("revoke: %w", err)
			}
			fmt.Println("All tokens revoked. Clients must re-pair.")
			return nil
		},
	}
}
