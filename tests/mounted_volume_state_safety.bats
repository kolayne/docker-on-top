#!/usr/bin/env bats

# TODO: move the following explanation from here to the source code?

# Under normal operation, docker-on-top keeps all of its volumes in a
# _valid_ state. When interfered with (e.g., when the user messes with
# our internal state or in case we were terminated abruptly or whatever
# other reason causes our crucial syscalls to fail),
# it may not be possible to maintain a _valid_ state, but we still want to
# leave a volume in a state that is _safe_, that is, on the next interaction
# with the volume, docker-on-top can tell that something went wrong last time.

# For example, if a volume is unmounted improperly, such that `activemountsdir`
# was cleaned properly but we failed to unmount `mountpointdir`, that's unfortunate
# (as we leave a mount hanging) but tolerable (as the next attempt to mount it will
# succeed as if the volume was unmounted cleanly).
#
# On the contrary, if a volume is unmounted improperly, such that `mountpointdir`
# was unmounted but `activemountsdir` remains non-empty, that's a horror. On the
# next attempt to mount that volume, docker-on-top will see that it is already mounted
# and will try to use `mountpoindir` outright but, as it is not actually mounted, that
# will lead to a completely broken behavior.

# The following tests try to simulate that either mount/unmount system calls, or
# activemounts file creation/removal system calls fail, and tests that docker-on-top
# is fine.


# For `CONTAINER_CMD_*`
load common.sh

# Ask for password early
sudo -v

# Test template

template_cleanup() {
  $3 "$BASE" "$NAME"
  $5 "$BASE" "$NAME"
  sudo umount /var/lib/docker-on-top/"$NAME"/mountpoint || :
  sudo rmdir /var/lib/docker-on-top/"$NAME"/mountpoint  || :
  sudo rm /var/lib/docker-on-top/"$NAME"/activemounts/* || :
  docker rm "$CONTAINER_ID"
  docker volume rm "$NAME"
  rm -rf "$BASE"
}

# $1 is empty if volatile, non-empty if non-volatile;
# $2 is the command (messing with the state) to perform before mounting;
# $3 shall undo the effect of $2;
# $4 is the command (messing with the state) to perform before unmounting;
# $5 shall undo the effect of $4.
template() {
  # Setup
  BASE="$(mktemp --directory)"
  NAME="$(basename "$BASE")"
  if [ -z "$1" ]; then
    VOLATILE=false
  else
    VOLATILE=true
  fi
  docker volume create --driver docker-on-top "$NAME" -o base="$BASE" -o volatile="$VOLATILE"

  echo 123 > "$BASE"/a
  echo 456 > "$BASE"/b

  # Run the breaking action before mount
  $2 "$BASE" "$NAME"

  # Start the container (might fail)
  if ! CONTAINER_ID=$(
      docker run -d -v "$NAME":/dot alpine:latest \
        sh -e -c "
          sleep 1  # Give as a moment to break everything

          $CONTAINER_CMD_CHECK_INITIAL_DATA

          $CONTAINER_CMD_MAKE_AND_CHECK_CHANGES
        "
    ); then

    # This is the best case: docker-on-top has detected the problem very early
    # and did not mount anything in the first place
    template_cleanup "$@"
    return
  fi

  # Undo the breaking action
  $3 "$BASE" "$NAME"

  # Run the breaking action before unmount
  $4 "$BASE" "$NAME"

  if [ 0 -ne "$(docker wait "$CONTAINER_ID")" ]; then
    # Container has failed
    template_cleanup "$@"
    false
    return
  fi

  # Undo the breaking action
  $5 "$BASE" "$NAME"

  # Now that we are fixed, we expect that (I) either the container doesn't start at all:
  if ! docker run --rm -v "$NAME":/dot alpine:latest true; then
    template_cleanup "$@"
    return  # The test is successful: docker-on-top detected the error and the mount is refused
  fi

  # (II) Or the container starts and runs as expected:
  if $VOLATILE; then
    docker run --rm -v "$NAME":/dot alpine:latest \
      sh -e -c "
        $CONTAINER_CMD_CHECK_INITIAL_DATA

        $CONTAINER_CMD_MAKE_AND_CHECK_CHANGES
      "
  else
    docker run --rm -v "$NAME":/dot alpine:latest \
      sh -e -c "
        $CONTAINER_CMD_CHECK_ALREADY_MODIFIED_DATA

        $CONTAINER_CMD_MAKE_AND_CHECK_CHANGES
      "
  fi

  template_cleanup "$@"
}


# I use "activate"/"deactivate" as shorts for "create/remove a file at `activemountsdir`"

# Break/unbreak functions receive $BASE as $1 and $NAME as $2

break_deactivate() {
  # Immutable file can not be removed
  sudo chattr +i /var/lib/docker-on-top/"$2"/activemounts/*
}

unbreak_deactivate() {
  sudo chattr -i /var/lib/docker-on-top/"$2"/activemounts/*
}


@test "Mount=fine, activate=fine, unmount=fine, deactivate=broken" {
  template "" : : break_deactivate unbreak_deactivate

  template "volatile" : : break_deactivate unbreak_deactivate
}

break_unmount() {
  # The mountpoint will be kept busy with the following process that has a file on it open
  sleep 1.5 < /var/lib/docker-on-top/"$2"/mountpoint/b &
}

unbreak_unmount() {
  # Just wait for the previous process to finish
  sleep 1.5
}


@test "Mount=fine, activate=fine, unmount=broken, deactivate=fine" {
  skip "This is not working yet, needs to be fixed"

  template "" : : break_unmount unbreak_unmount

  template "volatile" : : break_unmount unbreak_unmount
}

break_activate() {
  sudo chattr +i /var/lib/docker-on-top/"$2"/activemounts
}

unbreak_activate() {
  sudo chattr -i /var/lib/docker-on-top/"$2"/activemounts
}

@test "Mount=fine, activate=broken, unmount=fine, deactivate=fine" {
  template "" break_activate unbreak_activate : :

  template "volatile" break_activate unbreak_activate : :
}

break_mount() {
  # If the base directory does not exist, the overlayfs mount shall fail
  rm -r "$1"
}

unbreak_mount() {
  mkdir "$1"
}

@test "Mount=broken, activate=fine, unmount=fine, deactivate=fine" {
  template "" break_mount unbreak_mount : :

  template "volatile" break_mount unbreak_mount : :
}
