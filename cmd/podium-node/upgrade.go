package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/alvaroibarguen/podium/internal/node"
	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
	"github.com/alvaroibarguen/podium/internal/transport/tailnet"
)

func newUpgradeCommand() *cobra.Command {
	var configPath, baseURL, dest, restart string
	var drain bool
	var drainTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "upgrade VERSION",
		Short: "Download, verify and install a released podium-node, draining first",
		Long: "Download, verify and install a released podium-node.\n\n" +
			"The order matters and is not negotiable: the archive is fetched, checked\n" +
			"against the release's own checksums.txt and unpacked to a staging file BEFORE\n" +
			"anything on this machine changes. Only then is the node drained, the binary\n" +
			"replaced by an atomic rename, the service restarted and the node undrained.\n" +
			"A failure at any step before the rename leaves the running node untouched.\n\n" +
			"Draining asks the control plane to stop giving this node work and waits for\n" +
			"what it is already running to finish. It needs a credential this machine has:\n" +
			"under the dev transport that is the shared token from the node's own config.\n" +
			"If it cannot, drain from the control plane instead —\n" +
			"`podium node drain <name>` — and re-run with --drain=false.\n\n" +
			"There is no auto_upgrade. A worker that replaces its own binary without an\n" +
			"operator asking is a worker that can take a whole fleet down at 3am.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.ErrOrStderr()
			version := args[0]

			cfg, err := node.LoadConfig(configPath)
			if err != nil {
				return err
			}
			identity, enrolled, err := node.LoadIdentity(cfg.DataDir)
			if err != nil {
				return err
			}

			if dest == "" {
				if dest, err = os.Executable(); err != nil {
					return fmt.Errorf("upgrade: cannot find my own path: %w "+
						"(pass --dest with the podium-node to replace)", err)
				}
			}

			stage := node.StagePath(dest)
			fmt.Fprintf(out, "fetching %s for %s...\n", version, dest)
			if err := node.FetchBinary(cmd.Context(), node.UpgradeOptions{
				Version: version, BaseURL: baseURL,
			}, stage); err != nil {
				return err
			}
			// Everything from here can fail with the staged file already on disk; it is
			// removed unless the swap succeeds.
			swapped := false
			defer func() {
				if !swapped {
					_ = os.Remove(stage)
				}
			}()
			fmt.Fprintf(out, "verified, staged at %s\n", stage)

			var admin podiumv1connect.NodeAdminServiceClient
			if drain {
				if !enrolled {
					return errors.New("upgrade: this node has never enrolled, so there is " +
						"nothing to drain; re-run with --drain=false")
				}
				admin = adminClient(cfg)
				if err := drainAndWait(cmd.Context(), out, admin, identity.NodeID, drainTimeout); err != nil {
					return err
				}
			}

			if err := node.SwapBinary(stage, dest); err != nil {
				return err
			}
			swapped = true
			fmt.Fprintf(out, "installed %s\n", dest)

			if restart != "" {
				fmt.Fprintf(out, "restarting: %s\n", restart)
				if err := runRestart(cmd.Context(), restart); err != nil {
					// The binary is already in place, so this is recoverable by hand and
					// must not be reported as a failed upgrade with no explanation.
					fmt.Fprintf(out, "restart failed: %v\n"+
						"The new binary is installed. Restart podium-node yourself, then run\n"+
						"`podium node undrain %s` on the control plane.\n", err, identity.NodeID)
					return err
				}
			}

			if drain && admin != nil {
				if _, err := admin.UndrainNode(cmd.Context(),
					connect.NewRequest(&podiumv1.UndrainNodeRequest{NodeId: identity.NodeID})); err != nil {
					fmt.Fprintf(out, "could not undrain: %v\n"+
						"The node is upgraded but will take no work until you run "+
						"`podium node undrain %s`.\n", err, identity.NodeID)
					return err
				}
				fmt.Fprintf(out, "undrained %s\n", identity.NodeID)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to node.yaml (default "+node.DefaultConfigPath+")")
	cmd.Flags().StringVar(&baseURL, "base-url", node.DefaultReleaseBaseURL,
		"where the release archives and checksums.txt live")
	cmd.Flags().StringVar(&dest, "dest", "", "the podium-node binary to replace (default: this one)")
	cmd.Flags().StringVar(&restart, "restart-command", "systemctl restart podium-node",
		"how to restart the daemon once the binary is in place; empty skips it")
	cmd.Flags().BoolVar(&drain, "drain", true, "drain this node and wait for its tasks before swapping")
	cmd.Flags().DurationVar(&drainTimeout, "drain-timeout", 15*time.Minute,
		"how long to wait for running tasks to finish before giving up")
	return cmd
}

// adminClient builds a control-plane client with whatever credential this machine holds.
// Under the dev transport that is the shared token in the node's config; over a tailnet the
// machine's own tailscaled is the credential, which only exists when the node was configured
// with transport: host. A node running its own embedded tsnet device has nothing to lend,
// and the drain fails with a message saying so.
func adminClient(cfg node.Config) podiumv1connect.NodeAdminServiceClient {
	var httpClient *http.Client
	if cfg.Transport == node.TransportDev {
		httpClient = dev.NewClient(cfg.DevToken)
	} else {
		httpClient = tailnet.NewHostClient()
	}
	return podiumv1connect.NewNodeAdminServiceClient(httpClient, cfg.Server)
}

// drainAndWait asks the control plane to stop scheduling to this node, then polls until it
// reports no running tasks. It polls ListNodes rather than looking at the local Docker
// engine because the control plane's view is the one that decides whether a task is lost.
func drainAndWait(
	ctx context.Context,
	out io.Writer,
	admin podiumv1connect.NodeAdminServiceClient,
	nodeID string,
	timeout time.Duration,
) error {
	if _, err := admin.DrainNode(ctx, connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: nodeID})); err != nil {
		return fmt.Errorf("upgrade: drain %s: %w\n"+
			"Drain from the control plane instead (`podium node drain %s`), wait for its "+
			"tasks to finish, then re-run with --drain=false", nodeID, err, nodeID)
	}
	fmt.Fprintf(out, "draining %s, waiting for running tasks...\n", nodeID)

	deadline := time.Now().Add(timeout)
	for {
		res, err := admin.ListNodes(ctx, connect.NewRequest(&podiumv1.ListNodesRequest{}))
		if err != nil {
			return fmt.Errorf("upgrade: wait for drain: %w", err)
		}
		running := int32(-1)
		for _, n := range res.Msg.GetNodes() {
			if n.GetId() == nodeID {
				running = n.GetRunningTasks()
			}
		}
		switch {
		case running < 0:
			return fmt.Errorf("upgrade: the control plane does not know node %s any more", nodeID)
		case running == 0:
			fmt.Fprintf(out, "drained\n")
			return nil
		case time.Now().After(deadline):
			return fmt.Errorf("upgrade: %s still has %d task(s) running after %s; "+
				"nothing has been changed on this machine", nodeID, running, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// runRestart executes the restart command. It is a string rather than an argv because it is
// what an operator would type, and it is split on spaces rather than run through a shell so
// there is no quoting surprise.
func runRestart(ctx context.Context, command string) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...) //nolint:gosec // the operator's own command
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}
