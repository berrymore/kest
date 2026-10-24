#!/bin/sh
# Copies the read-only source tree at /src (without host build output and
# composer packages) and runs the PHP integration and end-to-end tests.
set -eu
mkdir -p /work
tar -C /src --exclude=./.git --exclude=./bin --exclude='*/vendor' -cf - . | tar -C /work -xf -
cd /work
php -v | head -1
go version
make test-integration
