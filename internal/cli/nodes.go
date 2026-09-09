package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

func newNodesCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "nodes",
		Short: "List enrolled nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := e.client.admin.ListNodes(cmd.Context(), connect.NewRequest(&podiumv1.ListNodesRequest{}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("list nodes: %w", err)}
			}
			w := newTable(e.stdout)
			fmt.Fprintln(w, "NAME\tID\tSTATUS\tLABELS\tRUNNING/MAX\tHEARTBEAT")
			overridden := 0
			for _, n := range res.Msg.GetNodes() {
				labels := "-"
				if len(n.GetLabels()) > 0 {
					labels = strings.Join(n.GetLabels(), ",")
				}
				status := nodeStatusName(n.GetStatus())
				if n.GetDraining() && n.GetStatus() != podiumv1.NodeStatus_NODE_STATUS_DRAINING {
					// Draining is a standing instruction, so it applies to a node that is
					// offline right now too — and an operator needs to see that before
					// they wonder why it takes no work when it comes back.
					status += " (draining)"
				}
				// MAX is the budget in force, and an override is marked: a machine
				// configured for 4 and capped at 2 must not read as a machine with 2 slots.
				slots := fmt.Sprintf("%d/%d", n.GetRunningTasks(), n.GetCapacity().GetMaxTasks())
				if n.MaxTasksOverride != nil {
					slots = fmt.Sprintf("%d/%d*", n.GetRunningTasks(), n.GetMaxTasksOverride())
					overridden++
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					n.GetName(), n.GetId(), status, labels, slots, heartbeatAge(n))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if overridden > 0 {
				e.note("* slot count set from the control plane, not by the node's own max_tasks " +
					"(`podium node slots NODE 0` hands it back)")
			}
			return nil
		},
	}
}

func heartbeatAge(n *podiumv1.Node) string {
	if n.GetLastHeartbeatAt() == nil {
		return "never"
	}
	return ago(n.GetLastHeartbeatAt().AsTime())
}

func newNodeCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Node administration",
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newEnrollTokenCommand(e),
		newRekeyCommand(e),
		newDrainCommand(e),
		newUndrainCommand(e),
		newNodeSlotsCommand(e),
		newNodeRemoveCommand(e),
	)
	return cmd
}

// resolveNode turns whatever the operator typed — a node ID or a node name — into an ID.
// The step's CLI is documented in terms of names because that is what an operator reads off
// `podium nodes`, and making them copy a ULID to drain a machine would be unkind.
func resolveNode(ctx context.Context, e *env, nameOrID string) (*podiumv1.Node, error) {
	res, err := e.client.admin.ListNodes(ctx, connect.NewRequest(&podiumv1.ListNodesRequest{}))
	if err != nil {
		return nil, &ExitError{Code: ExitInfra, Err: fmt.Errorf("list nodes: %w", err)}
	}
	var byName []*podiumv1.Node
	for _, n := range res.Msg.GetNodes() {
		if n.GetId() == nameOrID {
			return n, nil
		}
		if n.GetName() == nameOrID {
			byName = append(byName, n)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return nil, &ExitError{Code: ExitUsage, Err: fmt.Errorf("no node called %q", nameOrID)}
	default:
		ids := make([]string, 0, len(byName))
		for _, n := range byName {
			ids = append(ids, n.GetId())
		}
		return nil, &ExitError{Code: ExitUsage, Err: fmt.Errorf(
			"%d nodes are called %q (%s); name one by its ID", len(byName), nameOrID, strings.Join(ids, ", "))}
	}
}

func newDrainCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "drain NODE",
		Short: "Stop giving a node new work",
		Long: "Stop giving a node new work.\n\n" +
			"Whatever the node is already running finishes normally — a drain takes a machine\n" +
			"out of service without killing the jobs on it. The instruction is stored, so it\n" +
			"survives both daemons restarting and applies to a node that is offline right now.\n\n" +
			"A node started with --exit-on-drain exits 0 once its last task finishes, which is\n" +
			"how an upgrade replaces the binary. Without that flag it stays connected and idle\n" +
			"until `podium node undrain`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			node, err := resolveNode(cmd.Context(), e, args[0])
			if err != nil {
				return err
			}
			res, err := e.client.admin.DrainNode(cmd.Context(),
				connect.NewRequest(&podiumv1.DrainNodeRequest{NodeId: node.GetId()}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("drain node: %w", err)}
			}
			n := res.Msg.GetNode()
			fmt.Fprintf(e.stdout, "%s (%s) draining; %d task(s) still running\n",
				n.GetName(), n.GetId(), n.GetRunningTasks())
			return nil
		},
	}
}

func newUndrainCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "undrain NODE",
		Short: "Put a drained node back in the pool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			node, err := resolveNode(cmd.Context(), e, args[0])
			if err != nil {
				return err
			}
			res, err := e.client.admin.UndrainNode(cmd.Context(),
				connect.NewRequest(&podiumv1.UndrainNodeRequest{NodeId: node.GetId()}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("undrain node: %w", err)}
			}
			n := res.Msg.GetNode()
			fmt.Fprintf(e.stdout, "%s (%s) accepting work again\n", n.GetName(), n.GetId())
			return nil
		},
	}
}

func newNodeSlotsCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "slots NODE COUNT",
		Short: "Change how many tasks a node runs at once",
		Long: "Change how many tasks a node runs at once.\n\n" +
			"COUNT overrides the max_tasks in the node's own configuration, in both\n" +
			"directions. `0` clears the override and hands the node back to its file.\n\n" +
			"The number is stored against the node, so it survives both daemons restarting\n" +
			"and can be set on a node that is offline right now — it reads it when it\n" +
			"reconnects. Lowering it below what the node is already running takes nothing\n" +
			"down: those tasks finish, and the node accepts no more until enough have.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			count, err := strconv.ParseInt(args[1], 10, 32)
			if err != nil {
				return &ExitError{Code: ExitUsage, Err: fmt.Errorf("COUNT %q is not a number", args[1])}
			}
			node, err := resolveNode(cmd.Context(), e, args[0])
			if err != nil {
				return err
			}
			res, err := e.client.admin.SetNodeSlots(cmd.Context(), connect.NewRequest(
				&podiumv1.SetNodeSlotsRequest{NodeId: node.GetId(), MaxTasks: int32(count)}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("set node slots: %w", err)}
			}
			n := res.Msg.GetNode()
			if n.MaxTasksOverride == nil {
				fmt.Fprintf(e.stdout, "%s (%s) back on its own max_tasks of %d\n",
					n.GetName(), n.GetId(), n.GetCapacity().GetMaxTasks())
				return nil
			}
			fmt.Fprintf(e.stdout, "%s (%s) runs %d task(s) at once; %d running now\n",
				n.GetName(), n.GetId(), n.GetMaxTasksOverride(), n.GetRunningTasks())
			return nil
		},
	}
}

func newNodeRemoveCommand(e *env) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "rm NODE",
		Aliases: []string{"remove"},
		Short:   "Forget a node",
		Long: "Forget a node.\n\n" +
			"The node must not be running anything, and must be drained or gone: drain it\n" +
			"first (`podium node drain NODE`) or pass --force. Removing a node does not stop\n" +
			"its daemon — a node whose identity is still on disk re-enrolls as a new one only\n" +
			"after its identity.json is deleted too.\n\n" +
			"Finished tasks keep the node ID they ran on, so the history stays readable.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			node, err := resolveNode(cmd.Context(), e, args[0])
			if err != nil {
				return err
			}
			if _, err := e.client.admin.DeleteNode(cmd.Context(),
				connect.NewRequest(&podiumv1.DeleteNodeRequest{NodeId: node.GetId(), Force: force})); err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("delete node: %w", err)}
			}
			fmt.Fprintf(e.stdout, "%s (%s) removed\n", node.GetName(), node.GetId())
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove a node that is still online and not draining")
	return cmd
}

func newRekeyCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "rekey NODE_ID",
		Short: "Unbind a node from the Tailscale device it enrolled from",
		Long: "Unbind a node from the Tailscale device it enrolled from.\n\n" +
			"A node enrolled over the tailnet is pinned to the Tailscale device it enrolled\n" +
			"from, so a copied identity.json is useless on another machine. Rekey when the\n" +
			"worker is genuinely rebuilt or replaced: the node keeps its ID, labels and\n" +
			"history, and the next Hello binds it to whatever device it arrives from.\n\n" +
			"Until it reconnects the node key alone is enough, so rekey immediately before\n" +
			"the move, not as a matter of routine.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := e.client.admin.RekeyNode(cmd.Context(),
				connect.NewRequest(&podiumv1.RekeyNodeRequest{NodeId: args[0]}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("rekey node: %w", err)}
			}
			n := res.Msg.GetNode()
			e.note("node %s (%s) is unbound; the next connection binds it to that device", n.GetId(), n.GetName())
			return nil
		},
	}
}

func newEnrollTokenCommand(e *env) *cobra.Command {
	var labels []string
	var ttl time.Duration

	cmd := &cobra.Command{
		Use:   "enroll-token",
		Short: "Mint a single-use node enrollment token",
		Long: "Mint a single-use node enrollment token.\n\n" +
			"The token is printed to stdout and nothing else is, so it can be captured\n" +
			"directly: TOKEN=$(podium node enroll-token --label linux/arm64).\n" +
			"It is shown once and never stored in plaintext by the server.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &podiumv1.CreateEnrollmentTokenRequest{Labels: labels}
			if ttl > 0 {
				req.Ttl = durationpb.New(ttl)
			}
			res, err := e.client.admin.CreateEnrollmentToken(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("create enrollment token: %w", err)}
			}
			fmt.Fprintln(e.stdout, res.Msg.GetToken())
			e.note("expires %s", res.Msg.GetExpiresAt().AsTime().Local().Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&labels, "label", nil, "label to give the enrolling node (repeatable)")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "how long the token stays valid (default 1h)")
	return cmd
}
