#!/bin/bash
set -e

systemctl daemon-reload

systemctl enable tc2-hat-i2c
systemctl restart tc2-hat-i2c

systemctl enable tc2-hat-rtc
systemctl restart tc2-hat-rtc

# TODO Enabe when software is ready
systemctl disable tc2-hat-temp.service
systemctl stop tc2-hat-temp.service

systemctl enable tc2-hat-attiny.service
systemctl restart tc2-hat-attiny.service

systemctl enable rpi-reboot.service

#systemctl enable tc2-hat-uart.service
#systemctl restart tc2-hat-uart.service
