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
	connperfClientCmd = "sudo GOMAXPROCS=4 taskset -a -c 0,3 ./connperf connect %s --show-only-results 10.0.150.2:9100"
	runTracerCmd      = "sudo GOMAXPROCS=1 taskset -a -c 4,5 ./runtracer -period 10s -method all"
)

var (
	experFlavor string
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&experFlavor, "-exper-flavor", experFlavorCPULoad, "experiment flavor")
}

func sshCmd(ctx context.Context, host string, cmd string) (*exec.Cmd, io.ReadCloser, error) {
	sshCmd := strings.Fields(
		fmt.Sprintf("ssh -tt %s@%s", defaultHostUser, host),
	)
	sshCmd = append(sshCmd, cmd)
	c := exec.CommandContext(ctx, sshCmd[0], sshCmd[1:]...)
	log.Println(c.Args)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := c.Start(); err != nil {
		return nil, nil, err
	}
	return c, stdout, nil
}

func sshClientCmd(ctx context.Context, cmd string) (*exec.Cmd, io.ReadCloser, error) {
	return sshCmd(ctx, defaultClientHost, cmd)
}

func sshServerCmd(ctx context.Context, cmd string) (*exec.Cmd, io.ReadCloser, error) {
	return sshCmd(ctx, defaultServerHost, cmd)
}

func waitCmd(c *exec.Cmd, host, cmd string) {
	if err := c.Wait(); err != nil {
		eerr := &exec.ExitError{}
		if !errors.As(err, &eerr) {
			log.Printf("wait command error: %q, %s:%s \n", err, host, cmd)
		}
	}
}

func printCmdOut(in io.Reader, host string) {
	w := bufio.NewWriter(os.Stdout)
	s := bufio.NewScanner(in)
	for s.Scan() {
		fmt.Fprintln(w, "\t", host, "-->", s.Text())
	}
	w.Flush()
}

func runCPULoadEach(ctx context.Context, connperfClientFlag string) error {
	wait1 := make(chan struct{})
	cmd1, out1, err := sshServerCmd(ctx, connperfServerCmd)
	if err != nil {
		return err
	}
	go func() {
		waitCmd(cmd1, defaultServerHost, connperfServerCmd)
		wait1 <- struct{}{}
	}()
	go printCmdOut(out1, defaultServerHost)

	// wait server
	time.Sleep(1 * time.Second)

	wait2 := make(chan struct{})
	clientCmd := fmt.Sprintf(connperfClientCmd, connperfClientFlag)
	cmd2, out2, err := sshClientCmd(ctx, clientCmd)
	if err != nil {
		return err
	}
	go func() {
		waitCmd(cmd2, defaultClientHost, clientCmd)
		wait2 <- struct{}{}
	}()
	go printCmdOut(out2, defaultClientHost)

	// wait client
	time.Sleep(1 * time.Second)

	var wg sync.WaitGroup

	cmd3, out3, err := sshServerCmd(ctx, runTracerCmd)
	if err != nil {
		return err
	}
	defer cmd3.Process.Signal(os.Interrupt)
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitCmd(cmd3, defaultServerHost, runTracerCmd)
	}()
	go printCmdOut(out3, defaultServerHost)

	cmd4, out4, err := sshClientCmd(ctx, runTracerCmd)
	if err != nil {
		return err
	}
	defer cmd4.Process.Signal(os.Interrupt)
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitCmd(cmd4, defaultClientHost, runTracerCmd)
	}()
	go printCmdOut(out4, defaultClientHost)

	// wait until tracer has finished
	wg.Wait()

	cmd1.Process.Signal(os.Interrupt)
	cmd2.Process.Signal(os.Interrupt)
	select {
	case <-wait1:
	case <-wait2:
	default:
	}

	return nil
}

func runCPULoad(ctx context.Context) error {
	// tcp
	// - ephemeral
	for _, rate := range []int{5000, 10000, 15000, 20000} {
		flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 30s", rate)
		log.Println("parameter", flag)
		if err := runCPULoadEach(ctx, flag); err != nil {
			return err
		}
	}
	// tcp
	// - persistent
	for _, conns := range []int{5000, 10000, 15000, 20000} {
		flag := fmt.Sprintf("--proto tcp --flavor persistent --connections %d --duration 30s", conns)
		log.Println("parameter", flag)
		if err := runCPULoadEach(ctx, flag); err != nil {
			return err
		}
	}

	// udp
	for _, rate := range []int{5000, 10000, 15000, 20000} {
		flag := fmt.Sprintf("--proto udp --rate %d", rate)
		log.Println("parameter", flag)
		if err := runCPULoadEach(ctx, flag); err != nil {
			return err
		}
	}

	return nil
}

func runLatencyEach(ctx context.Context, connperfClientFlag string) error {
	wait1 := make(chan struct{})
	cmd1, out1, err := sshServerCmd(ctx, runTracerCmd)
	if err != nil {
		return err
	}
	defer cmd1.Process.Signal(os.Interrupt)
	go func() {
		waitCmd(cmd1, defaultServerHost, runTracerCmd)
		wait1 <- struct{}{}
	}()
	go printCmdOut(out1, defaultServerHost)

	wait2 := make(chan struct{})
	cmd2, out2, err := sshClientCmd(ctx, runTracerCmd)
	if err != nil {
		return err
	}
	defer cmd2.Process.Signal(os.Interrupt)
	go func() {
		waitCmd(cmd2, defaultClientHost, runTracerCmd)
		wait2 <- struct{}{}
	}()
	go printCmdOut(out2, defaultClientHost)

	var wg sync.WaitGroup

	cmd3, out3, err := sshServerCmd(ctx, connperfServerCmd)
	if err != nil {
		return err
	}
	go waitCmd(cmd3, defaultServerHost, connperfServerCmd)
	go printCmdOut(out3, defaultServerHost)

	// wait server
	time.Sleep(1 * time.Second)

	clientCmd := fmt.Sprintf(connperfClientCmd, connperfClientFlag)
	cmd4, out4, err := sshClientCmd(ctx, clientCmd)
	if err != nil {
		return err
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitCmd(cmd4, defaultClientHost, clientCmd)
	}()
	go printCmdOut(out4, defaultClientHost)

	// wait until tracer has finished
	wg.Wait()

	cmd1.Process.Signal(os.Interrupt)
	cmd2.Process.Signal(os.Interrupt)
	select {
	case <-wait1:
	case <-wait2:
	default:
	}

	return nil
}

func runLatency(ctx context.Context) error {
	// tcp
	// - ephemeral
	for _, rate := range []int{5000, 10000, 15000, 20000} {
		flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 10s", rate)
		log.Println("parameter", flag)
		if err := runCPULoadEach(ctx, flag); err != nil {
			return err
		}
	}

	// udp
	for _, rate := range []int{5000, 10000, 15000, 20000} {
		flag := fmt.Sprintf("--proto udp --rate %d --duration 10s", rate)
		log.Println("parameter", flag)
		if err := runCPULoadEach(ctx, flag); err != nil {
			return err
		}
	}

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

	log.Printf("Running with a flavor of %q\n", experFlavor)

	var err error
	switch experFlavor {
	case experFlavorCPULoad:
		err = runCPULoad(ctx)
	case experFlavorLatency:
		err = runLatency(ctx)
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
