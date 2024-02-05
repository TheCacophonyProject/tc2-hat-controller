#!/bin/bash

set -e

# make sure pymcuprog is installed for attiny programming.
#if ! command -v pymcuprog &> /dev/null; then
#    echo "pymcuprog not found, installing now"
#    apt-get install pipx -y
#    pipx ensurepath
#    pipx install pymcuprog==3.14.2.9
#fi

systemctl daemon-reload
# TODO Enabe when software is ready
systemctl disable tc2-hat-temp.service
systemctl stop tc2-hat-temp.service

systemctl enable tc2-hat-attiny.service
systemctl restart tc2-hat-attiny.service

systemctl enable rpi-reboot.service

systemctl enable tc2-hat-rtc.service

#systemctl enable tc2-hat-uart.service
#systemctl restart tc2-hat-uart.service
