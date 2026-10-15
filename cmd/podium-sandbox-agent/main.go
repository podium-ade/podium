// Command podium-sandbox-agent runs one Podium task inside a cloud sandbox. podium-node
// uploads it, starts it detached, and drives it over HTTP through the provider's exposed
// port. See internal/sandboxagent.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandboxagent"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
	"github.com/podium-ade/podium/internal/version"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:"+strconv.Itoa(wire.AgentPort), "address to serve on")
	tokenFile := flag.String("token-file", "", "file holding the run's bearer token; read once and deleted")
	dir := flag.String("dir", "/tmp/podium-agent", "where the journal and staged transfers live")
	secretDir := flag.String("secret-dir", "/run/podium-agent", "the executor's state, secrets included")
	tmpfs := flag.Bool("tmpfs", false, "mount a tmpfs on -secret-dir with sudo so secrets never reach the disk")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(*listen, *tokenFile, *dir, *secretDir, *tmpfs, logger); err != nil {
		logger.Error("podium-sandbox-agent", "error", err)
		os.Exit(1)
	}
}

func run(listen, tokenFile, dir, secretDir string, tmpfs bool, logger *slog.Logger) error {
	raw, err := os.ReadFile(tokenFile) //nolint:gosec // the node names the file it wrote
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	_ = os.Remove(tokenFile)
	token := strings.TrimSpace(string(raw))

	if tmpfs {
		if err := mountTmpfs(secretDir); err != nil {
			return err
		}
	} else if err := os.MkdirAll(secretDir, 0o700); err != nil {
		return err
	}

	factory := func(ctx context.Context, name string) (sandboxagent.Strategy, error) {
		switch name {
		case wire.StrategyDocker:
			return docker.New(ctx, docker.Options{DataDir: filepath.Join(secretDir, "docker"), Logger: logger})
		case wire.StrategyProcess:
			return newProcess(ctx, filepath.Join(secretDir, "process"), logger)
		default:
			return nil, fmt.Errorf("unknown strategy %q", name)
		}
	}
	srv, err := sandboxagent.New(token, dir, factory, logger)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	logger.Info("podium-sandbox-agent listening", "addr", ln.Addr().String(), "version", version.String())
	return http.Serve(ln, srv.Handler()) //nolint:gosec // every route is behind the bearer token
}

func mountTmpfs(dir string) error {
	opts := fmt.Sprintf("size=512m,mode=0700,uid=%d,gid=%d", os.Getuid(), os.Getgid())
	if os.Getuid() == 0 {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		return execRun("mount", "-t", "tmpfs", "-o", opts, "tmpfs", dir)
	}
	if err := execRun("sudo", "-n", "mkdir", "-p", dir); err != nil {
		return err
	}
	return execRun("sudo", "-n", "mount", "-t", "tmpfs", "-o", opts, "tmpfs", dir)
}

func execRun(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput() //nolint:gosec // fixed commands
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// taskUser is the unprivileged account a process task runs as, when the agent can make one.
const taskUser = "podium"

// newProcess builds the process strategy. With root, or sudo, the task gets an account of
// its own with no sudo, no docker group and no way to read the provider's credentials;
// without either it runs as the agent's own user, which is all such a sandbox offers.
func newProcess(ctx context.Context, dir string, logger *slog.Logger) (sandboxagent.Strategy, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	o := docker.ProcessOptions{DataDir: dir, Logger: logger}
	root := os.Getuid() == 0
	o.Sudo = !root && exec.Command("sudo", "-n", "true").Run() == nil
	if root || o.Sudo {
		if exec.Command("id", "-u", taskUser).Run() != nil {
			args := []string{"useradd", "-m", "-s", "/bin/sh", taskUser}
			if o.Sudo {
				args = append([]string{"sudo", "-n"}, args...)
			}
			if err := execRun(args[0], args[1:]...); err != nil {
				return nil, err
			}
		}
		o.User = taskUser
	}
	if exec.Command("docker", "info").Run() == nil {
		d, err := docker.New(ctx, docker.Options{DataDir: filepath.Join(dir, "docker"), Logger: logger})
		if err != nil {
			return nil, err
		}
		o.Sidecars = d
	}
	return docker.NewProcess(o)
}
