#!/usr/bin/env bats

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
		sh -e -c '
			# Initial data is visible
			[ "$(cat /dot/a)" = 123 ]
			[ "$(cat /dot/b)" = 456 ]

			# My file removal is visible to me
			rm /dot/a
			[ ! -e /dot/a ]

			# My changes are visible to me
			echo 789 > /dot/b
			[ "$(cat /dot/b)" = 789 ]

			# My new files are visible to me
			echo etc > /dot/c
			[ "$(cat /dot/c)" = etc ]
		'

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
