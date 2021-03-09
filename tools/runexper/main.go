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

	experFlavorCPULoad      = "cpu-load"
	experFlavorCPULoadCtnrs = "cpu-load-ctnrs"
	experFlavorLatency      = "latency"

	connperfServerCmd   = "sudo GOMAXPROCS=4 taskset -a -c 0,3 ./connperf serve -l 0.0.0.0:9100"
	connperfClientCmd   = "sudo GOMAXPROCS=4 taskset -a -c 0,3 ./connperf connect %s --show-only-results 10.0.150.2:9100"
	spawnCtnrServerCmd1 = "./spawnctnr -flavor server -containers %d"
	spawnCtnrClientCmd1 = "./connperf connect %s --show-only-results $(curl -sS http://10.0.150.2:8080/hostports)"
	runTracerCmd        = "sudo GOMAXPROCS=1 taskset -a -c 4,5 ./runtracer -method all"
	killSpawnCtnrCmd    = "sudo pkill -INT spawnctnr"
)

var (
	experFlavor string
	protocol    string
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&experFlavor, "exper-flavor", experFlavorCPULoad, "experiment flavor")
	flag.StringVar(&protocol, "protocol", "all", "protocol (tcp or udp)")
	flag.Parse()
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
	w := bufio.NewWriter(os.Stderr)
	s := bufio.NewScanner(in)
	for s.Scan() {
		fmt.Fprintln(w, "\t", host, "-->", s.Text())
	}
	w.Flush()
}

func runTracer(ctx context.Context, period time.Duration) error {
	tracerCmd := runTracerCmd + " " + fmt.Sprintf("-period %s", period)

	var wg sync.WaitGroup

	cmd3, out3, err := sshServerCmd(ctx, tracerCmd)
	if err != nil {
		return err
	}
	defer cmd3.Process.Signal(os.Interrupt)
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitCmd(cmd3, defaultServerHost, tracerCmd)
	}()
	go printCmdOut(out3, defaultServerHost)

	cmd4, out4, err := sshClientCmd(ctx, tracerCmd)
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
	return nil
}

func runCPULoadEach(ctx context.Context, connperfClientFlag string) error {
	wg := sync.WaitGroup{}
	wg.Add(1)
	cmd1, out1, err := sshServerCmd(ctx, connperfServerCmd)
	if err != nil {
		return err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd1, defaultServerHost, connperfServerCmd)
	}()
	go printCmdOut(out1, defaultServerHost)

	// wait server
	time.Sleep(5 * time.Second)

	wg.Add(1)
	clientCmd := fmt.Sprintf(connperfClientCmd, connperfClientFlag)
	cmd2, out2, err := sshClientCmd(ctx, clientCmd)
	if err != nil {
		return err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd2, defaultClientHost, clientCmd)
	}()
	go printCmdOut(out2, defaultClientHost)

	cleanup := func() {
		sshServerCmd(ctx, killSpawnCtnrCmd)
		cmd2.Process.Signal(os.Interrupt)
	}

	// wait client
	time.Sleep(5 * time.Second)

	if err := runTracer(ctx, 10*time.Second); err != nil {
		cleanup()
		return err
	}

	cleanup()
	wg.Wait()

	return nil
}

func runCPULoad(ctx context.Context) error {
	if protocol == "all" || protocol == "tcp" {
		// tcp
		// - ephemeral
		for _, rate := range []int{5000, 10000, 15000, 20000} {
			flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			if err := runCPULoadEach(ctx, flag); err != nil {
				return err
			}
		}
		// tcp
		// - persistent
		for _, conns := range []int{5000, 10000, 15000, 20000} {
			flag := fmt.Sprintf("--proto tcp --flavor persistent --connections %d --duration 1200s", conns)
			log.Println("parameter", flag)
			if err := runCPULoadEach(ctx, flag); err != nil {
				return err
			}
		}
	}
	if protocol == "all" || protocol == "udp" {
		// udp
		for _, rate := range []int{5000, 10000, 15000, 20000} {
			flag := fmt.Sprintf("--proto udp --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			if err := runCPULoadEach(ctx, flag); err != nil {
				return err
			}
		}
	}

	return nil
}

func runCPULoadCtnrsEach(ctx context.Context, containers int, connperfClientFlag string) error {
	var wg sync.WaitGroup
	spawnCtnrServerCmd := fmt.Sprintf(spawnCtnrServerCmd1, containers)
	wg.Add(1)
	cmd1, out1, err := sshServerCmd(ctx, spawnCtnrServerCmd)
	if err != nil {
		return err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd1, defaultServerHost, spawnCtnrServerCmd)
	}()
	go printCmdOut(out1, defaultServerHost)

	// wait server
	time.Sleep(5*time.Second + time.Duration(100*containers)*time.Millisecond)

	wg.Add(1)
	clientCmd := fmt.Sprintf(spawnCtnrClientCmd1, connperfClientFlag)
	cmd2, out2, err := sshClientCmd(ctx, clientCmd)
	if err != nil {
		return err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd2, defaultClientHost, clientCmd)
	}()
	go printCmdOut(out2, defaultClientHost)

	cleanup := func() {
		sshServerCmd(ctx, killSpawnCtnrCmd)
		cmd2.Process.Signal(os.Interrupt)
	}

	// wait client
	time.Sleep(10 * time.Second)

	if err := runTracer(ctx, 10*time.Second); err != nil {
		cleanup()
		return err
	}

	cleanup()
	wg.Wait()

	return nil
}

func runCPULoadCtnrs(ctx context.Context) error {
	if protocol == "all" || protocol == "tcp" {
		variants := []int{1, 50, 100, 150, 200}
		// tcp
		// - ephemeral
		for _, containers := range variants {
			rate := 10000 / containers
			flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			if err := runCPULoadCtnrsEach(ctx, containers, flag); err != nil {
				return err
			}
		}
		// udp
		for _, containers := range variants {
			rate := 10000 / containers
			flag := fmt.Sprintf("--proto udp --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			if err := runCPULoadCtnrsEach(ctx, containers, flag); err != nil {
				return err
			}
		}
	}
	return nil
}

func runLatencyWithoutTracer(ctx context.Context, connperfClientFlag string) error {
	var wg sync.WaitGroup

	cmd3, out3, err := sshServerCmd(ctx, connperfServerCmd)
	if err != nil {
		return err
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		waitCmd(cmd3, defaultServerHost, connperfServerCmd)
	}()
	go printCmdOut(out3, defaultServerHost)

	// wait server
	time.Sleep(5 * time.Second)

	clientCmd := fmt.Sprintf(connperfClientCmd, connperfClientFlag)
	cmd4, out4, err := sshClientCmd(ctx, clientCmd)
	if err != nil {
		return err
	}
	defer cmd4.Process.Signal(os.Interrupt)

	wg.Add(1)
	go func() {
		defer wg.Done()
		waitCmd(cmd4, defaultClientHost, clientCmd)
		cmd3.Process.Signal(os.Interrupt) // server kill
	}()
	go printCmdOut(out4, defaultClientHost)

	// wait until connperf server and client have finished
	wg.Wait()

	return nil
}

func prepareTracer(ctx context.Context, method string) (chan struct{}, chan struct{}, *os.Process, *os.Process, error) {
	runTracerCmd := runTracerCmd + " -period 1200s" + " -method " + method
	wait1 := make(chan struct{})
	cmd1, out1, err := sshServerCmd(ctx, runTracerCmd)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	go func() {
		waitCmd(cmd1, defaultServerHost, runTracerCmd)
		wait1 <- struct{}{}
	}()
	go printCmdOut(out1, defaultServerHost)

	wait2 := make(chan struct{})
	cmd2, out2, err := sshClientCmd(ctx, runTracerCmd)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	go func() {
		waitCmd(cmd2, defaultClientHost, runTracerCmd)
		wait2 <- struct{}{}
	}()
	go printCmdOut(out2, defaultClientHost)

	return wait1, wait2, cmd1.Process, cmd2.Process, nil
}

func runLatencyEach(ctx context.Context, connperfClientFlag string) error {
	for _, method := range []string{"snapshot-polling", "user-aggregation", "kernel-aggregation"} {
		wait1, wait2, proc1, proc2, err := prepareTracer(ctx, method)
		if err != nil {
			return err
		}

		if err := runLatencyWithoutTracer(ctx, connperfClientFlag); err != nil {
			return err
		}

		proc1.Signal(os.Interrupt)
		proc2.Signal(os.Interrupt)
		select {
		case <-wait1:
		case <-wait2:
		default:
		}
	}
	return nil
}

func runLatency(ctx context.Context) error {
	// TODO: no-runtracer

	if protocol == "all" || protocol == "tcp" {
		// - ephemeral
		for _, rate := range []int{5000, 10000, 15000, 20000} {
			flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 10s", rate)
			log.Println("parameter", flag)
			if err := runLatencyWithoutTracer(ctx, flag); err != nil {
				return err
			}
			if err := runLatencyEach(ctx, flag); err != nil {
				return err
			}
		}
	}
	if protocol == "all" || protocol == "udp" {
		for _, rate := range []int{5000, 10000, 15000, 20000} {
			flag := fmt.Sprintf("--proto udp --rate %d --duration 10s", rate)
			log.Println("parameter", flag)
			if err := runLatencyWithoutTracer(ctx, flag); err != nil {
				return err
			}
			if err := runLatencyEach(ctx, flag); err != nil {
				return err
			}
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
	case experFlavorCPULoadCtnrs:
		err = runCPULoadCtnrs(ctx)
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
