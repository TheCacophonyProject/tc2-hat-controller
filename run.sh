#!/bin/bash

COMPONENT=${1:-"tc2-hat-comms"}

function check_valid_component {
    X=`ls -1 cmd | grep ${1}`

    if [ "x$X" = "x" ]; then
        echo "${1} isn't a valid build component."
        exit
    fi
}

check_valid_component $COMPONENT

docker build --build-arg COMPONENT=${COMPONENT} -t ${COMPONENT} .
docker run --rm --mount type=bind,source="$(pwd)"/out,target=/out -it ${COMPONENT}
docker image prune -f
if [ "x$2" != "x" ]; then
  scp "$(pwd)"/out/"${COMPONENT}" $2
fi

