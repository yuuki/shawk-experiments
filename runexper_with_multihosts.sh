#!/bin/bash

PREFIX_CMD='pueue add -- ./runexper -exper-flavor cpu-load-ctnrs -spawnctnr-flavor server -ctnr-vars 1000'
CTNR_HOSTS_FLAG='-ctnr-hosts'
CTNR_SERVER_HOSTS=('10.0.150.2' '10.0.0.22' '10.0.0.23' '10.0.0.24' '10.0.0.25' '10.0.0.26' '10.0.0.27' '10.0.0.28' '10.0.0.29' '10.0.0.30')

HOSTS=()
for host in ${CTNR_SERVER_HOSTS[@]}; do
    HOSTS+=(${host})
    ${PREFIX_CMD} ${CTNR_HOSTS_FLAG} $(IFS=",";echo "${HOSTS[*]}")
done
