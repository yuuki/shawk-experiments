package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"golang.org/x/xerrors"
)

const (
	exitCodeOk  = 0
	exitCodeErr = -1

	defaultFlavor     = "server"
	defaultPeriod     = 60 * time.Second
	defaultContainers = 10

	connperfImage    = "docker.pkg.github.com/yuuki/connperf/connperf:latest"
	defaultClientCmd = "connect --proto tcp --type ephemeral --rate 1000"
	defaultServerCmd = "serve -l 0.0.0.0:9100"
)

var (
	flavor     string
	period     time.Duration
	containers int
	clientCmd  string
	serverCmd  string
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&flavor, "flavor", defaultFlavor, "flavor ('client', 'server')")
	flag.DurationVar(&period, "period", defaultPeriod, "period")
	flag.IntVar(&containers, "containers", defaultContainers, "the number of containers")
	flag.StringVar(&clientCmd, "client-cmd", defaultClientCmd, "connperf client command line option")
	flag.StringVar(&serverCmd, "server-cmd", defaultServerCmd, "connperf server command line option")
	flag.Parse()
}

func run() int {
	switch flavor {
	case "client":
	case "server":
	default:
		log.Printf("%q is unexpected flavor\n", flavor)
		return exitCodeErr
	}

	if err := spawnContainers(flavor); err != nil {
		log.Printf("%v\n", err)
		return exitCodeErr
	}

	return exitCodeOk
}

func spawnContainers(flavor string) error {
	cli, err := client.NewEnvClient()
	if err != nil {
		xerrors.Errorf("could not create docker client: %w", err)
	}

	ctx := context.Background()

	if _, err := cli.Ping(ctx); err != nil {
		return xerrors.Errorf("could not ping docker: %w", err)
	}

	log.Printf("--> Pulling %q\n", connperfImage)
	if _, err = cli.ImagePull(ctx, connperfImage, types.ImagePullOptions{}); err != nil {
		return xerrors.Errorf("could not pull %q: %w", connperfImage, err)
	}

	log.Printf("--> Spawning '%d' containers\n", containers)

	for i := 0; i < containers; i++ {
		if err := spawn(ctx, cli, flavor); err != nil {
			return err
		}
	}

	return nil
}

func spawn(ctx context.Context, cli *client.Client, flavor string) error {
	var cmd []string
	switch flavor {
	case "client":
		cmd = strings.Split(clientCmd, " ")
	case "server":
		cmd = strings.Split(serverCmd, " ")
	}
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: connperfImage,
		Cmd:   cmd,
		Tty:   false,
	}, nil, nil, nil, "")
	err = cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return xerrors.Errorf("failed to start container: %w", err)
	}
	defer func() {
		err = cli.ContainerRemove(ctx, resp.ID, types.ContainerRemoveOptions{
			Force: true,
		})
		if err != nil {
			log.Printf("failed to remove container: %s\n", err)
		}
	}()

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return xerrors.Errorf("failed to wait container: %w", err)
		}
	case <-statusCh:
	}

	return nil
}

func main() {
	os.Exit(run())
}
