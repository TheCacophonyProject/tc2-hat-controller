#!/bin/bash

set -e

# make sure pymcuprog is installed for attiny programming.
if ! command -v pymcuprog &> /dev/null; then
    echo "pymcuprog not found, installing now"
    pip3 install pymcuprog==3.14.2.9
fi

systemctl daemon-reload
systemctl enable tc2-hat-temp.service
systemctl restart tc2-hat-temp.service
systemctl enable tc2-hat-attiny.service
systemctl restart tc2-hat-attiny.service
