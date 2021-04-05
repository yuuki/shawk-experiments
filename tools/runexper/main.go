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
	spawnCtnrServerCmd1 = "./spawnctnr -flavor server -containers %d -host-network"
	spawnCtnrClientCmd1 = "./connperf connect %s --show-only-results $(curl -sS http://10.0.150.2:8080/hostports)"
	spawnCtnrServerCmd2 = connperfServerCmd
	spawnCtnrClientCmd2 = "./spawnctnr -flavor client -containers %d -host-network -client-cmd 'connect %s --show-only-results 10.0.150.2:9100'"
	runTracerCmd        = "sudo GOMAXPROCS=1 taskset -a -c 4,5 ./runtracer -method all"
	killConnperfCmd     = "sudo pkill -INT connperf"
	killSpawnCtnrCmd    = "sudo pkill -INT spawnctnr"

	pruneDocker   = "docker system prune -f"
	restartDocker = "sudo systemctl restart docker"
)

var (
	experFlavor     string
	spawnCtnrFlavor string
	protocol        string
	bpfProf         bool
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&experFlavor, "exper-flavor", experFlavorCPULoad, "experiment flavor")
	flag.StringVar(&spawnCtnrFlavor, "spawnctnr-flavor", "all", "spawnctnr flavor 'server' or 'client' or 'all")
	flag.StringVar(&protocol, "protocol", "all", "protocol (tcp or udp)")
	flag.BoolVar(&bpfProf, "bpf-profile", false, "bpf prof for conntop")
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
	if bpfProf {
		tracerCmd += " " + "-bpf-profile"
	}

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
		sshServerCmd(ctx, killConnperfCmd)
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
	variants := []int{5000, 10000, 15000, 20000}

	if protocol == "all" || protocol == "tcp" {
		// tcp
		// - ephemeral
		for _, rate := range variants {
			flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			if err := runCPULoadEach(ctx, flag); err != nil {
				return err
			}
		}
		// tcp
		// - persistent
		for _, conns := range variants {
			flag := fmt.Sprintf("--proto tcp --flavor persistent --connections %d --duration 1200s", conns)
			log.Println("parameter", flag)
			if err := runCPULoadEach(ctx, flag); err != nil {
				return err
			}
		}
	}
	if protocol == "all" || protocol == "udp" {
		// udp
		for _, rate := range variants {
			flag := fmt.Sprintf("--proto udp --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			if err := runCPULoadEach(ctx, flag); err != nil {
				return err
			}
		}
	}

	return nil
}

func runCPULoadServerCtnrsEach(ctx context.Context, containers int, connperfClientFlag string) error {
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

func runCPULoadClientCtnrsEach(ctx context.Context, containers int, connperfClientFlag string) error {
	var wg sync.WaitGroup
	wg.Add(1)
	cmd1, out1, err := sshServerCmd(ctx, spawnCtnrServerCmd2)
	if err != nil {
		return err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd1, defaultServerHost, spawnCtnrServerCmd2)
	}()
	go printCmdOut(out1, defaultServerHost)

	// wait server
	time.Sleep(5 * time.Second)

	wg.Add(1)
	spawnCtnrClientCmd := fmt.Sprintf(spawnCtnrClientCmd2, containers, connperfClientFlag)
	cmd2, out2, err := sshClientCmd(ctx, spawnCtnrClientCmd)
	if err != nil {
		return err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd2, defaultClientHost, spawnCtnrClientCmd)
	}()
	go printCmdOut(out2, defaultClientHost)

	cleanup := func() {
		sshClientCmd(ctx, killSpawnCtnrCmd) // kill client
		cmd1.Process.Signal(os.Interrupt)   // kill server
	}

	// wait client
	time.Sleep(5*time.Second + time.Duration(100*containers)*time.Millisecond)

	if err := runTracer(ctx, 10*time.Second); err != nil {
		cleanup()
		return err
	}

	cleanup()
	wg.Wait()

	return nil
}

func runCPULoadCtnrs(ctx context.Context) error {
	variants := []int{200, 400, 600, 800, 1000}
	connections := 10000

	if protocol == "all" || protocol == "tcp" {
		// tcp
		// - ephemeral
		for _, containers := range variants {
			rate := connections / containers
			flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			switch spawnCtnrFlavor {
			case "server":
				if err := runCPULoadServerCtnrsEach(ctx, containers, flag); err != nil {
					return err
				}
			case "client":
				if err := runCPULoadClientCtnrsEach(ctx, containers, flag); err != nil {
					return err
				}
			default:
			}
		}
	}
	if protocol == "all" || protocol == "udp" {
		// udp
		for _, containers := range variants {
			rate := connections / containers
			flag := fmt.Sprintf("--proto udp --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			switch spawnCtnrFlavor {
			case "server":
				if err := runCPULoadServerCtnrsEach(ctx, containers, flag); err != nil {
					return err
				}
			case "client":
				if err := runCPULoadClientCtnrsEach(ctx, containers, flag); err != nil {
					return err
				}
			default:
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

func prepareTracer(ctx context.Context, method string) (func(), *sync.WaitGroup, error) {
	runTracerCmd := runTracerCmd + " -period 1200s" + " -method " + method
	wg := sync.WaitGroup{}
	wg.Add(1)
	cmd1, out1, err := sshServerCmd(ctx, runTracerCmd)
	if err != nil {
		return func() {}, &wg, err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd1, defaultServerHost, runTracerCmd)
	}()
	go printCmdOut(out1, defaultServerHost)

	wg.Add(1)
	cmd2, out2, err := sshClientCmd(ctx, runTracerCmd)
	if err != nil {
		return func() {}, &wg, err
	}
	go func() {
		defer wg.Done()
		waitCmd(cmd2, defaultClientHost, runTracerCmd)
	}()
	go printCmdOut(out2, defaultClientHost)

	clean := func() {
		cmd1.Process.Signal(os.Interrupt)
		cmd2.Process.Signal(os.Interrupt)
	}
	return clean, &wg, nil
}

func runLatencyEach(ctx context.Context, connperfClientFlag string) error {
	for _, method := range []string{"snapshot-polling", "user-aggregation", "kernel-aggregation"} {
		clean, wg, err := prepareTracer(ctx, method)
		if err != nil {
			return err
		}

		if err := runLatencyWithoutTracer(ctx, connperfClientFlag); err != nil {
			clean()
			return err
		}

		clean()
		wg.Wait()
	}
	return nil
}

func runLatency(ctx context.Context) error {
	variants := []int{5000, 10000, 15000, 20000}

	if protocol == "all" || protocol == "tcp" {
		// - ephemeral
		for _, rate := range variants {
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
		for _, rate := range variants {
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

func cleanupDocker(ctx context.Context) error {
	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		c1, _, err := sshClientCmd(ctx, pruneDocker)
		if err != nil {
			log.Println(err)
		}
		c1.Wait()
		c2, _, err := sshClientCmd(ctx, restartDocker)
		if err != nil {
			log.Println(err)
		}
		c2.Wait()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c3, _, err := sshServerCmd(ctx, pruneDocker)
		if err != nil {
			log.Println(err)
		}
		c3.Wait()
		c4, _, err := sshServerCmd(ctx, restartDocker)
		if err != nil {
			log.Println(err)
		}
		c4.Wait()
	}()

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

	log.Printf("Running with a flavor of %q\n", experFlavor)

	var err error
	switch experFlavor {
	case experFlavorCPULoad:
		err = runCPULoad(ctx)
	case experFlavorCPULoadCtnrs:
		if err := cleanupDocker(ctx); err != nil {
			return exitCodeErr
		}
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
