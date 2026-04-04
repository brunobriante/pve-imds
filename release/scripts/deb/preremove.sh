#!/bin/sh
set -e
if [ -z "$DPKG_ROOT" ] && [ "$1" = remove ] && [ -d /run/systemd/system ] ; then
        deb-systemd-invoke stop 'pve-imds.service' >/dev/null || true
fi
