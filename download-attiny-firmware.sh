#!/bin/bash

# Check for the correct number of arguments
if [ "$#" -ne 3 ]; then
    echo "Usage: source $0 <MAJOR> <MINOR> <PATCH>, using sourse lets it set environment variables"
    exit 1
fi

MAJOR=$1
MINOR=$2
PATCH=$3

RELEASE_DOWNLOAD=https://github.com/TheCacophonyProject/attiny1616/releases/download/v${MAJOR}.${MINOR}.${PATCH}

wget -O _release/attiny-firmware.hex ${RELEASE_DOWNLOAD}/firmware.hex
wget -O _release/attiny-firmware.hex.sha256 ${RELEASE_DOWNLOAD}/firmware.hex.sha256
export ATTINY_HASH=$(cut -d ' ' -f 1 < _release/attiny-firmware.hex.sha256)
export ATTINY_MAJOR=${MAJOR}
export ATTINY_MINOR=${MINOR}
export ATTINY_PATCH=${PATCH}
