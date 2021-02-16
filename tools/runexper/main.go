package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"
)

const (
	exitCodeOk  = 0
	exitCodeErr = -1

	defaultClientHost = "10.0.150.1"
	defaultServerHost = "10.0.150.2"
	defaultHostUser   = "ubuntu"

	experFlavorCPULoad = "cpu-load"
	experFlavorLatency = "latency"

	connperfServerCmd = "sudo GOMAXPROCS=4 taskset -a -c 0,3 ./connperf serve -l 0.0.0.0:9100"
	connperfClientCmd = "sudo GOMAXPROCS=4 taskset -a -c 0,3 ./connperf connect —-proto tcp —-flavor ephemeral —-rate 1000 —-duration 60s 10.0.150.2:9100"
	runTracerCmd      = "sudo GOMAXPROCS=1 taskset -a -c 4,5 ./runtracer -period 10s -method all"
)

var (
	experFlavor string
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&experFlavor, "-exper-flavor", experFlavorCPULoad, "experiment flavor")
}

func sshCmd(ctx context.Context, host string, args []string) (*exec.Cmd, io.ReadCloser, error) {
	if len(args) < 1 {
		return nil, nil, errors.New("args length should be > 0")
	}
	host = defaultHostUser + "@" + host
	cmd := exec.CommandContext(ctx, "ssh",
		append([]string{"-t", host}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, stdout, nil
}

func sshClientCmd(ctx context.Context, args []string) (*exec.Cmd, io.ReadCloser, error) {
	return sshCmd(ctx, defaultClientHost, args)
}

func sshServerCmd(ctx context.Context, args []string) (*exec.Cmd, io.ReadCloser, error) {
	return sshCmd(ctx, defaultServerHost, args)
}

func runCPULoad(ctx context.Context) error {
	cmd1, _, err := sshServerCmd(ctx, strings.Fields(connperfServerCmd))
	if err != nil {
		return err
	}
	defer cmd1.Process.Kill()
	go func() {
		if err := cmd1.Wait(); err != nil {
			log.Println(err)
			return
		}
	}()

	// wait server
	time.Sleep(1 * time.Second)

	cmd2, _, err := sshClientCmd(ctx, strings.Fields(connperfClientCmd))
	if err != nil {
		return err
	}
	defer cmd2.Process.Kill()
	go func() {
		if err := cmd2.Wait(); err != nil {
			log.Println(err)
			return
		}
	}()

	// wait client
	time.Sleep(1 * time.Second)

	var wg sync.WaitGroup

	cmd3, out3, err := sshServerCmd(ctx, strings.Fields(runTracerCmd))
	if err != nil {
		return err
	}
	defer cmd3.Process.Kill()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := cmd3.Wait(); err != nil {
			log.Println(err)
			return
		}
	}()
	go func() {
		s := bufio.NewScanner(out3)
		for s.Scan() {
			fmt.Println(s.Text())
		}
	}()

	cmd4, out4, err := sshClientCmd(ctx, strings.Fields(runTracerCmd))
	if err != nil {
		return err
	}
	defer cmd4.Process.Kill()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := cmd4.Wait(); err != nil {
			log.Println(err)
			return
		}
	}()
	go func() {
		s := bufio.NewScanner(out4)
		for s.Scan() {
			fmt.Println(s.Text())
		}
	}()

	// wait until tracer has finished
	wg.Wait()

	return nil
}

func run() int {
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

	var err error
	switch experFlavor {
	case experFlavorCPULoad:
		err = runCPULoad(ctx)
	case experFlavorLatency:
	default:
		log.Printf("unexpected flavor %q\n", experFlavor)
		return exitCodeErr
	}
	if err != nil {
		log.Printf("%+v", err)
		return exitCodeErr
	}
	return exitCodeOk
}

func main() {
	os.Exit(run())
}
