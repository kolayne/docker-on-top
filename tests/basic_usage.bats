#!/usr/bin/env bats

# For `CONTAINER_CMD_*`
load common.sh

basic_test() {
	# Setup
	BASE="$(mktemp --directory)"
	NAME="$(basename "$BASE")"
	if [ -z "$1" ]; then
		VOLATILE=false
	else
		VOLATILE=true
	fi
	docker volume create --driver docker-on-top "$NAME" -o base="$BASE" -o volatile="$VOLATILE"

	# Deferred cleanup
	trap 'rm -rf "$BASE"; docker volume rm "$NAME"; trap - RETURN' RETURN

	echo 123 > "$BASE"/a
	echo 456 > "$BASE"/b

	docker run --rm -v "$NAME":/dot alpine:latest \
		sh -e -c "
			$CONTAINER_CMD_CHECK_INITIAL_DATA

			$CONTAINER_CMD_MAKE_AND_CHECK_CHANGES
		"

	# Changes are not visible from the host
	[ "$(cat "$BASE"/a)" = 123 ]
	[ "$(cat "$BASE"/b)" = 456 ]
	[ ! -e "$BASE"/c ]

	if $VOLATILE; then
		# For a volatile volume, changes should not be visible
		[ "$(docker run --rm -v "$NAME":/dot alpine:latest sh -c 'cat /dot/*')" = "$(echo 123; echo 456)" ]
	else
		# For a regular volume, changes should remain
		[ "$(docker run --rm -v "$NAME":/dot alpine:latest sh -c 'cat /dot/*')" = "$(echo 789; echo etc)" ]
	fi
}

@test "Test basic usage" {
	basic_test
}

@test "Test volatile basic usage" {
	basic_test volatile
}
