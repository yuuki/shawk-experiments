package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"golang.org/x/sync/errgroup"
	"golang.org/x/xerrors"
)

const (
	exitCodeOk  = 0
	exitCodeErr = -1

	defaultFlavor     = "server"
	defaultPeriod     = 60 * time.Second
	defaultContainers = 10

	connperfImage    = "ghcr.io/yuuki/connperf:latest"
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

	sig := make(chan os.Signal, 1)
	defer close(sig)
	signal.Notify(sig, os.Interrupt, os.Kill)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ret := <-sig
		log.Printf("Received %v, Goodbye\n", ret)
		cancel()
	}()

	if err := spawnContainers(ctx); err != nil {
		log.Printf("%v\n", err)
		return exitCodeErr
	}

	return exitCodeOk
}

func spawnContainers(ctx context.Context) error {
	cli, err := client.NewEnvClient()
	if err != nil {
		xerrors.Errorf("could not create docker client: %w", err)
	}

	if _, err := cli.Ping(ctx); err != nil {
		return xerrors.Errorf("could not ping docker: %w", err)
	}

	log.Printf("--> Pulling %q\n", connperfImage)
	reader, err := cli.ImagePull(ctx, connperfImage, types.ImagePullOptions{})
	if err != nil {
		return xerrors.Errorf("could not pull %q: %w", connperfImage, err)
	}
	io.Copy(os.Stdout, reader)

	log.Printf("--> Spawning '%d' containers\n", containers)

	eg, ctx := errgroup.WithContext(ctx)
	for i := 0; i < containers; i++ {
		i := i
		eg.Go(func() error {
			return spawn(ctx, cli, i)
		})
	}

	return eg.Wait()
}

func spawn(ctx context.Context, cli *client.Client, i int) error {
	var cmd []string
	switch flavor {
	case "client":
		cmd = strings.Split(clientCmd, " ")
	case "server":
		cmd = strings.Split(serverCmd, " ")
	}

	tcp, _ := nat.NewPort("tcp", "9100")
	udp, _ := nat.NewPort("udp", "9100")
	portSet := nat.PortSet{tcp: struct{}{}, udp: struct{}{}}
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:        connperfImage,
		Cmd:          cmd,
		Tty:          false,
		ExposedPorts: portSet,
	}, &container.HostConfig{
		PortBindings: nat.PortMap{
			tcp: []nat.PortBinding{
				{HostIP: "0.0.0.0"},
			},
			udp: []nat.PortBinding{
				{HostIP: "0.0.0.0"},
			},
		},
	}, nil, nil, fmt.Sprintf("connperf-%s-%04d", flavor, i))
	if err != nil {
		return xerrors.Errorf("failed to create container: %w", err)
	}

	defer func() {
		err = cli.ContainerRemove(context.Background(), resp.ID, types.ContainerRemoveOptions{
			Force: true,
		})
		if err != nil {
			log.Printf("failed to remove container: %s\n", err)
		}
	}()

	err = cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{})
	if err != nil {
		return xerrors.Errorf("failed to start container: %w", err)
	}

	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			if errors.Is(err, context.Canceled) {
				break
			}
			return xerrors.Errorf("failed to wait container: %w", err)
		}
	case <-statusCh:
	}

	return nil
}

func main() {
	os.Exit(run())
}
