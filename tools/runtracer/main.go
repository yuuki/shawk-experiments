package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
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
	method     string
	period     time.Duration
	bpfProfile bool

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
	flag.BoolVar(&bpfProfile, "bpf-profile", false, "bpf profile")
	flag.Parse()
}

func main() {
	os.Exit(run())
}

func run() int {
	log.Printf("Running method %q during period %q ...\n", method, period)

	if bpfProfile {
		if err := disableBPFProfile(); err != nil {
			log.Printf("%+v\n", err)
			return exitCodeErr
		}
	}

	switch method {
	case methodSnapshotPolling:
		if err := runCmd(cmdByMethod[methodSnapshotPolling]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	case methodUserAggregation:
		if err := runCmdWithBPFProfile(cmdByMethod[methodUserAggregation]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	case methodKernelAggregation:
		if err := runCmdWithBPFProfile(cmdByMethod[methodKernelAggregation]); err != nil {
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
		if err := runCmdWithBPFProfile(cmdByMethod[methodUserAggregation]); err != nil {
			log.Println(err)
			return exitCodeErr
		}
		log.Printf("Running method %q during period %q ...\n", methodKernelAggregation, period)
		if err := runCmdWithBPFProfile(cmdByMethod[methodKernelAggregation]); err != nil {
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

func enableBPFProfile() error {
	cmd := exec.Command("sysctl", "-w", "kernel.bpf_stats_enabled=1")
	if err := cmd.Run(); err != nil {
		return xerrors.Errorf("enable bpf profile error: %w")
	}
	return nil
}

func disableBPFProfile() error {
	cmd := exec.Command("sysctl", "-w", "kernel.bpf_stats_enabled=0")
	if err := cmd.Run(); err != nil {
		return xerrors.Errorf("enable bpf profile error: %w")
	}
	return nil
}

// BpfProgramStats is a stattistics of BPF program.
type BpfProgramStats struct {
	Name     string        `json:"name"`
	RunCount uint          `json:"run_count"`
	RunTime  time.Duration `json:"run_time"`
}

func getBPFStats() (map[int]*BpfProgramStats, error) {
	resp, err := http.Get("http://localhost:6060/bpf/stats")
	if err != nil {
		return nil, xerrors.Errorf("could not get bpf stats: %w", err)
	}
	stats := map[int]*BpfProgramStats{}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, xerrors.Errorf("could not decode json body bpf stats: %w", err)
	}
	return stats, nil
}

func printBPFStats(stats map[int]*BpfProgramStats) {
	fmt.Println("--- BPF stats ---")
	for _, stat := range stats {
		if stat.RunCount == 0 {
			continue
		}
		avgRunTime := stat.RunTime / time.Duration(stat.RunCount)
		fmt.Printf("name:%s run_cnt:%d avg_run_time_ns:%d\n",
			stat.Name,
			stat.RunCount,
			avgRunTime.Nanoseconds(),
		)
	}
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

func runCmdWithBPFProfile(args []string) error {
	if bpfProfile {
		enableBPFProfile()
		defer disableBPFProfile()
		args = append(args, "-prof")
	}
	fn := func(pid int) {
		stat, err := measureCPUStats(pid)
		if err != nil {
			log.Fatal(err)
		}
		stat.PrintReport()

		bpfStat, err := getBPFStats()
		if err != nil {
			log.Fatal(err)
		}
		printBPFStats(bpfStat)
	}
	return runCmdWithReport(args, fn)
}

func runCmd(args []string) error {
	fn := func(pid int) {
		stat, err := measureCPUStats(pid)
		if err != nil {
			log.Fatal(err)
		}
		stat.PrintReport()
	}
	return runCmdWithReport(args, fn)
}

func runCmdWithReport(args []string, reportFn func(pid int)) error {
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

		reportFn(cmd.Process.Pid)

		if err := cmd.Process.Kill(); err != nil {
			time.Sleep(1 * time.Second)
			cmd.Process.Kill()
		}
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
