#!/usr/bin/env bash
# One-time Raspberry Pi setup for Lull.
# Run this ON THE PI. Everything else is a single binary you scp over.
set -euo pipefail

echo "== enabling I2C =="
sudo raspi-config nonint do_i2c 0

echo "== installing i2c tools (for i2cdetect) =="
sudo apt-get update -qq
sudo apt-get install -y -qq i2c-tools

echo
echo "== scanning the I2C bus =="
echo "This is THE command that decides your build. Match the address:"
echo "  0x53 -> ADXL345   : Branch A, accelerometer primary"
echo "  0x19 / 0x18 -> LIS3DHTR : Branch A, accelerometer primary"
echo "  0x4C -> MMA7660FC : Branch B, 6-bit, acoustic becomes primary"
echo
i2cdetect -y 1

echo
echo "== adding user to i2c and dialout groups =="
sudo usermod -aG i2c,dialout "$USER"
echo "log out and back in for group changes to apply"

echo
echo "== next =="
echo "  1. build on your laptop:  make pi"
echo "  2. copy it over:          make deploy PI=$USER@\$(hostname).local"
echo "  3. run it here:           ./lull -source=serial"
