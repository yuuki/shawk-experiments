package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/process"
	"golang.org/x/xerrors"
)

const (
	exitCodeOk  = 0
	exitCodeErr = -1

	methodSnapshotPolling   = "snapshot-polling"
	methodUserAggregation   = "user-aggregation"
	methodKernelAggregation = "kernel-aggregation"
	methodAll               = "all"

	intervalMeasurement = 1 * time.Second
)

var (
	method string
	period time.Duration

	cmdByMethod = map[string][]string{
		methodSnapshotPolling:   []string{"./lstf", "-p", "-n", "--watch=1"},
		methodUserAggregation:   []string{"./conntop", "-user-aggr", "-interval", "1s"},
		methodKernelAggregation: []string{"./conntop", "-kernel-aggr", "-interval", "1s"},
	}
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&method, "method", methodAll, "method")
	flag.DurationVar(&period, "period", 30*time.Second, "period")
	flag.Parse()
}

func main() {
	os.Exit(run())
}

func run() int {
	log.Printf("Running method %q during period %q ...\n", method, period)

	switch method {
	case methodSnapshotPolling:
		if err := runCmd(cmdByMethod[methodSnapshotPolling]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	case methodUserAggregation:
		if err := runCmd(cmdByMethod[methodUserAggregation]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	case methodKernelAggregation:
		if err := runCmd(cmdByMethod[methodKernelAggregation]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	case methodAll:
		log.Printf("Running method %q during period %q ...\n", methodSnapshotPolling, period)
		if err := runCmd(cmdByMethod[methodSnapshotPolling]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
		log.Printf("Running method %q during period %q ...\n", methodUserAggregation, period)
		if err := runCmd(cmdByMethod[methodUserAggregation]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
		log.Printf("Running method %q during period %q ...\n", methodKernelAggregation, period)
		if err := runCmd(cmdByMethod[methodKernelAggregation]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	default:
		log.Printf("%q is unknown method\n", method)
		return exitCodeErr
	}

	return exitCodeOk
}

type cpuStat struct {
	CPUTotal  float64
	CPUUser   float64
	CPUSystem float64
	CPUIowait float64
}

func (s *cpuStat) PrintReport() {
	fmt.Println("--- CPU stats ---")
	fmt.Printf("total:%.2f%% user:%.2f%% system:%.2f%% iowait:%.2f%%\n",
		s.CPUTotal*100,
		s.CPUUser*100,
		s.CPUSystem*100,
		s.CPUIowait*100,
	)
}

func measureCPUStats(pid int) (*cpuStat, error) {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return nil, err
	}

	lastCPUTimes, err := proc.Times()
	if err != nil {
		return nil, err
	}

	time.Sleep(period)

	cpuTimes, err := proc.Times()
	if err != nil {
		return nil, err
	}

	deltaCPUUser := cpuTimes.User - lastCPUTimes.User
	deltaCPUSystem := cpuTimes.System - lastCPUTimes.System
	deltaCPUIowait := cpuTimes.Iowait - lastCPUTimes.Iowait
	deltaCPUTotal := deltaCPUUser + deltaCPUSystem + deltaCPUIowait

	stat := &cpuStat{
		CPUTotal:  deltaCPUTotal / period.Seconds(),
		CPUUser:   deltaCPUUser / period.Seconds(),
		CPUSystem: deltaCPUSystem / period.Seconds(),
		CPUIowait: deltaCPUIowait / period.Seconds(),
	}

	return stat, nil
}

func runCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("args length should be > 0")
	}

	cmd := exec.Command(args[0], args[1:]...)
	log.Printf("Kicking %q ...\n", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		time.Sleep(period)

		stat, err := measureCPUStats(cmd.Process.Pid)
		if err != nil {
			log.Fatal(err)
		}
		stat.PrintReport()

		cmd.Process.Kill()
	}()

	if err := cmd.Wait(); err != nil {
		eerr := &exec.ExitError{}
		// ignore exiterror
		if !errors.As(err, &eerr) {
			return xerrors.Errorf("wait command error: :%w \n", err)
		}
	}
	return nil
}
