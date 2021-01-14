package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/process"
)

const (
	exitCodeOk  = 0
	exitCodeErr = -1

	methodSnapshotPooling   = "snapshot-pooling"
	methodUserAggregation   = "user-aggregation"
	methodKernelAggregation = "kernel-aggregation"

	intervalMeasurement = 1 * time.Second
)

var (
	method string
	period time.Duration
)

func init() {
	log.SetFlags(0)

	flag.StringVar(&method, "method", methodKernelAggregation, "method")
	flag.DurationVar(&period, "period", 30*time.Second, "period")
	flag.Parse()
}

func main() {
	os.Exit(run())
}

func run() int {
	log.Printf("Running method %q during period %q ...\n", method, period)

	switch method {
	case methodSnapshotPooling:
		if err := runLstf(); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	case methodUserAggregation:
		if err := runConntopUser(); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	case methodKernelAggregation:
		if err := runConntopKernel(); err != nil {
			log.Println(err)
			return exitCodeErr
		}
	default:
		log.Printf("%q is unknown method\n", method)
		return exitCodeErr
	}

	return exitCodeOk
}

type stat struct {
	CPU      *cpu.TimesStat
	CPUTotal float64
}

func (s *stat) Print() {
	fmt.Printf("%+v\n", s)
}

func measureStats(pid int, stopTimer *time.Timer) (*stat, error) {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return nil, err
	}

	tick := time.NewTicker(intervalMeasurement)
	defer tick.Stop()

	cpuStats := make([]*cpu.TimesStat, 0)
	cnt := 0

	for {
		select {
		case <-tick.C:
			cpuTimes, err := proc.Times()
			if err != nil {
				return nil, err
			}
			cpuStats = append(cpuStats, cpuTimes)

			cnt++
		case <-stopTimer.C:
			goto END
		default:
		}
	}
END:

	reportStat := &stat{
		CPU: &cpu.TimesStat{},
	}

	// average
	for _, c := range cpuStats {
		reportStat.CPU.User += c.User
		reportStat.CPU.System += c.System
		reportStat.CPU.Iowait += c.Iowait
		reportStat.CPUTotal += c.User + c.System + c.Iowait
	}
	reportStat.CPUTotal /= float64(cnt)
	reportStat.CPU.User /= float64(cnt)
	reportStat.CPU.System /= float64(cnt)

	return reportStat, nil
}

func runLstf() error {
	cmd := exec.Command("./lstf", "-p", "-n", "--watch=1")
	log.Printf("Kicking %q ...\n", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		time.Sleep(period)
		if err := cmd.Process.Kill(); err != nil {
			log.Fatal(err)
		}
	}()
	go func() {
		timer := time.NewTimer(period)
		stat, err := measureStats(cmd.Process.Pid, timer)
		if err != nil {
			log.Fatal(err)
		}
		stat.Print()
	}()

	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func runConntopUser() error {
	cmd := exec.Command("./conntop", "-streaming")
	log.Printf("Kicking %q ...\n", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		time.Sleep(period)
		if err := cmd.Process.Kill(); err != nil {
			log.Fatal(err)
		}
	}()

	// measurement

	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func runConntopKernel() error {
	cmd := exec.Command("./conntop", "-interval", "1s")
	log.Printf("Kicking %q ...\n", strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		time.Sleep(period)
		if err := cmd.Process.Kill(); err != nil {
			log.Fatal(err)
		}
	}()

	// measurement

	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}
