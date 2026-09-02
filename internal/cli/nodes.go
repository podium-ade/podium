package cli

import (
	"fmt"
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
			for _, n := range res.Msg.GetNodes() {
				labels := "-"
				if len(n.GetLabels()) > 0 {
					labels = strings.Join(n.GetLabels(), ",")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%d\t%s\n",
					n.GetName(), n.GetId(), nodeStatusName(n.GetStatus()), labels,
					n.GetRunningTasks(), n.GetCapacity().GetMaxTasks(),
					heartbeatAge(n))
			}
			return w.Flush()
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
	cmd.AddCommand(newEnrollTokenCommand(e))
	return cmd
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
