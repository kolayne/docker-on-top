#!/usr/bin/env bats

@test "invalid volume name" {
  # Special character as the first character is not allowed
  ! docker volume create --driver docker-on-top _invalidname

  # Only dots, hyphens, and underscores are allowed as special characters
  ! docker volume create --driver docker-on-top invalid\*name
}

@test "invalid base path" {
  # Only absolute paths are allowed as base paths
  ! docker volume create --driver docker-on-top valid-name -o base=\\
  ! docker volume create --driver docker-on-top valid-name -o base=a/b

  # Base directory shall exist
  ! docker volume create --driver docker-on-top valid-name -o base=/does/not/exist

  # TODO: if the base directory points to a file, that should be an error
  # ! docker volume create --driver docker-on-top valid-name -o base="$(realpath "$0")"
}
