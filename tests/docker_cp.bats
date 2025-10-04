#!/usr/bin/env bats

# For `CONTAINER_CMD_*`
load common.sh

@test "Docker cp-ing from a container" {
    # https://github.com/kolayne/docker-on-top/issues/18

    BASE="$(mktemp --directory)"
    NAME="$(basename "$BASE")"
    docker volume create --driver docker-on-top "$NAME" -o base="$BASE" -o volatile=true

    # Deferred cleanup
    trap 'rm -rf "$BASE"; docker container rm -f "$CONTAINER_ID"; docker volume rm "$NAME"; trap - RETURN' RETURN

    CONTAINER_ID=$(docker run --name "$NAME" -d -v "$NAME":/dot alpine:latest sh -e -c '
        echo 123 > /dot/b
        sleep 1
        # What the following checks is that after the second container is started, the overlay upperdir
        # is not cleared (which is what was happening before the issue was fixed). However, there is no
        # reliable way to test this, since what exactly happens in the errorneous case depends on the
        # implementation of the overlay filesystem.
        # For me, this seems to work (but just `cat /dot/b` does not fail):
        ls /dot | grep b && cat /dot/b
    ')

    sleep 0.5

    # It does not matter where we copy to, but I put it on the volume
    # to simplify the cleanup.
    docker cp "$NAME":/etc/passwd "$BASE/"

    docker run --rm -v "$NAME":/dot alpine:latest true

    [ 0 -eq "$(docker wait "$CONTAINER_ID")" ]
}
