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
	"strconv"
	"strings"
	"sync"
	"syscall"
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

	runTracerPeriod        = 30 * time.Second
	connperfPersistentRate = 5

	connectionsForCtnrs = 10000

	connperfServerCmd   = "sudo GOMAXPROCS=4 taskset -a -c 0-3 ./connperf serve -l 0.0.0.0:9100"
	connperfClientCmd   = "sudo GOMAXPROCS=4 taskset -a -c 0-3 ./connperf connect %s --show-only-results 10.0.150.2:9100"
	spawnCtnrServerCmd1 = "./spawnctnr -flavor server -containers %d -host-network"
	spawnCtnrClientCmd1 = "./connperf connect %s --show-only-results"
	spawnCtnrServerCmd2 = connperfServerCmd
	spawnCtnrClientCmd2 = "./spawnctnr -flavor client -containers %d -host-network -client-cmd 'connect %s --show-only-results 10.0.150.2:9100'"
	runTracerCmd        = "sudo GOMAXPROCS=1 taskset -a -c 4-5 ./runtracer -method all"
	killConnperfCmd     = "sudo pkill -INT connperf"
	killSpawnCtnrCmd    = "sudo pkill -INT spawnctnr"

	pruneDocker   = "docker system prune -f"
	restartDocker = "sudo systemctl restart docker"
)

var (
	experFlavor     string
	spawnCtnrFlavor string
	protoFlavor     string
	protocol        string
	connNumVars     []int
	ctnrNumVars     []int
	ctnrHostVars    []string
	bpfProf         bool
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&experFlavor, "exper-flavor", experFlavorCPULoad, "experiment flavor")
	flag.StringVar(&spawnCtnrFlavor, "spawnctnr-flavor", "all", "spawnctnr flavor 'server' or 'client' or 'all")
	flag.StringVar(&protocol, "protocol", "all", "protocol (tcp or udp)")
	flag.StringVar(&protoFlavor, "protocol-flavor", "all", "tcp (ephemeral or peesistent), udp")
	var connNums string
	flag.StringVar(&connNums, "conn-vars", "5000,10000,15000,20000", "variants of the number of connections")
	var ctnrNums string
	flag.StringVar(&ctnrNums, "ctnr-vars", "200,400,600,800,1000", "variants of the number of containers")
	var ctnrHosts string
	flag.StringVar(&ctnrHosts, "ctnr-hosts", "", "variants of the hostname or ipaddrs of hosts")
	flag.BoolVar(&bpfProf, "bpf-profile", false, "bpf prof for conntop")
	flag.Parse()

	for _, s := range strings.Split(connNums, ",") {
		i, _ := strconv.Atoi(s)
		connNumVars = append(connNumVars, i)
	}
	for _, s := range strings.Split(ctnrNums, ",") {
		i, _ := strconv.Atoi(s)
		ctnrNumVars = append(ctnrNumVars, i)
	}
	ctnrHostVars = strings.Split(ctnrHosts, ",")
}

func spawnCtnrConnperfClientCmd1(flag string) string {
	var targets []string
	for _, host := range ctnrHostVars {
		targets = append(targets,
			fmt.Sprintf("$(curl -sS http://%s:8080/hostports)", host))
	}
	return spawnCtnrClientCmd1 + " " + strings.Join(targets, " ")
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

func sshCmdOnMultiHosts(ctx context.Context, cmd string, hosts []string, wg *sync.WaitGroup) (func(), error) {
	ecmds := make([]*exec.Cmd, 0, len(hosts))
	for _, host := range hosts {
		ecmd, out, err := sshCmd(ctx, host, cmd)
		if err != nil {
			return func() {}, err
		}
		ecmds = append(ecmds, ecmd)
		wg.Add(1)
		go func() {
			defer wg.Done()
			waitCmd(ecmd, host, cmd)
		}()
		go printCmdOut(out, host)
	}

	stop := func() {
		for _, ecmd := range ecmds {
			ecmd.Process.Signal(os.Interrupt)
		}
	}

	return stop, nil
}

func sshClientCmd(ctx context.Context, cmd string, wg *sync.WaitGroup) (func(), error) {
	if len(ctnrHostVars) > 1 && spawnCtnrFlavor == "client" {
		return sshCmdOnMultiHosts(ctx, cmd, ctnrHostVars, wg)
	}
	hosts := []string{defaultClientHost}
	return sshCmdOnMultiHosts(ctx, cmd, hosts, wg)
}

func sshServerCmd(ctx context.Context, cmd string, wg *sync.WaitGroup) (func(), error) {
	if len(ctnrHostVars) > 1 && spawnCtnrFlavor == "server" {
		return sshCmdOnMultiHosts(ctx, cmd, ctnrHostVars, wg)
	}
	hosts := []string{defaultServerHost}
	return sshCmdOnMultiHosts(ctx, cmd, hosts, wg)
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

	stopServer, err := sshServerCmd(ctx, tracerCmd, &wg)
	if err != nil {
		return err
	}
	defer stopServer()

	stopClient, err := sshClientCmd(ctx, tracerCmd, &wg)
	if err != nil {
		return err
	}
	defer stopClient()

	// wait until tracer has finished
	wg.Wait()
	return nil
}

func runCPULoadEach(ctx context.Context, connperfClientFlag string) error {
	wg := sync.WaitGroup{}
	_, err := sshServerCmd(ctx, connperfServerCmd, &wg)
	if err != nil {
		return err
	}

	// wait server
	time.Sleep(5 * time.Second)

	clientCmd := fmt.Sprintf(connperfClientCmd, connperfClientFlag)
	stop, err := sshClientCmd(ctx, clientCmd, &wg)
	if err != nil {
		return err
	}

	cleanup := func() {
		sshServerCmd(ctx, killConnperfCmd, &wg)
		stop()
	}

	// wait client
	time.Sleep(10 * time.Second)

	if err := runTracer(ctx, runTracerPeriod); err != nil {
		cleanup()
		return err
	}

	cleanup()
	wg.Wait()

	return nil
}

func runCPULoad(ctx context.Context) error {
	variants := connNumVars

	if protocol == "all" || protocol == "tcp" {
		if protoFlavor == "all" || protoFlavor == "ephemeral" {
			for _, rate := range variants {
				flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 1200s", rate)
				log.Println("parameter", flag)
				if err := runCPULoadEach(ctx, flag); err != nil {
					return err
				}
			}
		}
		if protoFlavor == "all" || protoFlavor == "persistent" {
			for _, conns := range variants {
				flag := fmt.Sprintf("--proto tcp --flavor persistent --rate %d --connections %d --duration 1200s", connperfPersistentRate, conns)
				log.Println("parameter", flag)
				if err := runCPULoadEach(ctx, flag); err != nil {
					return err
				}
			}
		}
	}
	if protocol == "all" || protocol == "udp" {
		if protoFlavor == "all" || protoFlavor == "udp" {
			for _, rate := range variants {
				flag := fmt.Sprintf("--proto udp --rate %d --duration 1200s", rate)
				log.Println("parameter", flag)
				if err := runCPULoadEach(ctx, flag); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func runCPULoadServerCtnrsEach(ctx context.Context, containers int, connperfClientFlag string) error {
	var wg sync.WaitGroup
	spawnCtnrServerCmd := fmt.Sprintf(spawnCtnrServerCmd1, containers)
	_, err := sshServerCmd(ctx, spawnCtnrServerCmd, &wg)
	if err != nil {
		return err
	}

	// wait server
	time.Sleep(5*time.Second + time.Duration(100*containers)*time.Millisecond)

	clientCmd := spawnCtnrConnperfClientCmd1(connperfClientFlag)
	stop, err := sshClientCmd(ctx, clientCmd, &wg)
	if err != nil {
		return err
	}
	cleanup := func() {
		sshServerCmd(ctx, killSpawnCtnrCmd, &wg)
		stop()
	}

	// wait client
	time.Sleep(10 * time.Second)

	if err := runTracer(ctx, runTracerPeriod); err != nil {
		cleanup()
		return err
	}

	cleanup()
	wg.Wait()

	return nil
}

func runCPULoadClientCtnrsEach(ctx context.Context, containers int, connperfClientFlag string) error {
	var wg sync.WaitGroup
	stopServer, err := sshServerCmd(ctx, spawnCtnrServerCmd2, &wg)
	if err != nil {
		return err
	}

	// wait server
	time.Sleep(5 * time.Second)

	spawnCtnrClientCmd := fmt.Sprintf(spawnCtnrClientCmd2, containers, connperfClientFlag)
	_, err = sshClientCmd(ctx, spawnCtnrClientCmd, &wg)
	if err != nil {
		return err
	}

	cleanup := func() {
		sshClientCmd(ctx, killSpawnCtnrCmd, &wg) // kill client
		stopServer()
	}

	// wait client
	time.Sleep(5*time.Second + time.Duration(100*containers)*time.Millisecond)

	if err := runTracer(ctx, runTracerPeriod); err != nil {
		cleanup()
		return err
	}

	cleanup()
	wg.Wait()

	return nil
}

func runCPULoadCtnrsOnMultiHosts(ctx context.Context, connections int) error {
	ctnrHosts := len(ctnrHostVars)
	return runCPULoadCtnrs(ctx, connections/ctnrHosts)
}

func runCPULoadCtnrs(ctx context.Context, connections int) error {
	if protocol == "all" || protocol == "tcp" {
		if protoFlavor == "all" || protoFlavor == "ephemeral" {
			for _, containers := range ctnrNumVars {
				rate := connections / containers
				flag := fmt.Sprintf("--proto tcp --flavor ephemeral --rate %d --duration 1200s", rate)
				log.Println("parameter", flag)
				if spawnCtnrFlavor == "all" || spawnCtnrFlavor == "server" {
					if err := runCPULoadServerCtnrsEach(ctx, containers, flag); err != nil {
						return err
					}
				}
				if spawnCtnrFlavor == "all" || spawnCtnrFlavor == "client" {
					if err := runCPULoadClientCtnrsEach(ctx, containers, flag); err != nil {
						return err
					}
				}
			}
		}

		if protoFlavor == "all" || protoFlavor == "persistent" {
			for _, containers := range ctnrNumVars {
				rate := connections / containers
				flag := fmt.Sprintf("--proto tcp --flavor persistent --connections %d --rate %d --duration 1200s", rate, connperfPersistentRate)
				log.Println("parameter", flag)
				if spawnCtnrFlavor == "all" || spawnCtnrFlavor == "server" {
					if err := runCPULoadServerCtnrsEach(ctx, containers, flag); err != nil {
						return err
					}
				}
				if spawnCtnrFlavor == "all" || spawnCtnrFlavor == "client" {
					if err := runCPULoadClientCtnrsEach(ctx, containers, flag); err != nil {
						return err
					}
				}
			}
		}
	}
	if protocol == "all" || protocol == "udp" {
		// udp
		for _, containers := range ctnrNumVars {
			rate := connections / containers
			flag := fmt.Sprintf("--proto udp --rate %d --duration 1200s", rate)
			log.Println("parameter", flag)
			if spawnCtnrFlavor == "all" || spawnCtnrFlavor == "server" {
				if err := runCPULoadServerCtnrsEach(ctx, containers, flag); err != nil {
					return err
				}
			}
			if spawnCtnrFlavor == "all" || spawnCtnrFlavor == "client" {
				if err := runCPULoadClientCtnrsEach(ctx, containers, flag); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func runLatencyWithoutTracer(ctx context.Context, connperfClientFlag string) error {
	var wg sync.WaitGroup

	stopServer, err := sshServerCmd(ctx, connperfServerCmd, &wg)
	if err != nil {
		return err
	}
	defer stopServer()

	// wait server
	time.Sleep(5 * time.Second)

	clientCmd := fmt.Sprintf(connperfClientCmd, connperfClientFlag)
	stop, err := sshClientCmd(ctx, clientCmd, &wg)
	if err != nil {
		return err
	}
	defer stop()

	// wait until connperf server and client have finished
	wg.Wait()

	return nil
}

func prepareTracer(ctx context.Context, method string) (func(), *sync.WaitGroup, error) {
	runTracerCmd := runTracerCmd + " -period 1200s" + " -method " + method
	if bpfProf {
		runTracerCmd += " " + "-bpf-profile"
	}

	wg := sync.WaitGroup{}
	stopServer, err := sshServerCmd(ctx, runTracerCmd, &wg)
	if err != nil {
		return func() {}, &wg, err
	}

	stopClient, err := sshClientCmd(ctx, runTracerCmd, &wg)
	if err != nil {
		return func() {}, &wg, err
	}

	clean := func() {
		stopClient()
		stopServer()
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
	variants := connNumVars

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
	gwg := sync.WaitGroup{}

	gwg.Add(1)
	go func() {
		defer gwg.Done()

		wg := sync.WaitGroup{}
		_, err := sshClientCmd(ctx, pruneDocker, &wg)
		if err != nil {
			log.Println(err)
		}
		wg.Wait()

		wg = sync.WaitGroup{}
		_, err = sshClientCmd(ctx, restartDocker, &wg)
		if err != nil {
			log.Println(err)
		}
		wg.Wait()
	}()

	gwg.Add(1)
	go func() {
		defer gwg.Done()

		wg := sync.WaitGroup{}
		_, err := sshServerCmd(ctx, pruneDocker, &wg)
		if err != nil {
			log.Println(err)
		}
		wg.Wait()

		wg = sync.WaitGroup{}
		_, err = sshServerCmd(ctx, restartDocker, &wg)
		if err != nil {
			log.Println(err)
		}
		wg.Wait()
	}()

	gwg.Wait()
	return nil
}

func run() int {
	sig := make(chan os.Signal, 1)
	defer close(sig)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

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
		if len(ctnrHostVars) > 1 {
			err = runCPULoadCtnrsOnMultiHosts(ctx, connectionsForCtnrs)
		} else {
			err = runCPULoadCtnrs(ctx, connectionsForCtnrs)
		}
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
