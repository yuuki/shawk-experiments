# shawk-experiments

## Automated experiments for tracing

### Prepare

```shell-session
make
```

`make` generates 3 binaries `runexper`, `runtracer`, and `spawnctnr`.

1. Transfer `runtracer` and `spawnctnr` to Linux hosts for your experiments.
1. Download the [`lstf`](https://github.com/yuuki/lstf) binary to ditto.
1. Download the [`connperf`](https://github.com/yuuki/connperf/releases) binary to ditto.
1. Build and transfer the [`conntop`](https://github.com/yuuki/go-conntracer-bpf/) binary to ditto.

### runexper

`runexper` kicks commands for benchmarking and `runtracer` on experiments hosts via SSH while changing the various parameters.

Measuring CPU load.

```shell-session
runexper -exper-flavor cpu-load
```

Measuring latency.

```shell-session
runexper -exper-flavor latency
```

### runtracer

`runtracer` runs lstf and conntop on your experimental hosts.

### spawnctnr

`spawnctnr` spawns multiple connperf processes inside docker containers for either client and server.

# License

This project is licensed under the Apache License, Version 2.0. See [LICENSE](./LICENSE) for the full license text.
