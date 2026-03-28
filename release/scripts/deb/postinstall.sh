#!/bin/sh
set -e
systemctl daemon-reload
systemctl enable --now pve-imds.service
