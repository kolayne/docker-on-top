#!/bin/false

CONTAINER_CMD_CHECK_INITIAL_DATA='
	# Check that the initial data is as expected
	[ "$(cat /dot/a)" = 123 ]
	[ "$(cat /dot/b)" = 456 ]
'

CONTAINER_CMD_MAKE_AND_CHECK_CHANGES='
	# My file removal is visible to me
	rm -f /dot/a
	[ ! -e /dot/a ]

	# My changes are visible to me
	echo 789 > /dot/b
	[ "$(cat /dot/b)" = 789 ]

	# My new files are visible to me
	echo etc > /dot/c
	[ "$(cat /dot/c)" = etc ]
'

CONTAINER_CMD_CHECK_ALREADY_MODIFIED_DATA='
	[ ! -e /dot/a ]
	[ "$(cat /dot/b)" = 789 ]
	[ "$(cat /dot/c)" = etc ]
'
