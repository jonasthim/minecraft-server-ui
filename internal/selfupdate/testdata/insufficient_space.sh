#!/bin/sh
# Isolated memory filesystem failure; never fills or mounts over host data.
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
fixture=$(mktemp -d /home/thim/.cache/dilla-tmp/task24-enospc-XXXXXX)
mkdir "$fixture/stages"
(cd "$repo" && go test -c ./internal/selfupdate -o "$fixture/selfupdate.test")
unshare --user --map-root-user --mount sh -eu -c '
 mount -t tmpfs -o size=1m none "$1/stages"
 MINECRAFT_UI_TEST_STAGE_DISKFULL="$1/stages" "$1/selfupdate.test" -test.run "^TestStagingInsufficientSpace$" -test.v
' task24-enospc "$fixture"
