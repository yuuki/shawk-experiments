package main

import (
	"flag"
	"log"
	"os"
	"os/exec"
	"time"
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

func measureStats() {
	tick := time.NewTicker(intervalMeasurement)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			// measture
		default:
		}
	}
}

func waitAndKill(proc *os.Process) {
	timer := time.NewTimer(period)
	<-timer.C
	if err := proc.Kill(); err != nil {
		log.Fatal(err)
	}
}

func runLstf() error {
	cmd := exec.Command("./lstf", "-p", "-n", "--watch=1")
	if err := cmd.Start(); err != nil {
		return err
	}

	go waitAndKill(cmd.Process)

	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func runConntopUser() error {
	cmd := exec.Command("./conntop", "-streaming")
	if err := cmd.Start(); err != nil {
		return err
	}

	go waitAndKill(cmd.Process)

	// measurement

	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}

func runConntopKernel() error {
	cmd := exec.Command("./conntop", "-interval", "1s")
	if err := cmd.Start(); err != nil {
		return err
	}

	go waitAndKill(cmd.Process)

	// measurement

	if err := cmd.Wait(); err != nil {
		return err
	}
	return nil
}
